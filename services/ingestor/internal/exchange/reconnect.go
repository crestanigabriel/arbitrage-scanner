package exchange

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
)

// Session runs on one open connection until it fails or ctx is done. It calls healthy once
// the connection is established at the protocol level (subscriptions acknowledged), which
// resets the reconnect backoff.
type Session func(ctx context.Context, conn Conn, healthy func()) error

// Connector keeps a session running across connection failures.
type Connector interface {
	// Run dials, runs session, and redials after it ends, until ctx is done (returning nil) or
	// session returns a Permanent error (returning it).
	Run(ctx context.Context, session Session) error
}

// PermanentError marks a failure that reconnecting cannot fix, such as a rejected subscription.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err so that a Connector stops instead of reconnecting.
func Permanent(err error) error { return &PermanentError{Err: err} }

// ReconnectObserver is told about every reconnect, for metrics.
type ReconnectObserver interface {
	// Reconnecting is called each time a connection is lost or a dial fails, before the
	// backoff wait.
	Reconnecting()
}

// Reconnector is the Connector used by the exchange adapters: it redials with a Backoff,
// waiting on the injected Clock.
type Reconnector struct {
	url      string
	dialer   Dialer
	clock    clock.Clock
	backoff  *Backoff
	observer ReconnectObserver
	log      *slog.Logger
}

var _ Connector = (*Reconnector)(nil)

// NewReconnector returns a Reconnector for url.
func NewReconnector(url string, d Dialer, c clock.Clock, b *Backoff, o ReconnectObserver, log *slog.Logger) *Reconnector {
	return &Reconnector{url: url, dialer: d, clock: c, backoff: b, observer: o, log: log}
}

// Run implements Connector.
func (r *Reconnector) Run(ctx context.Context, session Session) error {
	for {
		err := r.once(ctx, session)
		if ctx.Err() != nil {
			return nil
		}
		if perr, ok := errors.AsType[*PermanentError](err); ok {
			return perr
		}
		r.observer.Reconnecting()
		wait := r.backoff.Next()
		r.log.WarnContext(ctx, "connection lost, reconnecting", "url", r.url, "error", err, "wait", wait.String())
		timer := r.clock.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C():
		}
	}
}

func (r *Reconnector) once(ctx context.Context, session Session) error {
	conn, err := r.dialer.Dial(ctx, r.url)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	r.log.InfoContext(ctx, "connected", "url", r.url)
	err = session(ctx, conn, r.backoff.Reset)
	if cerr := conn.Close(); cerr != nil && err == nil {
		err = fmt.Errorf("close: %w", cerr)
	}
	if err == nil {
		err = errors.New("session ended")
	}
	return err
}
