// Package exchange defines what an exchange adapter provides and the transport it runs on.
//
// An adapter (internal/exchange/<name>) implements MarketSource. It owns the local book of each
// symbol, verifies it with the exchange's own rules, and emits normalized Events. Everything
// exchange-specific (wire format, symbol names, checksums, sequence numbers) stays inside it.
package exchange

import (
	"context"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
)

// MarketSource streams normalized market data from one exchange.
type MarketSource interface {
	// Run sends Events to out until ctx is done or a permanent error occurs. Transient failures
	// (dropped connections, failed checksums) are handled inside Run. It returns nil when ctx is
	// done. It never closes out.
	Run(ctx context.Context, out chan<- Event) error
}

// Event is one of BookUpdate, BookReset or Trade. The interface is sealed: only this package
// declares Event types, so consumers can switch over them exhaustively.
type Event interface{ event() }

// BookUpdate is the verified, in-sync top of one book after an exchange update.
type BookUpdate struct {
	Exchange string // lowercase exchange id, e.g. "kraken"
	Symbol   string // canonical BASE-QUOTE, e.g. "BTC-USD"
	// Bids and Asks are best first, at most the configured depth each. The adapter hands over
	// ownership: they are copies, never shared with its book.
	Bids, Asks []book.Level
	// BidLevels and AskLevels are the sizes of the adapter's local book, which can be much
	// deeper than what is published (Coinbase sends its whole book).
	BidLevels, AskLevels int
	ExchangeTime         time.Time // exchange time of the latest update included
	ReceivedTime         time.Time // when the adapter received that update
}

// BookReset reports that a book is out of sync and must not be published until the next
// BookUpdate for it.
type BookReset struct {
	Exchange string
	Symbol   string
	Reason   ResetReason
}

// ResetReason says why a book went out of sync.
type ResetReason string

// Reasons for a BookReset. They are label values for metrics, so the set stays small.
const (
	ResetChecksum  ResetReason = "checksum"  // the local book failed the exchange checksum
	ResetSequence  ResetReason = "sequence"  // a gap in the exchange sequence numbers
	ResetProtocol  ResetReason = "protocol"  // a message could not be applied
	ResetReconnect ResetReason = "reconnect" // the connection was lost
)

// Side is the side of an order.
type Side int

// Order sides.
const (
	SideBuy Side = iota + 1
	SideSell
)

// Trade is one execution, normalized to the taker's side.
type Trade struct {
	Exchange     string
	Symbol       string
	TradeID      string
	Price        decimal.Decimal
	Quantity     decimal.Decimal
	TakerSide    Side
	ExchangeTime time.Time
	ReceivedTime time.Time
}

func (BookUpdate) event() {}
func (BookReset) event()  {}
func (Trade) event()      {}

// Conn is a message-oriented connection, such as a WebSocket.
type Conn interface {
	// Read blocks for the next message. It returns an error once ctx is done or the
	// connection fails; the connection cannot be used after that.
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close() error
}

// Dialer opens Conns.
type Dialer interface {
	Dial(ctx context.Context, url string) (Conn, error)
}

// Send delivers e on out, or gives up when ctx is done. It reports whether e was sent.
func Send(ctx context.Context, out chan<- Event, e Event) bool {
	select {
	case out <- e:
		return true
	case <-ctx.Done():
		return false
	}
}
