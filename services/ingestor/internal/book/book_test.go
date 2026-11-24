package book_test

import (
	"slices"
	"testing"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
)

type op struct {
	side     book.Side
	price    string
	quantity string
}

// render shows a side as "price:quantity" strings, best first.
func render(levels []book.Level) []string {
	out := make([]string, len(levels))
	for i, l := range levels {
		out[i] = l.Price.String() + ":" + l.Quantity.String()
	}
	return out
}

func apply(b *book.Book, ops []op) {
	for _, o := range ops {
		b.Apply(o.side, decimal.MustParse(o.price), decimal.MustParse(o.quantity))
	}
}

func TestApply(t *testing.T) {
	tests := []struct {
		name     string
		ops      []op
		wantBids []string
		wantAsks []string
	}{
		{
			name:     "empty",
			wantBids: []string{},
			wantAsks: []string{},
		},
		{
			name: "inserts are sorted best first",
			ops: []op{
				{book.Bid, "100", "1"},
				{book.Bid, "102", "2"},
				{book.Bid, "101", "3"},
				{book.Ask, "105", "1"},
				{book.Ask, "103", "2"},
				{book.Ask, "104", "3"},
			},
			wantBids: []string{"102:2", "101:3", "100:1"},
			wantAsks: []string{"103:2", "104:3", "105:1"},
		},
		{
			name: "an existing level is replaced, not added to",
			ops: []op{
				{book.Bid, "100", "1"},
				{book.Bid, "100", "0.5"},
				{book.Ask, "101", "1"},
				{book.Ask, "101", "2"},
			},
			wantBids: []string{"100:0.5"},
			wantAsks: []string{"101:2"},
		},
		{
			name: "a price matches across scales and keeps its first text",
			ops: []op{
				{book.Bid, "100.0", "1"}, {book.Bid, "100", "2"},
			},
			wantBids: []string{"100.0:2"},
			wantAsks: []string{},
		},
		{
			name: "zero quantity removes the level",
			ops: []op{
				{book.Bid, "100", "1"},
				{book.Bid, "101", "1"},
				{book.Bid, "100", "0.00000000"},
				{book.Ask, "102", "1"},
				{book.Ask, "102", "0"},
			},
			wantBids: []string{"101:1"},
			wantAsks: []string{},
		},
		{
			name: "removing a missing price does nothing",
			ops: []op{
				{book.Bid, "100", "1"}, {book.Bid, "99", "0"}, {book.Ask, "101", "0"},
			},
			wantBids: []string{"100:1"},
			wantAsks: []string{},
		},
		{
			name: "decimal ordering, not text ordering",
			ops: []op{
				{book.Ask, "9.5", "1"}, {book.Ask, "10", "1"}, {book.Ask, "9.45", "1"},
			},
			wantBids: []string{},
			wantAsks: []string{"9.45:1", "9.5:1", "10:1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := book.New()
			apply(b, tt.ops)
			bids, asks := b.Top(100)
			if got := render(bids); !slices.Equal(got, tt.wantBids) {
				t.Errorf("bids = %v, want %v", got, tt.wantBids)
			}
			if got := render(asks); !slices.Equal(got, tt.wantAsks) {
				t.Errorf("asks = %v, want %v", got, tt.wantAsks)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	b := book.New()
	apply(b, []op{
		{book.Bid, "100", "1"},
		{book.Bid, "99", "1"},
		{book.Bid, "98", "1"},
		{book.Ask, "101", "1"},
		{book.Ask, "102", "1"},
	})

	// A better bid pushes the worst one out of a depth-3 book. The exchange sends no removal
	// for it, so truncation is what drops it.
	apply(b, []op{{book.Bid, "100.5", "1"}})
	b.Truncate(3)

	bids, asks := b.Top(10)
	if got, want := render(bids), []string{"100.5:1", "100:1", "99:1"}; !slices.Equal(got, want) {
		t.Errorf("bids = %v, want %v", got, want)
	}
	if got, want := render(asks), []string{"101:1", "102:1"}; !slices.Equal(got, want) {
		t.Errorf("asks = %v, want %v (thinner than depth, untouched)", got, want)
	}

	// The dropped level must stay gone: if it comes back into range, the exchange sends it.
	apply(b, []op{{book.Bid, "100.5", "0"}})
	if got, want := b.Len(book.Bid), 2; got != want {
		t.Errorf("Len(Bid) = %d, want %d", got, want)
	}
}

func TestTop(t *testing.T) {
	b := book.New()
	apply(b, []op{
		{book.Bid, "100", "1"},
		{book.Bid, "99", "1"},
		{book.Bid, "98", "1"},
		{book.Ask, "101", "1"},
	})

	bids, asks := b.Top(2)
	if got, want := render(bids), []string{"100:1", "99:1"}; !slices.Equal(got, want) {
		t.Errorf("Top(2) bids = %v, want %v", got, want)
	}
	if got, want := render(asks), []string{"101:1"}; !slices.Equal(got, want) {
		t.Errorf("Top(2) asks = %v, want %v", got, want)
	}

	// Top returns copies: later changes to the book do not show through.
	apply(b, []op{{book.Bid, "100", "5"}, {book.Bid, "99", "0"}})
	if got, want := render(bids), []string{"100:1", "99:1"}; !slices.Equal(got, want) {
		t.Errorf("earlier Top result changed to %v, want %v", got, want)
	}
}

func TestReset(t *testing.T) {
	b := book.New()
	apply(b, []op{{book.Bid, "100", "1"}, {book.Ask, "101", "1"}})
	b.Reset()
	if b.Len(book.Bid) != 0 || b.Len(book.Ask) != 0 {
		t.Errorf("after Reset, Len = %d bids / %d asks, want 0 / 0", b.Len(book.Bid), b.Len(book.Ask))
	}
}
