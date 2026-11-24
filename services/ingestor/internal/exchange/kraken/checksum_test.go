package kraken

import (
	"fmt"
	"testing"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
)

func TestAppendDigits(t *testing.T) {
	tests := map[string]string{
		"45285.2":    "452852",
		"0.00100000": "100000",
		"59979.0":    "599790",
		"100":        "100",
		"0.5":        "5",
		"1.05":       "105",
	}
	for in, want := range tests {
		if got := string(appendDigits(nil, in)); got != want {
			t.Errorf("appendDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChecksumOrderAndDepth(t *testing.T) {
	b := book.New()
	set := func(side book.Side, price, qty string) {
		b.Apply(side, decimal.MustParse(price), decimal.MustParse(qty))
	}
	// 12 levels per side: only the best 10 count.
	for i := range 12 {
		set(book.Ask, fmt.Sprintf("101.%02d", i), "1.0")           // 101.00 … 101.11
		set(book.Bid, fmt.Sprintf("%d.%d", 90+i/10, i%10), "0.50") // 90.0 … 91.1
	}
	asks, bids := topStrings(b)
	if got, want := checksum(b), crc(asks[:10], bids[:10]); got != want {
		t.Errorf("checksum = %d, want %d (asks low→high, then bids high→low, top 10 each)", got, want)
	}

	// Changing a level beyond the top 10 does not change the checksum; a top level does.
	before := checksum(b)
	set(book.Bid, "90.0", "7") // the 11th best bid is 90.1, so 90.0 is below the top 10
	if checksum(b) != before {
		t.Error("a level outside the top 10 changed the checksum")
	}
	set(book.Bid, "91.1", "0.51")
	if checksum(b) == before {
		t.Error("a quantity change in the top 10 did not change the checksum")
	}
}

func topStrings(b *book.Book) (asks, bids [][2]string) {
	bl, al := b.Top(100)
	for _, l := range al {
		asks = append(asks, [2]string{l.Price.String(), l.Quantity.String()})
	}
	for _, l := range bl {
		bids = append(bids, [2]string{l.Price.String(), l.Quantity.String()})
	}
	return asks, bids
}
