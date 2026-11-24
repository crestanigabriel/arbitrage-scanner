package exchange_test

import (
	"testing"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
)

func noJitter(time.Duration) time.Duration { return 0 }

func maxJitter(n time.Duration) time.Duration { return n - 1 }

func TestBackoffBounds(t *testing.T) {
	const base, maxDelay = 500 * time.Millisecond, 30 * time.Second
	// The ceiling d doubles from base up to the cap; each delay lies in [d/2, d].
	ceilings := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	low := exchange.NewBackoff(base, maxDelay, noJitter)
	high := exchange.NewBackoff(base, maxDelay, maxJitter)
	for i, d := range ceilings {
		if got := low.Next(); got != d/2 {
			t.Errorf("attempt %d with no jitter: Next() = %v, want %v", i, got, d/2)
		}
		if got := high.Next(); got != d {
			t.Errorf("attempt %d with max jitter: Next() = %v, want %v", i, got, d)
		}
	}
}

func TestBackoffNoOverflow(t *testing.T) {
	b := exchange.NewBackoff(time.Second, time.Minute, maxJitter)
	for range 100 {
		if got := b.Next(); got <= 0 || got > time.Minute {
			t.Fatalf("Next() = %v, want in (0, 1m]", got)
		}
	}
}

func TestBackoffReset(t *testing.T) {
	b := exchange.NewBackoff(time.Second, time.Minute, maxJitter)
	b.Next()
	b.Next()
	b.Reset()
	if got := b.Next(); got != time.Second {
		t.Errorf("Next() after Reset = %v, want %v", got, time.Second)
	}
}
