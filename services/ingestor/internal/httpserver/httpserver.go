// Package httpserver serves the ingestor's operational endpoints: Prometheus /metrics and
// /healthz.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Checker reports whether the service is healthy: nil when it is.
type Checker interface {
	Check() error
}

// CheckFunc adapts a function to Checker.
type CheckFunc func() error

// Check implements Checker.
func (f CheckFunc) Check() error { return f() }

// Handler routes GET /metrics to the metrics in g and GET /healthz to c: 200 "ok" when c
// passes, 503 with its error otherwise.
func Handler(g prometheus.Gatherer, c Checker) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(g, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := c.Check(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintln(w, err)
			return
		}
		_, _ = fmt.Fprintln(w, "ok")
	})
	return mux
}

// shutdownGrace is how long Serve lets in-flight requests finish after ctx is done. Scrapes and
// health checks are short, and losing one at shutdown is harmless.
const shutdownGrace = time.Second

// Serve serves h on ln until ctx is done, then shuts down and returns nil. The caller opens
// ln, so a port already in use fails at startup.
//
// Shutdown is graceful for up to shutdownGrace, then forced. Clients such as Go's own
// Transport can hold connections that never send a request, and a graceful Shutdown waits
// seconds for those.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("http server shutdown: %w", err)
		}
		if err := srv.Close(); err != nil {
			return fmt.Errorf("http server close: %w", err)
		}
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Probe GETs url with client and returns nil on a 200. It is the container healthcheck:
// distroless images have no curl or wget.
func Probe(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return nil
}
