// Package throttle limits how often the latest value per key is published, with a leading and
// a trailing edge.
//
// The first value after a quiet interval is published at once (leading edge). Values offered
// inside the interval replace each other, and the last one is published when the interval ends
// (trailing edge). A key is therefore published at most once per interval and is never more
// than one interval out of date.
package throttle

import (
	"slices"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
)

// Throttle is a state machine with no goroutines or timers of its own: its owner offers values,
// calls Due when NextDeadline is reached, and publishes what both return. It is not safe for
// concurrent use; one goroutine owns it, which keeps publishes in order.
type Throttle[K comparable, V any] struct {
	clock    clock.Clock
	interval time.Duration
	keys     map[K]*entry[V]
}

type entry[V any] struct {
	published  bool      // at least one value was published
	last       time.Time // when the last value was published
	pending    V
	hasPending bool
}

// New returns a throttle with the given interval. An interval of 0 publishes every value.
func New[K comparable, V any](c clock.Clock, interval time.Duration) *Throttle[K, V] {
	return &Throttle[K, V]{clock: c, interval: interval, keys: make(map[K]*entry[V])}
}

// Offer records v as the latest value for k. It returns v and true when v must be published
// now. Otherwise v is held, replacing any value held before it, until Due returns it.
func (t *Throttle[K, V]) Offer(k K, v V) (V, bool) {
	now := t.clock.Now()
	e, ok := t.keys[k]
	if !ok {
		e = &entry[V]{}
		t.keys[k] = e
	}
	if !e.published || now.Sub(e.last) >= t.interval {
		// Leading edge. A value still held from the previous interval is older than v, so it
		// is dropped rather than published after it.
		t.markPublished(e, now)
		return v, true
	}
	e.pending, e.hasPending = v, true
	var zero V
	return zero, false
}

// Due returns the held values whose interval has ended, oldest deadline first, and counts them
// as published now.
func (t *Throttle[K, V]) Due() []V {
	now := t.clock.Now()
	type due struct {
		deadline time.Time
		v        V
	}
	var ready []due
	for _, e := range t.keys {
		if d := e.last.Add(t.interval); e.hasPending && !d.After(now) {
			ready = append(ready, due{d, e.pending})
			t.markPublished(e, now)
		}
	}
	slices.SortStableFunc(ready, func(a, b due) int { return a.deadline.Compare(b.deadline) })
	out := make([]V, len(ready))
	for i, r := range ready {
		out[i] = r.v
	}
	return out
}

// NextDeadline returns when the earliest held value becomes due. ok is false when no value is
// held.
func (t *Throttle[K, V]) NextDeadline() (deadline time.Time, ok bool) {
	for _, e := range t.keys {
		if !e.hasPending {
			continue
		}
		if d := e.last.Add(t.interval); !ok || d.Before(deadline) {
			deadline, ok = d, true
		}
	}
	return deadline, ok
}

// Drop discards the value held for k, if any. Used when the value is no longer valid, such as
// a book that went out of sync.
func (t *Throttle[K, V]) Drop(k K) {
	if e, ok := t.keys[k]; ok {
		var zero V
		e.pending, e.hasPending = zero, false
	}
}

func (t *Throttle[K, V]) markPublished(e *entry[V], now time.Time) {
	var zero V
	e.published, e.last = true, now
	e.pending, e.hasPending = zero, false
}
