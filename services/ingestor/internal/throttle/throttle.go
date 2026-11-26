// Package throttle limits how often the latest value per key is published, with a leading and
// a trailing edge.
//
// The first value after a quiet interval is published at once (leading edge). Values offered
// inside the interval replace each other, and the last one is published when the interval ends
// (trailing edge). A key is therefore published at most once per interval and is never more
// than one interval out of date.
//
// An optional keepalive publishes the last value of a key again when nothing was published for
// it for that long, so a consumer can tell a quiet key from a lost one. Drop stops it until the
// next value is offered.
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
	clock     clock.Clock
	interval  time.Duration
	keepalive time.Duration
	keys      map[K]*entry[V]
}

type entry[V any] struct {
	published  bool      // at least one value was published
	last       time.Time // when the last value was published
	pending    V
	hasPending bool
	latest     V    // the last value published, which the keepalive publishes again
	live       bool // latest is still valid: it was not dropped
}

// New returns a throttle with the given interval and keepalive. An interval of 0 publishes
// every value. A keepalive of 0 never publishes a value twice. A keepalive shorter than the
// interval is raised to it, so a key is still published at most once per interval.
func New[K comparable, V any](c clock.Clock, interval, keepalive time.Duration) *Throttle[K, V] {
	if keepalive > 0 && keepalive < interval {
		keepalive = interval
	}
	return &Throttle[K, V]{clock: c, interval: interval, keepalive: keepalive, keys: make(map[K]*entry[V])}
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
		t.markPublished(e, now, v)
		return v, true
	}
	e.pending, e.hasPending = v, true
	var zero V
	return zero, false
}

// Due returns the values to publish now, oldest deadline first, and counts them as published
// now: the held values whose interval has ended, and the last values of keys whose keepalive
// has ended.
func (t *Throttle[K, V]) Due() []V {
	now := t.clock.Now()
	type due struct {
		deadline time.Time
		v        V
	}
	var ready []due
	for _, e := range t.keys {
		d, ok := t.deadline(e)
		if !ok || d.After(now) {
			continue
		}
		v := e.latest
		if e.hasPending {
			v = e.pending
		}
		ready = append(ready, due{d, v})
		t.markPublished(e, now, v)
	}
	slices.SortStableFunc(ready, func(a, b due) int { return a.deadline.Compare(b.deadline) })
	out := make([]V, len(ready))
	for i, r := range ready {
		out[i] = r.v
	}
	return out
}

// NextDeadline returns when Due next has a value to return. ok is false when no value is held
// and no keepalive is running.
func (t *Throttle[K, V]) NextDeadline() (deadline time.Time, ok bool) {
	for _, e := range t.keys {
		if d, has := t.deadline(e); has && (!ok || d.Before(deadline)) {
			deadline, ok = d, true
		}
	}
	return deadline, ok
}

// Drop discards the value held for k, if any, and stops its keepalive until the next Offer.
// Used when the value is no longer valid, such as a book that went out of sync.
func (t *Throttle[K, V]) Drop(k K) {
	if e, ok := t.keys[k]; ok {
		var zero V
		e.pending, e.hasPending = zero, false
		e.latest, e.live = zero, false
	}
}

// deadline returns when e is next due: the end of its interval when a value is held, otherwise
// the end of its keepalive while one runs.
func (t *Throttle[K, V]) deadline(e *entry[V]) (time.Time, bool) {
	switch {
	case e.hasPending:
		return e.last.Add(t.interval), true
	case e.live && t.keepalive > 0:
		return e.last.Add(t.keepalive), true
	default:
		return time.Time{}, false
	}
}

func (t *Throttle[K, V]) markPublished(e *entry[V], now time.Time, v V) {
	var zero V
	e.published, e.last = true, now
	e.pending, e.hasPending = zero, false
	e.latest, e.live = v, true
}
