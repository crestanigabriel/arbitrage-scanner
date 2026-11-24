package httpserver_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/httpserver"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))
	return rec.Code, rec.Body.String()
}

func TestHandler(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "things_total", Help: "Things."})
	reg.MustRegister(c)
	c.Add(3)

	var health error
	h := httpserver.Handler(reg, httpserver.CheckFunc(func() error { return health }))

	if code, body := get(t, h, "/metrics"); code != http.StatusOK || !strings.Contains(body, "things_total 3") {
		t.Errorf("/metrics = %d %q, want 200 with things_total 3", code, body)
	}
	if code, body := get(t, h, "/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Errorf("/healthz = %d %q, want 200 ok", code, body)
	}
	health = errors.New("nats: DISCONNECTED")
	if code, body := get(t, h, "/healthz"); code != http.StatusServiceUnavailable || !strings.Contains(body, "DISCONNECTED") {
		t.Errorf("/healthz = %d %q, want 503 with the check error", code, body)
	}
	if code, _ := get(t, h, "/nope"); code != http.StatusNotFound {
		t.Errorf("/nope = %d, want 404", code)
	}
}

func TestServeAndProbe(t *testing.T) {
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var down atomic.Bool
	h := httpserver.Handler(prometheus.NewRegistry(), httpserver.CheckFunc(func() error {
		if down.Load() {
			return errors.New("down")
		}
		return nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- httpserver.Serve(ctx, ln, h) }()

	url := "http://" + ln.Addr().String() + "/healthz"
	if err := httpserver.Probe(t.Context(), http.DefaultClient, url); err != nil {
		t.Errorf("Probe() = %v, want nil while healthy", err)
	}
	down.Store(true)
	if err := httpserver.Probe(t.Context(), http.DefaultClient, url); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("Probe() = %v, want a 503 error", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve() = %v after cancel, want nil", err)
	}
	if err := httpserver.Probe(t.Context(), http.DefaultClient, url); err == nil {
		t.Error("Probe() succeeded after the server stopped")
	}
}

func TestServeReportsListenerFailure(t *testing.T) {
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	err = httpserver.Serve(t.Context(), ln, http.NotFoundHandler())
	if err == nil {
		t.Fatal("Serve() on a closed listener returned nil")
	}
}
