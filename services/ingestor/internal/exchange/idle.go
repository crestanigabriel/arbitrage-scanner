package exchange

import (
	"context"
	"errors"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
)

// ErrIdle is returned by a Conn from WithIdleTimeout when no message arrives in time.
var ErrIdle = errors.New("no message within the idle timeout")

// WithIdleTimeout wraps conn so that a Read that waits longer than timeout fails with ErrIdle.
// Exchanges send heartbeats while subscribed, so a silent connection is a dead one even when
// TCP has not noticed yet. Timing uses clk, so tests drive it with a fake clock.
func WithIdleTimeout(conn Conn, clk clock.Clock, timeout time.Duration) Conn {
	return &idleConn{Conn: conn, clock: clk, timeout: timeout}
}

type idleConn struct {
	Conn
	clock   clock.Clock
	timeout time.Duration
}

// Read implements Conn. Like a done ctx, an idle timeout ends the connection: the inner Read
// is cancelled.
func (c *idleConn) Read(ctx context.Context) ([]byte, error) {
	rctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	t := c.clock.NewTimer(c.timeout)
	defer t.Stop()
	go func() {
		select {
		case <-t.C():
			cancel(ErrIdle)
		case <-rctx.Done():
		}
	}()
	msg, err := c.Conn.Read(rctx)
	if err != nil && ctx.Err() == nil && errors.Is(context.Cause(rctx), ErrIdle) {
		return nil, ErrIdle
	}
	return msg, err
}
