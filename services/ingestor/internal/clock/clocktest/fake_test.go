package clocktest_test

import (
	"testing"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock/clocktest"
)

var start = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func fired(ch <-chan time.Time) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestFakeTimer(t *testing.T) {
	c := clocktest.NewFake(start)
	timer := c.NewTimer(50 * time.Millisecond)

	c.Advance(49 * time.Millisecond)
	if fired(timer.C()) {
		t.Fatal("timer fired before its deadline")
	}
	c.Advance(time.Millisecond)
	if !fired(timer.C()) {
		t.Fatal("timer did not fire at its deadline")
	}
	if got, want := c.Now(), start.Add(50*time.Millisecond); !got.Equal(want) {
		t.Errorf("Now() = %v, want %v", got, want)
	}
	if c.Pending() != 0 {
		t.Errorf("Pending() = %d after firing, want 0", c.Pending())
	}
}

func TestFakeTimerStopAndReset(t *testing.T) {
	c := clocktest.NewFake(start)
	timer := c.NewTimer(10 * time.Millisecond)

	if !timer.Stop() {
		t.Error("Stop() = false on an active timer")
	}
	c.Advance(time.Second)
	if fired(timer.C()) {
		t.Fatal("stopped timer fired")
	}

	timer.Reset(10 * time.Millisecond)
	c.Advance(10 * time.Millisecond) // fires, value left unread
	timer.Reset(10 * time.Millisecond)
	if fired(timer.C()) {
		t.Fatal("Reset left a stale value on the channel")
	}
	c.Advance(10 * time.Millisecond)
	if !fired(timer.C()) {
		t.Fatal("reset timer did not fire")
	}
}

func TestFakeTimerZeroDuration(t *testing.T) {
	c := clocktest.NewFake(start)
	if !fired(c.NewTimer(0).C()) {
		t.Fatal("a zero-duration timer must fire at once")
	}
}
