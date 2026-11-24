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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/config"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/kraken"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/pipeline"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/publish"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/throttle"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/ws"
)

// Wiring constants that are not worth configuring.
const (
	reconnectBase = 500 * time.Millisecond
	reconnectMax  = 30 * time.Second
	// Kraken sends a heartbeat every second while subscribed.
	idleTimeout = 10 * time.Second
	// Room for bursts (a snapshot of trades, a reconnect) between the source and the pipeline.
	eventBuffer = 1024
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.LookupEnv, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ingestor:", err)
		os.Exit(1)
	}
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
		"nats_url", cfg.NATSURL, "kraken_ws_url", cfg.KrakenWSURL)

	clk := clock.Real{}

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
	publisher := publish.NewJetStream(js)

	backoff := exchange.NewBackoff(reconnectBase, reconnectMax, rand.N[time.Duration]) // jitter, not a security value
	connector := exchange.NewReconnector(cfg.KrakenWSURL, ws.Dialer{}, clk, backoff, log.With("exchange", kraken.Exchange))
	source, err := kraken.New(kraken.Config{
		Symbols:     cfg.Symbols,
		Depth:       cfg.BookDepth,
		IdleTimeout: idleTimeout,
	}, connector, clk, log)
	if err != nil {
		return err
	}

	bookThrottle := throttle.New[pipeline.BookKey, exchange.BookUpdate](clk, cfg.BookThrottle)
	pipe := pipeline.New(publisher, bookThrottle, clk, uint32(cfg.BookDepth), log) //nolint:gosec // 1-100, validated by config

	events := make(chan exchange.Event, eventBuffer)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer close(events)
		return source.Run(gctx, events)
	})
	g.Go(func() error { return pipe.Run(gctx, events) })
	err = g.Wait()
	if err == nil || errors.Is(err, context.Canceled) {
		log.Info("stopped")
		return nil
	}
	return err
}
