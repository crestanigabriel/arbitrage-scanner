// Package book is a local, exchange-agnostic price-level order book.
//
// Exchanges send absolute quantities per price level: a level is replaced by the new quantity,
// and a zero quantity removes it. Exchange adapters apply their feed to a Book and verify it
// with their own rules (Kraken checksums, Coinbase sequence numbers).
package book

import (
	"slices"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
)

// Side is one side of the book.
type Side int

const (
	// Bid is the buy side, best (highest price) first.
	Bid Side = iota + 1
	// Ask is the sell side, best (lowest price) first.
	Ask
)

// Level is one price level: the total quantity resting at a price.
type Level struct {
	Price    decimal.Decimal
	Quantity decimal.Decimal
}

// Book holds both sides of one order book, each sorted best first. It is not safe for
// concurrent use; each book is owned by one goroutine.
type Book struct {
	bids []Level // descending price
	asks []Level // ascending price
}

// New returns an empty book.
func New() *Book { return &Book{} }

// Apply sets the quantity at price on side. A zero quantity removes the level; removing a
// price that is not in the book does nothing.
func (b *Book) Apply(side Side, price, quantity decimal.Decimal) {
	levels := b.side(side)
	i, found := slices.BinarySearchFunc(*levels, price, func(l Level, p decimal.Decimal) int {
		if side == Bid {
			return p.Cmp(l.Price) // descending
		}
		return l.Price.Cmp(p)
	})
	switch {
	case quantity.IsZero() && found:
		*levels = slices.Delete(*levels, i, i+1)
	case quantity.IsZero():
		// Nothing to remove.
	case found:
		(*levels)[i].Quantity = quantity
	default:
		*levels = slices.Insert(*levels, i, Level{Price: price, Quantity: quantity})
	}
}

// Truncate keeps the best depth levels of each side and drops the rest.
func (b *Book) Truncate(depth int) {
	if len(b.bids) > depth {
		b.bids = slices.Delete(b.bids, depth, len(b.bids))
	}
	if len(b.asks) > depth {
		b.asks = slices.Delete(b.asks, depth, len(b.asks))
	}
}

// Top returns copies of the best n levels of each side, or fewer when a side is thinner.
func (b *Book) Top(n int) (bids, asks []Level) {
	return slices.Clone(b.bids[:min(n, len(b.bids))]), slices.Clone(b.asks[:min(n, len(b.asks))])
}

// Len returns the number of levels on side.
func (b *Book) Len(side Side) int { return len(*b.side(side)) }

// Reset empties both sides.
func (b *Book) Reset() {
	b.bids = b.bids[:0]
	b.asks = b.asks[:0]
}

func (b *Book) side(side Side) *[]Level {
	if side == Bid {
		return &b.bids
	}
	return &b.asks
}
