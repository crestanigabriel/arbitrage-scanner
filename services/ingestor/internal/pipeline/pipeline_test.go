package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock/clocktest"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	marketdatav1 "github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/gen/arbscanner/marketdata/v1"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/pipeline"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/throttle"
)

const interval = 50 * time.Millisecond

var t0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

type fakePublisher struct {
	mu     sync.Mutex
	books  []*marketdatav1.OrderBook
	trades []*marketdatav1.Trade
	err    error
}

func (f *fakePublisher) PublishBook(_ context.Context, b *marketdatav1.OrderBook) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.books = append(f.books, b)
	return f.err
}

func (f *fakePublisher) PublishTrade(_ context.Context, t *marketdatav1.Trade) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trades = append(f.trades, t)
	return f.err
}

// published returns the books published so far as "symbol#sequence:best bid quantity".
func (f *fakePublisher) published() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.books))
	for i, b := range f.books {
		out[i] = fmt.Sprintf("%s#%d:%s", b.GetSymbol(), b.GetSequence(), b.GetBids()[0].GetQuantity())
	}
	return out
}

// fakeMetrics records every call as a line of text.
type fakeMetrics struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeMetrics) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeMetrics) BookUpdated(ex, sym string, bids, asks int) {
	f.record("update %s %s %d/%d", ex, sym, bids, asks)
}

func (f *fakeMetrics) BookReset(ex, sym string, reason exchange.ResetReason) {
	f.record("reset %s %s %s", ex, sym, reason)
}

func (f *fakeMetrics) TradeReceived(ex, sym string) { f.record("trade %s %s", ex, sym) }

func (f *fakeMetrics) Published(kind pipeline.Kind, ex, sym string, _ time.Duration, err error) {
	f.record("published %s %s %s %v", kind, ex, sym, err)
}

func (f *fakeMetrics) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type harness struct {
	clock   *clocktest.Fake
	pub     *fakePublisher
	metrics *fakeMetrics
	in      chan exchange.Event
	done    chan error
}

func runPipeline(ctx context.Context, t *testing.T) *harness {
	t.Helper()
	h := &harness{
		clock:   clocktest.NewFake(t0),
		pub:     &fakePublisher{},
		metrics: &fakeMetrics{},
		in:      make(chan exchange.Event),
		done:    make(chan error, 1),
	}
	th := throttle.New[pipeline.BookKey, exchange.BookUpdate](h.clock, interval)
	p := pipeline.New(h.pub, th, h.metrics, h.clock, 50, slog.New(slog.DiscardHandler))
	go func() { h.done <- p.Run(ctx, h.in) }()
	return h
}

func (h *harness) send(evs ...exchange.Event) {
	for _, e := range evs {
		h.in <- e
	}
	synctest.Wait()
}

func (h *harness) advance(d time.Duration) {
	h.clock.Advance(d)
	synctest.Wait()
}

func lvl(price, qty string) book.Level {
	return book.Level{Price: decimal.MustParse(price), Quantity: decimal.MustParse(qty)}
}

// update is a BTC-USD (or other symbol) book whose best bid quantity tags it.
func update(symbol, tag string) exchange.BookUpdate {
	return exchange.BookUpdate{
		Exchange:     "kraken",
		Symbol:       symbol,
		Bids:         []book.Level{lvl("100.0", tag), lvl("99.5", "2")},
		Asks:         []book.Level{lvl("100.5", "0.50000000")},
		BidLevels:    2,
		AskLevels:    1,
		ExchangeTime: t0.Add(-2 * time.Millisecond),
		ReceivedTime: t0.Add(-time.Millisecond),
	}
}

func TestBookConversion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runPipeline(t.Context(), t)
		h.send(update("BTC-USD", "1.00000000"))

		want := &marketdatav1.OrderBook{
			Exchange: "kraken",
			Symbol:   "BTC-USD",
			Sequence: 1,
			Depth:    50,
			Bids: []*marketdatav1.PriceLevel{
				{Price: "100.0", Quantity: "1.00000000"}, {Price: "99.5", Quantity: "2"},
			},
			Asks:        []*marketdatav1.PriceLevel{{Price: "100.5", Quantity: "0.50000000"}},
			ExchangeTs:  timestamppb.New(t0.Add(-2 * time.Millisecond)),
			ReceivedTs:  timestamppb.New(t0.Add(-time.Millisecond)),
			PublishedTs: timestamppb.New(t0),
		}
		if len(h.pub.books) != 1 {
			t.Fatalf("published %d books, want 1", len(h.pub.books))
		}
		if diff := cmp.Diff(want, h.pub.books[0], protocmp.Transform()); diff != "" {
			t.Errorf("OrderBook mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestBooksAreThrottled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runPipeline(t.Context(), t)

		h.send(update("BTC-USD", "1")) // leading edge
		h.advance(10 * time.Millisecond)
		h.send(update("BTC-USD", "2"), update("BTC-USD", "3"))
		h.send(update("ETH-USD", "1")) // another book: its own leading edge
		h.advance(39 * time.Millisecond)
		if got := h.pub.published(); len(got) != 2 {
			t.Fatalf("published %v before the interval ended, want 2 leading edges", got)
		}
		h.advance(time.Millisecond) // 50ms: BTC's trailing edge, with the latest book only
		h.advance(time.Second)

		want := []string{"BTC-USD#1:1", "ETH-USD#1:1", "BTC-USD#2:3"}
		if got := h.pub.published(); !slices.Equal(got, want) {
			t.Errorf("published %v, want %v", got, want)
		}
		if h.clock.Pending() != 0 {
			t.Errorf("%d timers armed with nothing held", h.clock.Pending())
		}
	})
}

