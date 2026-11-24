// Package pipeline turns exchange events into published market data: books through the
// per-book throttle, trades straight through.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	marketdatav1 "github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/gen/arbscanner/marketdata/v1"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/publish"
)

// BookKey identifies one book.
type BookKey struct {
	Exchange string
	Symbol   string
}

// Throttle limits how often each book is published. *throttle.Throttle implements it.
type Throttle interface {
	Offer(k BookKey, v exchange.BookUpdate) (exchange.BookUpdate, bool)
	Due() []exchange.BookUpdate
	NextDeadline() (time.Time, bool)
	Drop(k BookKey)
}

// Kind is what a publish carried, a metrics label value.
type Kind string

// Publish kinds.
const (
	KindBook  Kind = "book"
	KindTrade Kind = "trade"
)

// Metrics records what the pipeline receives and publishes. *metrics.Metrics implements it.
type Metrics interface {
	// BookUpdated is called for every in-sync book update received, throttled or not.
	// bidLevels and askLevels are the sizes of the adapter's local book.
	BookUpdated(exchange, symbol string, bidLevels, askLevels int)
	// BookReset is called when a book goes out of sync.
	BookReset(exchange, symbol string, reason exchange.ResetReason)
	// TradeReceived is called for every trade received.
	TradeReceived(exchange, symbol string)
	// Published is called after every publish attempt, with how long it took and its error.
	Published(kind Kind, exchange, symbol string, took time.Duration, err error)
}

// Pipeline publishes the events of a MarketSource. Everything runs in Run's goroutine, so
// publishes happen in order and the throttle needs no locking.
type Pipeline struct {
	pub      publish.Publisher
	throttle Throttle
	metrics  Metrics
	clock    clock.Clock
	depth    uint32
	log      *slog.Logger
	sequence map[BookKey]uint64
}

// New returns a Pipeline. depth is the configured levels per side, published in each
// OrderBook as the depth the consumer can expect.
func New(pub publish.Publisher, th Throttle, m Metrics, clk clock.Clock, depth uint32, log *slog.Logger) *Pipeline {
	return &Pipeline{pub: pub, throttle: th, metrics: m, clock: clk, depth: depth, log: log, sequence: make(map[BookKey]uint64)}
}

// Run consumes events until ctx is done or in is closed, and returns nil then. A failed
// publish is logged and does not stop it: the next book replaces the lost one.
func (p *Pipeline) Run(ctx context.Context, in <-chan exchange.Event) error {
	var (
		timer    clock.Timer
		timerC   <-chan time.Time
		deadline time.Time // when timer fires, if timerC is not nil
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-in:
			if !ok {
				return nil
			}
			p.handle(ctx, ev)
		case <-timerC:
			timerC = nil
			for _, u := range p.throttle.Due() {
				p.publishBook(ctx, u)
			}
		}

		// Keep one timer armed for the earliest held book.
		next, ok := p.throttle.NextDeadline()
		switch {
		case !ok && timerC != nil:
			timer.Stop()
			timerC = nil
		case ok && (timerC == nil || !next.Equal(deadline)):
			wait := next.Sub(p.clock.Now())
			if timer == nil {
				timer = p.clock.NewTimer(wait)
			} else {
				timer.Reset(wait)
			}
			timerC, deadline = timer.C(), next
		}
	}
}

func (p *Pipeline) handle(ctx context.Context, ev exchange.Event) {
	switch ev := ev.(type) {
	case exchange.BookUpdate:
		p.metrics.BookUpdated(ev.Exchange, ev.Symbol, ev.BidLevels, ev.AskLevels)
		if u, now := p.throttle.Offer(BookKey{ev.Exchange, ev.Symbol}, ev); now {
			p.publishBook(ctx, u)
		}
	case exchange.BookReset:
		// The held book is from before the failure: never publish it.
		p.throttle.Drop(BookKey{ev.Exchange, ev.Symbol})
		p.metrics.BookReset(ev.Exchange, ev.Symbol, ev.Reason)
	case exchange.Trade:
		p.metrics.TradeReceived(ev.Exchange, ev.Symbol)
		p.publishTrade(ctx, ev)
	default:
		panic(fmt.Sprintf("pipeline: unknown event %T", ev))
	}
}

func (p *Pipeline) publishBook(ctx context.Context, u exchange.BookUpdate) {
	key := BookKey{u.Exchange, u.Symbol}
	p.sequence[key]++
	start := p.clock.Now()
	b := &marketdatav1.OrderBook{
		Exchange:    u.Exchange,
		Symbol:      u.Symbol,
		Sequence:    p.sequence[key],
		Depth:       p.depth,
		Bids:        levels(u.Bids),
		Asks:        levels(u.Asks),
		ExchangeTs:  timestamppb.New(u.ExchangeTime),
		ReceivedTs:  timestamppb.New(u.ReceivedTime),
		PublishedTs: timestamppb.New(start),
	}
	err := p.pub.PublishBook(ctx, b)
	if ctx.Err() != nil {
		return // shutting down: neither a failure nor a publish worth counting
	}
	p.metrics.Published(KindBook, u.Exchange, u.Symbol, p.clock.Now().Sub(start), err)
	if err != nil {
		p.log.WarnContext(ctx, "book not published", "exchange", u.Exchange, "symbol", u.Symbol, "error", err)
	}
}

func (p *Pipeline) publishTrade(ctx context.Context, tr exchange.Trade) {
	start := p.clock.Now()
	t := &marketdatav1.Trade{
		Exchange:    tr.Exchange,
		Symbol:      tr.Symbol,
		TradeId:     tr.TradeID,
		Price:       tr.Price.String(),
		Quantity:    tr.Quantity.String(),
		TakerSide:   side(tr.TakerSide),
		ExchangeTs:  timestamppb.New(tr.ExchangeTime),
		ReceivedTs:  timestamppb.New(tr.ReceivedTime),
		PublishedTs: timestamppb.New(start),
	}
	err := p.pub.PublishTrade(ctx, t)
	if ctx.Err() != nil {
		return
	}
	p.metrics.Published(KindTrade, tr.Exchange, tr.Symbol, p.clock.Now().Sub(start), err)
	if err != nil {
		p.log.WarnContext(ctx, "trade not published", "exchange", tr.Exchange, "symbol", tr.Symbol, "trade_id", tr.TradeID, "error", err)
	}
}

func levels(ls []book.Level) []*marketdatav1.PriceLevel {
	out := make([]*marketdatav1.PriceLevel, len(ls))
	for i, l := range ls {
		out[i] = &marketdatav1.PriceLevel{Price: l.Price.String(), Quantity: l.Quantity.String()}
	}
	return out
}

func side(s exchange.Side) marketdatav1.Side {
	switch s {
	case exchange.SideBuy:
		return marketdatav1.Side_SIDE_BUY
	case exchange.SideSell:
		return marketdatav1.Side_SIDE_SELL
	default:
		return marketdatav1.Side_SIDE_UNSPECIFIED
	}
}
