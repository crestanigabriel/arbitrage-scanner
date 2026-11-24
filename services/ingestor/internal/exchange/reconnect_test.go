package exchange_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock/clocktest"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/exchangetest"
)

const url = "wss://example.test/ws"

type harness struct {
	clock   *clocktest.Fake
	dialer  *exchangetest.Dialer
	backoff *exchange.Backoff
	rc      *exchange.Reconnector
}

func newHarness() *harness {
	h := &harness{
		clock:   clocktest.NewFake(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)),
		dialer:  exchangetest.NewDialer(),
		backoff: exchange.NewBackoff(time.Second, time.Minute, noJitter),
	}
	h.rc = exchange.NewReconnector(url, h.dialer, h.clock, h.backoff, slog.New(slog.DiscardHandler))
	return h
}

// start runs the reconnector in the background and returns a channel with its result.
func (h *harness) start(ctx context.Context, s exchange.Session) <-chan error {
	done := make(chan error, 1)
	go func() { done <- h.rc.Run(ctx, s) }()
	return done
}

func TestReconnectorRedialsWithBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		ctx, cancel := context.WithCancel(t.Context())
		sessions := 0
		conns := []*exchangetest.Conn{exchangetest.NewConn(), exchangetest.NewConn(), exchangetest.NewConn()}
		for _, c := range conns {
			h.dialer.Results <- exchangetest.DialResult{Conn: c}
		}
		done := h.start(ctx, func(context.Context, exchange.Conn, func()) error {
			sessions++
			return errors.New("connection reset")
		})

		synctest.Wait()
		if sessions != 1 || h.dialer.Dials() != 1 {
			t.Fatalf("sessions = %d, dials = %d; want 1, 1", sessions, h.dialer.Dials())
		}
		if !conns[0].IsClosed() {
			t.Error("the connection of a finished session was not closed")
		}

		// The first wait is base/2 with no jitter: nothing happens before it ends.
		h.clock.Advance(499 * time.Millisecond)
		synctest.Wait()
		if h.dialer.Dials() != 1 {
			t.Fatalf("redialed after 499ms, before the backoff ended")
		}
		h.clock.Advance(time.Millisecond)
		synctest.Wait()
		if h.dialer.Dials() != 2 {
			t.Fatalf("dials = %d after the first backoff, want 2", h.dialer.Dials())
		}

		// The second wait doubles.
		h.clock.Advance(999 * time.Millisecond)
		synctest.Wait()
		if h.dialer.Dials() != 2 {
			t.Fatalf("redialed before the second backoff ended")
		}
		h.clock.Advance(time.Millisecond)
		synctest.Wait()
		if h.dialer.Dials() != 3 {
			t.Fatalf("dials = %d after the second backoff, want 3", h.dialer.Dials())
		}

		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() = %v after cancel, want nil", err)
		}
	})
}

func TestReconnectorHealthyResetsBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.backoff.Next() // pretend earlier failures grew the backoff
		h.backoff.Next()
		h.dialer.Results <- exchangetest.DialResult{Conn: exchangetest.NewConn()}
		h.dialer.Results <- exchangetest.DialResult{Conn: exchangetest.NewConn()}
		h.start(t.Context(), func(_ context.Context, _ exchange.Conn, healthy func()) error {
			healthy()
			return errors.New("connection reset")
		})

		synctest.Wait()
		h.clock.Advance(500 * time.Millisecond) // base/2: only enough after a reset
		synctest.Wait()
		if h.dialer.Dials() != 2 {
			t.Fatalf("dials = %d, want 2: healthy() did not reset the backoff", h.dialer.Dials())
		}
	})
}

func TestReconnectorRetriesDialErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.dialer.Results <- exchangetest.DialResult{Err: errors.New("no route to host")}
		ran := make(chan struct{}, 1)
		h.dialer.Results <- exchangetest.DialResult{Conn: exchangetest.NewConn()}
		h.start(t.Context(), func(ctx context.Context, _ exchange.Conn, _ func()) error {
			ran <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})

		synctest.Wait()
		h.clock.Advance(500 * time.Millisecond)
		synctest.Wait()
		select {
		case <-ran:
		default:
			t.Fatal("no session after a failed dial and one backoff")
		}
	})
}

func TestReconnectorStopsOnPermanentError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		h.dialer.Results <- exchangetest.DialResult{Conn: exchangetest.NewConn()}
		cause := errors.New("currency pair not supported")
		done := h.start(t.Context(), func(context.Context, exchange.Conn, func()) error {
			return exchange.Permanent(cause)
		})

		err := <-done
		if !errors.Is(err, cause) {
			t.Fatalf("Run() = %v, want the permanent error %v", err, cause)
		}
		if h.dialer.Dials() != 1 {
			t.Errorf("dials = %d, want 1 (no reconnect after a permanent error)", h.dialer.Dials())
		}
	})
}

func TestReconnectorCancelDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness()
		ctx, cancel := context.WithCancel(t.Context())
		h.dialer.Results <- exchangetest.DialResult{Err: errors.New("refused")}
		done := h.start(ctx, func(context.Context, exchange.Conn, func()) error { return nil })

		synctest.Wait()
		if h.clock.Pending() != 1 {
			t.Fatalf("Pending() = %d, want 1 backoff timer", h.clock.Pending())
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() = %v, want nil", err)
		}
		if h.clock.Pending() != 0 {
			t.Error("backoff timer left armed after cancel")
		}
	})
}
