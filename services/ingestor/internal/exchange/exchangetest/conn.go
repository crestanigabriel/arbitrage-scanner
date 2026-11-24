// Package exchangetest provides fakes of the exchange transport for adapter tests.
package exchangetest

import (
	"context"
	"errors"
	"sync"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
)

// ErrClosed is returned by a Conn used after Close.
var ErrClosed = errors.New("exchangetest: connection closed")

// Conn is a scripted exchange.Conn. The test pushes frames for Read with Push, makes Read fail
// with Fail, and inspects what the code under test wrote with Written. Its channel operations
// block durably, so it works inside testing/synctest bubbles.
type Conn struct {
	frames chan []byte
	errs   chan error
	closed chan struct{}
	once   sync.Once

	mu      sync.Mutex
	written [][]byte
}

var _ exchange.Conn = (*Conn)(nil)

// NewConn returns an open Conn with nothing to read yet.
func NewConn() *Conn {
	return &Conn{
		frames: make(chan []byte, 4096),
		errs:   make(chan error, 1),
		closed: make(chan struct{}),
	}
}

// Push queues frames for Read, in order.
func (c *Conn) Push(frames ...string) {
	for _, f := range frames {
		c.frames <- []byte(f)
	}
}

// Fail makes the next Read that finds no queued frame return err.
func (c *Conn) Fail(err error) { c.errs <- err }

// Written returns the messages written so far, as strings.
func (c *Conn) Written() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.written))
	for i, w := range c.written {
		out[i] = string(w)
	}
	return out
}

// IsClosed reports whether Close was called.
func (c *Conn) IsClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// Read implements exchange.Conn. Queued frames are delivered before a Fail error.
func (c *Conn) Read(ctx context.Context) ([]byte, error) {
	select {
	case f := <-c.frames:
		return f, nil
	default:
	}
	select {
	case f := <-c.frames:
		return f, nil
	case err := <-c.errs:
		return nil, err
	case <-c.closed:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Write implements exchange.Conn.
func (c *Conn) Write(_ context.Context, msg []byte) error {
	if c.IsClosed() {
		return ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, append([]byte(nil), msg...))
	return nil
}

// Close implements exchange.Conn.
func (c *Conn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// Dialer is a scripted exchange.Dialer: each Dial takes the next result from Results. A Dial
// with no result queued blocks until ctx is done.
type Dialer struct {
	Results chan DialResult

	mu   sync.Mutex
	urls []string
}

// DialResult is what one Dial returns.
type DialResult struct {
	Conn exchange.Conn
	Err  error
}

var _ exchange.Dialer = (*Dialer)(nil)

// NewDialer returns a Dialer with room for queued results.
func NewDialer() *Dialer { return &Dialer{Results: make(chan DialResult, 16)} }

// Dial implements exchange.Dialer.
func (d *Dialer) Dial(ctx context.Context, url string) (exchange.Conn, error) {
	d.mu.Lock()
	d.urls = append(d.urls, url)
	d.mu.Unlock()
	select {
	case r := <-d.Results:
		return r.Conn, r.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Dials returns how many times Dial was called.
func (d *Dialer) Dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.urls)
}