func TestBookResetDropsHeldBook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runPipeline(t.Context(), t)

		h.send(update("BTC-USD", "1"))
		h.send(update("BTC-USD", "2")) // held
		h.send(exchange.BookReset{Exchange: "kraken", Symbol: "BTC-USD", Reason: exchange.ResetChecksum})
		h.advance(time.Second)

		if got, want := h.pub.published(), []string{"BTC-USD#1:1"}; !slices.Equal(got, want) {
			t.Errorf("published %v, want %v: the held book went out of sync", got, want)
		}

		// After the resync, the next book is published and the sequence continues.
		h.send(update("BTC-USD", "3"))
		if got, want := h.pub.published(), []string{"BTC-USD#1:1", "BTC-USD#2:3"}; !slices.Equal(got, want) {
			t.Errorf("published %v, want %v", got, want)
		}
	})
}

func TestTradesPassThrough(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runPipeline(t.Context(), t)
		h.clock.Advance(time.Second)
		for _, side := range []exchange.Side{exchange.SideBuy, exchange.SideSell} {
			h.send(exchange.Trade{
				Exchange:     "kraken",
				Symbol:       "BTC-USD",
				TradeID:      "117760264",
				Price:        decimal.MustParse("59977.0"),
				Quantity:     decimal.MustParse("0.08896249"),
				TakerSide:    side,
				ExchangeTime: t0.Add(-time.Second),
				ReceivedTime: t0,
			})
		}

		want := &marketdatav1.Trade{
			Exchange:    "kraken",
			Symbol:      "BTC-USD",
			TradeId:     "117760264",
			Price:       "59977.0",
			Quantity:    "0.08896249",
			TakerSide:   marketdatav1.Side_SIDE_BUY,
			ExchangeTs:  timestamppb.New(t0.Add(-time.Second)),
			ReceivedTs:  timestamppb.New(t0),
			PublishedTs: timestamppb.New(t0.Add(time.Second)),
		}
		if len(h.pub.trades) != 2 {
			t.Fatalf("published %d trades, want 2 (trades are not throttled)", len(h.pub.trades))
		}
		if diff := cmp.Diff(want, h.pub.trades[0], protocmp.Transform()); diff != "" {
			t.Errorf("Trade mismatch (-want +got):\n%s", diff)
		}
		if got := h.pub.trades[1].GetTakerSide(); got != marketdatav1.Side_SIDE_SELL {
			t.Errorf("second trade side = %v, want SIDE_SELL", got)
		}
	})
}

func TestPublishErrorDoesNotStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runPipeline(t.Context(), t)
		h.pub.err = errors.New("nats: timeout")
		h.send(update("BTC-USD", "1"))
		h.advance(interval)
		h.pub.err = nil
		h.send(update("BTC-USD", "2"))
		if got := len(h.pub.books); got != 2 {
			t.Errorf("publish attempts = %d, want 2", got)
		}
	})
}

func TestRunStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		h := runPipeline(ctx, t)
		h.send(update("BTC-USD", "1"), update("BTC-USD", "2")) // a timer is armed
		cancel()
		if err := <-h.done; err != nil {
			t.Errorf("Run() = %v after cancel, want nil", err)
		}
		synctest.Wait()
		if h.clock.Pending() != 0 {
			t.Error("timer left armed after Run returned")
		}

		h2 := runPipeline(t.Context(), t)
		close(h2.in)
		if err := <-h2.done; err != nil {
			t.Errorf("Run() = %v after the input closed, want nil", err)
		}
	})
}

func TestMetrics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runPipeline(t.Context(), t)
		h.send(update("BTC-USD", "1"), update("BTC-USD", "2")) // the second is held
		h.send(exchange.BookReset{Exchange: "kraken", Symbol: "BTC-USD", Reason: exchange.ResetChecksum})
		h.send(exchange.Trade{
			Exchange: "kraken", Symbol: "BTC-USD", TradeID: "1",
			Price: decimal.MustParse("1"), Quantity: decimal.MustParse("1"), TakerSide: exchange.SideBuy,
		})
		h.pub.err = errors.New("nats: timeout")
		h.advance(interval)
		h.send(update("ETH-USD", "1"))

		want := []string{
			"update kraken BTC-USD 2/1",
			"published book kraken BTC-USD <nil>",
			"update kraken BTC-USD 2/1", // counted although the throttle holds it
			"reset kraken BTC-USD checksum",
			"trade kraken BTC-USD",
			"published trade kraken BTC-USD <nil>",
			"update kraken ETH-USD 2/1",
			"published book kraken ETH-USD nats: timeout",
		}
		if got := h.metrics.recorded(); !slices.Equal(got, want) {
			t.Errorf("metrics calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
}
