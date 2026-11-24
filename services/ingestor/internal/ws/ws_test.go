package ws_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/ws"
)

// echoServer is an in-process WebSocket server that echoes every message. It returns its URL.
func echoServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		c.SetReadLimit(16 << 20)
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, b); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestRoundTrip runs the Dialer against an echo server, including a message above the
// library's default 32 KB read limit.
func TestRoundTrip(t *testing.T) {
	ctx := t.Context()
	conn, err := ws.Dialer{}.Dial(ctx, echoServer(t))
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	for _, msg := range []string{`{"method":"ping"}`, `"` + strings.Repeat("x", 200_000) + `"`} {
		if err := conn.Write(ctx, []byte(msg)); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		got, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if string(got) != msg {
			t.Fatalf("Read() = %d bytes, want the %d bytes written", len(got), len(msg))
		}
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := conn.Read(cctx); err == nil {
		t.Error("Read() with a done context succeeded")
	}
}

// TestReadLimit checks that ReadLimit replaces the default: a message above 1 MB is read with
// a larger limit and rejected without one.
func TestReadLimit(t *testing.T) {
	ctx := t.Context()
	url := echoServer(t)
	msg := []byte(`"` + strings.Repeat("x", 2<<20) + `"`)

	big, err := ws.Dialer{ReadLimit: 4 << 20}.Dial(ctx, url)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = big.Close() })
	if err := big.Write(ctx, msg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got, err := big.Read(ctx); err != nil || len(got) != len(msg) {
		t.Fatalf("Read() = %d bytes, %v, want %d bytes", len(got), err, len(msg))
	}

	small, err := ws.Dialer{}.Dial(ctx, url)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = small.Close() })
	if err := small.Write(ctx, msg); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := small.Read(ctx); err == nil {
		t.Fatal("Read() of a message above DefaultReadLimit succeeded")
	}
}
