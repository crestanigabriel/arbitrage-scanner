package book_test

import (
	"fmt"
	"testing"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
)

// Benchmarks on a book the size of Coinbase's BTC-USD (about 11k bids and 25k asks on
// 2026-11-24). Kraken books are at most 1000 levels per side, so they are the easy case.

const (
	benchBids = 12_000
	benchAsks = 25_000
)

// price returns the price i cents away from 60000.00, below it for bids and above for asks.
func price(side book.Side, i int) decimal.Decimal {
	cents := 6_000_000 + i
	if side == book.Bid {
		cents = 6_000_000 - 1 - i
	}
	return decimal.MustParse(fmt.Sprintf("%d.%02d", cents/100, cents%100))
}

// snapshot returns benchBids and benchAsks levels one cent apart, best first, as Coinbase
// sends them.
func snapshot() (bids, asks []book.Level) {
	qty := decimal.MustParse("0.5")
	for i := range benchBids {
		bids = append(bids, book.Level{Price: price(book.Bid, i), Quantity: qty})
	}
	for i := range benchAsks {
		asks = append(asks, book.Level{Price: price(book.Ask, i), Quantity: qty})
	}
	return bids, asks
}

func fullBook() *book.Book {
	b := book.New()
	b.Load(snapshot())
	return b
}

// BenchmarkSnapshot loads a whole Coinbase-sized snapshot into a book.
func BenchmarkSnapshot(b *testing.B) {
	bids, asks := snapshot()
	bk := book.New()
	for b.Loop() {
		bk.Load(bids, asks)
	}
}

// BenchmarkApply changes levels at a given distance from the top of the book. near-top is
// the common case: most updates land within a few levels of the best price.
func BenchmarkApply(b *testing.B) {
	for _, tc := range []struct {
		name  string
		level int
	}{{"top", 0}, {"near-top", 5}, {"middle", benchAsks / 2}, {"bottom", benchAsks - 1}} {
		qty := decimal.MustParse("1.25")
		p := price(book.Ask, tc.level)
		b.Run("replace/"+tc.name, func(b *testing.B) {
			bk := fullBook()
			for b.Loop() {
				bk.Apply(book.Ask, p, qty) // the level exists: its quantity changes
			}
		})
		b.Run("remove+insert/"+tc.name, func(b *testing.B) {
			bk := fullBook()
			zero := decimal.MustParse("0")
			for b.Loop() {
				bk.Apply(book.Ask, p, zero) // removes the level
				bk.Apply(book.Ask, p, qty)  // and puts it back
			}
		})
	}
}

func BenchmarkTop(b *testing.B) {
	bk := fullBook()
	for b.Loop() {
		bk.Top(100)
	}
}
