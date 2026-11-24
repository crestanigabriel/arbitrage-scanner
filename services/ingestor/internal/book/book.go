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

// Book holds both sides of one order book. It is not safe for concurrent use; each book is
// owned by one goroutine.
//
// Each side is a sorted slice stored worst first, so the best price is at the end. Nearly all
// updates land within a few levels of the best price, and at the end of a slice an insert or
// a removal moves almost nothing. Stored best first, each one would shift the whole side,
// which is tens of thousands of levels for a Coinbase book.
type Book struct {
	bids []Level // ascending price: best (highest) last
	asks []Level // descending price: best (lowest) last
}

// New returns an empty book.
func New() *Book { return &Book{} }

// Apply sets the quantity at price on side. A zero quantity removes the level; removing a
// price that is not in the book does nothing.
func (b *Book) Apply(side Side, price, quantity decimal.Decimal) {
	levels := b.side(side)
	i, found := slices.BinarySearchFunc(*levels, price, worstFirst(side))
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

// Load replaces the whole book with a snapshot, in any order. It is the same as Reset
// followed by Apply for every level, in order: zero quantities are skipped, and the last
// quantity given for a price wins. Sorting once makes it O(n log n) where Apply would be
// O(n²) for a large best-first snapshot.
func (b *Book) Load(bids, asks []Level) {
	b.bids = load(b.bids[:0], Bid, bids)
	b.asks = load(b.asks[:0], Ask, asks)
}

func load(dst []Level, side Side, levels []Level) []Level {
	dst = append(dst, levels...)
	cmp := worstFirst(side)
	slices.SortStableFunc(dst, func(x, y Level) int { return cmp(x, y.Price) })
	// Equal prices are adjacent and in input order: replay each run as Apply would. A zero
	// removes the level, the next non-zero inserts it with its own price text, and later ones
	// only change the quantity.
	out := dst[:0]
	for i := 0; i < len(dst); {
		var cur Level
		alive := false
		j := i
		for ; j < len(dst) && dst[j].Price.Cmp(dst[i].Price) == 0; j++ {
			switch l := dst[j]; {
			case l.Quantity.IsZero():
				alive = false
			case alive:
				cur.Quantity = l.Quantity
			default:
				cur, alive = l, true
			}
		}
		if alive {
			out = append(out, cur)
		}
		i = j
	}
	return out
}

// worstFirst orders a side's levels against a price, worst first.
func worstFirst(side Side) func(Level, decimal.Decimal) int {
	if side == Bid {
		return func(l Level, p decimal.Decimal) int { return l.Price.Cmp(p) } // ascending
	}
	return func(l Level, p decimal.Decimal) int { return p.Cmp(l.Price) } // descending
}

// Truncate keeps the best depth levels of each side and drops the rest.
func (b *Book) Truncate(depth int) {
	if n := len(b.bids) - depth; n > 0 {
		b.bids = slices.Delete(b.bids, 0, n)
	}
	if n := len(b.asks) - depth; n > 0 {
		b.asks = slices.Delete(b.asks, 0, n)
	}
}

// Top returns copies of the best n levels of each side, best first, or fewer when a side is
// thinner.
func (b *Book) Top(n int) (bids, asks []Level) {
	return top(b.bids, n), top(b.asks, n)
}

func top(levels []Level, n int) []Level {
	out := slices.Clone(levels[len(levels)-min(n, len(levels)):])
	slices.Reverse(out)
	return out
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
