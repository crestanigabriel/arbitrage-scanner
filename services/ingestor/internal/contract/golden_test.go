package contract_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	marketdatav1 "github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/gen/arbscanner/marketdata/v1"
)

var update = flag.Bool("update", false, "rewrite the golden files in fixtures/contracts")

// goldenDir is fixtures/contracts at the repo root, relative to this package.
var goldenDir = filepath.Join("..", "..", "..", "..", "fixtures", "contracts")

func ts(s string) *timestamppb.Timestamp {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return timestamppb.New(t)
}

func level(price, quantity string) *marketdatav1.PriceLevel {
	return &marketdatav1.PriceLevel{Price: price, Quantity: quantity}
}

// The values below are asserted field by field in
// services/detector/tests/unit/test_contracts.py. Change both together.

func orderBook() *marketdatav1.OrderBook {
	return &marketdatav1.OrderBook{
		Exchange: "kraken",
		Symbol:   "BTC-USD",
		Sequence: 42,
		Depth:    50,
		Bids:     []*marketdatav1.PriceLevel{level("67123.40", "0.5"), level("67123.3", "1.25")},
		Asks: []*marketdatav1.PriceLevel{
			level("67123.5", "0.1"), level("67124", "0.2"), level("67124.1", "0.00012"),
		},
		ExchangeTs:  ts("2026-11-23T12:00:00.123456Z"),
		ReceivedTs:  ts("2026-11-23T12:00:00.125Z"),
		PublishedTs: ts("2026-11-23T12:00:00.126Z"),
	}
}

func trade() *marketdatav1.Trade {
	return &marketdatav1.Trade{
		Exchange:    "coinbase",
		Symbol:      "BTC-USD",
		TradeId:     "912345678",
		Price:       "67150.01",
		Quantity:    "0.00012",
		TakerSide:   marketdatav1.Side_SIDE_SELL,
		ExchangeTs:  ts("2026-11-23T12:00:01.5Z"),
		ReceivedTs:  ts("2026-11-23T12:00:01.75Z"),
		PublishedTs: ts("2026-11-23T12:00:01.7505Z"),
	}
}

func TestGolden(t *testing.T) {
	tests := []struct {
		file string
		want proto.Message
		got  proto.Message // empty message of the same type, decoded into
	}{
		{"order_book.binpb", orderBook(), &marketdatav1.OrderBook{}},
		{"trade.binpb", trade(), &marketdatav1.Trade{}},
	}

	// Golden files are only read and written through this root, so no path can escape it.
	root, err := os.OpenRoot(goldenDir)
	if err != nil {
		t.Fatalf("open %s: %v", goldenDir, err)
	}
	t.Cleanup(func() { _ = root.Close() })

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			if *update {
				b, err := proto.MarshalOptions{Deterministic: true}.Marshal(tt.want)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if err := root.WriteFile(tt.file, b, 0o600); err != nil {
					t.Fatalf("write golden file: %v", err)
				}
			}

			b, err := root.ReadFile(tt.file)
			if err != nil {
				t.Fatalf("read golden file (run with -update to create it): %v", err)
			}
			if err := proto.Unmarshal(b, tt.got); err != nil {
				t.Fatalf("unmarshal %s: %v", tt.file, err)
			}
			if !proto.Equal(tt.got, tt.want) {
				t.Errorf("%s does not match the reference message\ngot:\n%s\nwant:\n%s",
					tt.file, prototext.Format(tt.got), prototext.Format(tt.want))
			}
		})
	}
}
