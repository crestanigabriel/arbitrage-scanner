// Package clocktest provides a manually advanced clock for tests.
package clocktest

import (
	"slices"
	"sync"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
)

// Fake is a clock.Clock whose time moves only when Advance is called. Timers fire, in deadline
// order, when the time reaches their deadline. It is safe for concurrent use.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

var _ clock.Clock = (*Fake)(nil)

// NewFake returns a fake clock set to start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// NewTimer returns a timer that fires once the fake time reaches Now()+d.
func (f *Fake) NewTimer(d time.Duration) clock.Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{clock: f, ch: make(chan time.Time, 1)}
	f.timers = append(f.timers, t)
	f.arm(t, d)
	return t
}

// Advance moves the time forward by d and fires every timer that is due.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	f.fireDue()
}

// Pending returns how many timers are armed. Tests use it to see that a component is waiting.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.timers {
		if t.active {
			n++
		}
	}
	return n
}

// arm and fireDue need f.mu held.
func (f *Fake) arm(t *fakeTimer, d time.Duration) {
	// Like time.Timer since Go 1.23, a stopped or reset timer never delivers a stale value.
	select {
	case <-t.ch:
	default:
	}
	t.deadline = f.now.Add(d)
	t.active = true
	f.fireDue()
}

func (f *Fake) fireDue() {
	due := make([]*fakeTimer, 0, len(f.timers))
	for _, t := range f.timers {
		if t.active && !t.deadline.After(f.now) {
			due = append(due, t)
		}
	}
	slices.SortStableFunc(due, func(a, b *fakeTimer) int { return a.deadline.Compare(b.deadline) })
	for _, t := range due {
		t.active = false
		t.ch <- f.now // buffered and drained on arm, so this never blocks
	}
}

type fakeTimer struct {
	clock    *Fake
	ch       chan time.Time
	deadline time.Time
	active   bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.active
	t.active = false
	select {
	case <-t.ch:
	default:
	}
	return was
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.active
	t.clock.arm(t, d)
	return was
}
