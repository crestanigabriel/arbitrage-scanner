// Package ws implements exchange.Dialer and exchange.Conn over WebSockets
// (github.com/coder/websocket). It is the only package that knows the WebSocket library.
package ws

import (
	"context"
	"fmt"

	"github.com/coder/websocket"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
)

// DefaultReadLimit is the largest message accepted when Dialer.ReadLimit is 0. The library
// default (32 KB) is smaller than a deep book snapshot.
const DefaultReadLimit = 1 << 20

// Dialer opens WebSocket connections.
type Dialer struct {
	// ReadLimit is the largest message accepted, in bytes; 0 means DefaultReadLimit. A Coinbase
	// book snapshot is one message of several MB.
	ReadLimit int64
}

var _ exchange.Dialer = Dialer{}

// Dial implements exchange.Dialer.
func (d Dialer) Dial(ctx context.Context, url string) (exchange.Conn, error) {
	c, resp, err := websocket.Dial(ctx, url, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("websocket dial %s: %w", url, err)
	}
	limit := d.ReadLimit
	if limit <= 0 {
		limit = DefaultReadLimit
	}
	c.SetReadLimit(limit)
	return &conn{c: c}, nil
}

type conn struct{ c *websocket.Conn }

// Read implements exchange.Conn. A done ctx closes the connection, as the library does.
func (c *conn) Read(ctx context.Context) ([]byte, error) {
	_, b, err := c.c.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("websocket read: %w", err)
	}
	return b, nil
}

// Write implements exchange.Conn. Messages are sent as text frames: every exchange we speak
// to uses JSON.
func (c *conn) Write(ctx context.Context, msg []byte) error {
	if err := c.c.Write(ctx, websocket.MessageText, msg); err != nil {
		return fmt.Errorf("websocket write: %w", err)
	}
	return nil
}

// Close implements exchange.Conn.
func (c *conn) Close() error {
	// CloseNow: the peer is a public feed with nothing to flush, and a close handshake with a
	// connection that already failed would only wait for its timeout.
	return c.c.CloseNow()
}
