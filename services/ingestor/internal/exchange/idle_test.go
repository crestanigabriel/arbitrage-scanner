package exchange_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock/clocktest"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/exchangetest"
)

func TestIdleTimeout(t *testing.T) {
	const timeout = 10 * time.Second
	synctest.Test(t, func(t *testing.T) {
		clk := clocktest.NewFake(time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC))
		inner := exchangetest.NewConn()
		conn := exchange.WithIdleTimeout(inner, clk, timeout)

		type result struct {
			msg []byte
			err error
		}
		read := func() <-chan result {
			ch := make(chan result, 1)
			go func() {
				msg, err := conn.Read(t.Context())
				ch <- result{msg, err}
			}()
			synctest.Wait()
			return ch
		}

		// Each message restarts the timeout.
		for range 3 {
			ch := read()
			clk.Advance(timeout - time.Millisecond)
			inner.Push("ping")
			if r := <-ch; r.err != nil || string(r.msg) != "ping" {
				t.Fatalf("Read() = %q, %v, want ping", r.msg, r.err)
			}
		}

		ch := read()
		clk.Advance(timeout)
		if r := <-ch; !errors.Is(r.err, exchange.ErrIdle) {
			t.Fatalf("Read() error = %v, want ErrIdle", r.err)
		}
		if n := clk.Pending(); n != 0 {
			t.Errorf("%d timers still armed after Read returned", n)
		}
	})
}

func TestIdleTimeoutReportsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clk := clocktest.NewFake(time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC))
		conn := exchange.WithIdleTimeout(exchangetest.NewConn(), clk, time.Second)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := conn.Read(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Read() error = %v, want context.Canceled", err)
		}
	})
}
