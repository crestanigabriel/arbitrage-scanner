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

// TestRoundTrip runs the Dialer against an in-process echo server, including a message above
// the library's default 32 KB read limit.
func TestRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		c.SetReadLimit(1 << 20)
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

	ctx := t.Context()
	conn, err := ws.Dialer{}.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"))
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
