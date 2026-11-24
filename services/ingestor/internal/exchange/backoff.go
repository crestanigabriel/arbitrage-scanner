package exchange

import "time"

// Backoff is a capped exponential backoff with jitter. Each delay is drawn from
// [d/2, d], where d = min(Max, Base × 2^attempt): the lower bound keeps reconnects from
// hammering the exchange, and the random half spreads out many clients that lost their
// connection at the same moment. It is not safe for concurrent use.
type Backoff struct {
	base, max time.Duration
	jitter    Jitter
	attempt   int
}

// Jitter returns a uniformly random duration in [0, n). n is always > 0.
type Jitter func(n time.Duration) time.Duration

// NewBackoff returns a backoff starting at base and capped at max.
func NewBackoff(base, maxDelay time.Duration, jitter Jitter) *Backoff {
	return &Backoff{base: base, max: maxDelay, jitter: jitter}
}

// Next returns the delay before the next attempt and counts the attempt.
func (b *Backoff) Next() time.Duration {
	d := b.max
	// Stop doubling once past the cap, so the shift cannot overflow.
	if b.attempt < 32 {
		if e := b.base << b.attempt; e > 0 && e < b.max {
			d = e
		}
	}
	b.attempt++
	half := d / 2
	return half + b.jitter(d-half+1)
}

// Reset starts the next delay from base again. Call it once a connection is healthy.
func (b *Backoff) Reset() { b.attempt = 0 }
