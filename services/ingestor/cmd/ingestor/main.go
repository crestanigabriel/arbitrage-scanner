// Command ingestor streams exchange order books and trades and publishes them to NATS
// JetStream.
//
// This file is the service's composition root: concrete dependencies are constructed here and
// only here, then injected into the components that use them.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"golang.org/x/sync/errgroup"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/config"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/coinbase"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/kraken"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/httpserver"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/metrics"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/pipeline"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/publish"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/throttle"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/ws"
)

// Wiring constants that are not worth configuring.
const (
	// The backoff also keeps reconnects well under Coinbase's 8 connections per second.
	reconnectBase = 500 * time.Millisecond
	reconnectMax  = 30 * time.Second
	// Kraken and Coinbase both send a heartbeat every second while subscribed.
	idleTimeout = 10 * time.Second
	// A Coinbase book snapshot is one message: about 4 MB for BTC-USD on 2026-11-24.
	coinbaseReadLimit = 32 << 20
	// Room for bursts (a snapshot of trades, a reconnect) between the sources and the pipeline.
	eventBuffer = 1024
	// How long `ingestor healthcheck` waits for /healthz.
	probeTimeout = 3 * time.Second
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(os.LookupEnv); err != nil {
			fmt.Fprintln(os.Stderr, "ingestor healthcheck:", err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.LookupEnv, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ingestor:", err)
		os.Exit(1)
	}
}

// healthcheck asks the running ingestor's /healthz, on HTTP_ADDR, whether it is healthy. It is
// the container healthcheck: the distroless image has no curl or wget.
func healthcheck(lookupEnv func(string) (string, bool)) error {
	cfg, err := config.Load(lookupEnv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	return httpserver.Probe(ctx, &http.Client{Timeout: probeTimeout}, probeURL(cfg.HTTPAddr))
}

// probeURL is the /healthz URL for a listen address: a wildcard host is reached on loopback.
func probeURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil { // config.Load validated it
		host, port = "", addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

// run wires the service and runs it until ctx is done. Configuration comes through lookupEnv
// and logs go to logOut, so tests can drive it.
func run(ctx context.Context, lookupEnv func(string) (string, bool), logOut io.Writer) error {
	cfg, err := config.Load(lookupEnv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(logOut, &slog.HandlerOptions{Level: cfg.LogLevel}))
	log.InfoContext(ctx, "starting",
		"symbols", cfg.Symbols, "book_depth", cfg.BookDepth, "book_throttle", cfg.BookThrottle.String(),
		"nats_url", cfg.NATSURL, "kraken_ws_url", cfg.KrakenWSURL, "coinbase_ws_url", cfg.CoinbaseWSURL,
		"http_addr", cfg.HTTPAddr)

	clk := clock.Real{}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m, err := metrics.New(reg)
	if err != nil {
		return fmt.Errorf("metrics: %w", err)
	}

	closed := make(chan struct{})
	nc, err := nats.Connect(cfg.NATSURL,
		nats.Name("arbscanner-ingestor"),
		nats.MaxReconnects(-1),
		nats.ClosedHandler(func(*nats.Conn) { close(closed) }),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil { // nil on a deliberate close, such as the drain at shutdown
				log.Warn("nats disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) { log.Info("nats reconnected") }),
	)
	if err != nil {
		return fmt.Errorf("connect to nats at %s: %w", cfg.NATSURL, err)
	}
	defer func() {
		// Flush what is in flight, then wait for the connection to close.
		if err := nc.Drain(); err != nil {
			log.Warn("nats drain", "error", err)
			nc.Close()
		}
		<-closed
	}()
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}
	if err := publish.EnsureStreams(ctx, js); err != nil {
		return err
	}
	publisher := publish.NewJetStream(js, js)

	// One source per exchange, each with its own connection, reconnect backoff and metrics.
	connector := func(ex, url string, readLimit int64) exchange.Connector {
		backoff := exchange.NewBackoff(reconnectBase, reconnectMax, rand.N[time.Duration]) // jitter, not a security value
		return exchange.NewReconnector(url, ws.Dialer{ReadLimit: readLimit}, clk, backoff, m.Reconnects(ex), log.With("exchange", ex))
	}
	krakenSource, err := kraken.New(kraken.Config{
		Symbols:     cfg.Symbols,
		Depth:       cfg.BookDepth,
		IdleTimeout: idleTimeout,
	}, connector(kraken.Exchange, cfg.KrakenWSURL, 0), clk, log)
	if err != nil {
		return err
	}
	coinbaseSource, err := coinbase.New(coinbase.Config{
		Symbols:     cfg.Symbols,
		Depth:       cfg.BookDepth,
		IdleTimeout: idleTimeout,
	}, connector(coinbase.Exchange, cfg.CoinbaseWSURL, coinbaseReadLimit), clk, log)
	if err != nil {
		return err
	}
	sources := []exchange.MarketSource{krakenSource, coinbaseSource}

	bookThrottle := throttle.New[pipeline.BookKey, exchange.BookUpdate](clk, cfg.BookThrottle)
	pipe := pipeline.New(publisher, bookThrottle, m, clk, uint32(cfg.BookDepth), log) //nolint:gosec // 1-100, validated by config

	health := httpserver.CheckFunc(func() error {
		if st := nc.Status(); st != nats.CONNECTED {
			return fmt.Errorf("nats: %s", st)
		}
		return nil
	})
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("http listen: %w", err)
	}

	events := make(chan exchange.Event, eventBuffer)
	g, gctx := errgroup.WithContext(ctx)
	var running sync.WaitGroup
	for _, src := range sources {
		running.Add(1)
		g.Go(func() error {
			defer running.Done()
			return src.Run(gctx, events)
		})
	}
	g.Go(func() error {
		running.Wait() // every source has stopped sending
		close(events)
		return nil
	})
	g.Go(func() error { return pipe.Run(gctx, events) })
	g.Go(func() error { return httpserver.Serve(gctx, ln, httpserver.Handler(reg, health)) })
	err = g.Wait()
	if err == nil || errors.Is(err, context.Canceled) {
		log.Info("stopped")
		return nil
	}
	return err
}
