// Package clock abstracts time so that components that wait or timestamp can be tested with a
// fake clock. Only the composition root constructs Real.
package clock

import "time"

// Clock tells the time and creates timers.
type Clock interface {
	Now() time.Time
	// NewTimer returns a timer that sends the time on its channel once d has elapsed.
	NewTimer(d time.Duration) Timer
}

// Timer is a one-shot timer, with the semantics of time.Timer since Go 1.23: after Stop or
// Reset returns, no stale value is received from C.
type Timer interface {
	C() <-chan time.Time
	// Stop prevents the timer from firing. It reports whether the timer was active.
	Stop() bool
	// Reset makes the timer fire d from now. It reports whether the timer was active.
	Reset(d time.Duration) bool
}

// Real is the system clock.
type Real struct{}

var _ Clock = Real{}

// Now returns time.Now().
func (Real) Now() time.Time { return time.Now() }

// NewTimer wraps time.NewTimer.
func (Real) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }
