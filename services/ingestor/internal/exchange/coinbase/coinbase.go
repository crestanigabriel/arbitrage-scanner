// Package coinbase is the exchange adapter for the Coinbase Advanced Trade WebSocket feed
// (wss://advanced-trade-ws.coinbase.com, no authentication).
//
// One connection carries the heartbeats, level2 and market_trades channels for every
// configured symbol. Coinbase has no depth option and no checksum. It sends the whole book
// as one snapshot message, then absolute updates, and numbers every message of the connection
// with sequence_num. The adapter keeps the whole book per symbol and emits its top. A gap in
// the sequence means a message was lost, and no one can tell which book it belonged to. So
// every book is reset and the connection is dropped. The reconnect brings fresh snapshots.
package coinbase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
)

// Exchange is the exchange id used in events and subjects.
const Exchange = "coinbase"

// maxDepth is the most levels per side the adapter emits (the OrderBook contract limit).
const maxDepth = 100

// Channels subscribed to, one subscribe message each.
const (
	chanHeartbeats = "heartbeats"
	chanLevel2     = "level2"
	chanTrades     = "market_trades"
)

// Config configures the adapter.
type Config struct {
	// Symbols are canonical BASE-QUOTE symbols, which are also Coinbase product ids.
	Symbols []string
	// Depth is how many levels per side each BookUpdate carries, 1 to 100.
	Depth int
	// IdleTimeout ends a connection that receives nothing for this long. Coinbase sends a
	// heartbeat every second.
	IdleTimeout time.Duration
}

// Source streams Coinbase books and trades. It implements exchange.MarketSource.
type Source struct {
	products    []string // in configuration order
	depth       int
	idleTimeout time.Duration
	connector   exchange.Connector
	clock       clock.Clock
	log         *slog.Logger
}

var _ exchange.MarketSource = (*Source)(nil)

// New returns a Source. connector supplies connections to Coinbase (and reconnects them), clk
// timestamps events and times the idle timeout.
func New(cfg Config, connector exchange.Connector, clk clock.Clock, log *slog.Logger) (*Source, error) {
	if cfg.Depth < 1 || cfg.Depth > maxDepth {
		return nil, fmt.Errorf("coinbase: depth %d out of range 1-%d", cfg.Depth, maxDepth)
	}
	if cfg.IdleTimeout <= 0 {
		return nil, fmt.Errorf("coinbase: idle timeout must be positive, got %v", cfg.IdleTimeout)
	}
	if len(cfg.Symbols) == 0 {
		return nil, errors.New("coinbase: no symbols")
	}
	s := &Source{
		depth:       cfg.Depth,
		idleTimeout: cfg.IdleTimeout,
		connector:   connector,
		clock:       clk,
		log:         log.With("exchange", Exchange),
	}
	for _, sym := range cfg.Symbols {
		if err := checkSymbol(sym); err != nil {
			return nil, err
		}
		if slices.Contains(s.products, sym) {
			return nil, fmt.Errorf("coinbase: duplicate symbol %q", sym)
		}
		s.products = append(s.products, sym)
	}
	return s, nil
}

// usdcBooks are the -USDC products with a book of their own. Every other -USDC product is an
// alias: Coinbase serves the -USD book under the -USD product id.
var usdcBooks = []string{"USDT-USDC", "EURC-USDC"}

// checkSymbol accepts a canonical symbol as a Coinbase product id.
func checkSymbol(canonical string) error {
	base, quote, ok := strings.Cut(canonical, "-")
	if !ok || base == "" || quote == "" || strings.Contains(quote, "-") {
		return fmt.Errorf("coinbase: symbol %q is not BASE-QUOTE", canonical)
	}
	if quote == "USDC" && !slices.Contains(usdcBooks, canonical) {
		return fmt.Errorf("coinbase: %s is an alias of %s-USD, which is the unified USD/USDC book: use %s-USD",
			canonical, base, base)
	}
	return nil
}

// Run implements exchange.MarketSource.
func (s *Source) Run(ctx context.Context, out chan<- exchange.Event) error {
	return s.connector.Run(ctx, func(ctx context.Context, conn exchange.Conn, healthy func()) error {
		sess := &session{
			src:         s,
			conn:        exchange.WithIdleTimeout(conn, s.clock, s.idleTimeout),
			out:         out,
			healthy:     healthy,
			pendingAcks: 3, // one per subscribe message
			books:       make(map[string]*bookState, len(s.products)),
		}
		for _, p := range s.products {
			sess.books[p] = &bookState{book: book.New()}
		}
		return sess.run(ctx)
	})
}

// session is the state of one connection. Books start over on every connection.
type session struct {
	src         *Source
	conn        exchange.Conn
	out         chan<- exchange.Event
	healthy     func()
	pendingAcks int
	books       map[string]*bookState // by product id
	lastSeq     int64
	seqStarted  bool
}

type bookState struct {
	book *book.Book
	// live is true between the snapshot and the end of the connection. Updates are ignored
	// while it is false.
	live bool
}

// errSequenceGap ends a connection that lost a message.
var errSequenceGap = errors.New("coinbase: sequence gap")

func (s *session) run(ctx context.Context) error {
	defer s.resetAll(ctx, exchange.ResetReconnect) // the books die with the connection

	// Coinbase drops a connection that has not subscribed within 5 seconds, so all three go
	// out at once. Three messages are well inside its 8 messages per second limit.
	if err := s.subscribe(ctx, chanHeartbeats, nil); err != nil {
		return err
	}
	if err := s.subscribe(ctx, chanLevel2, s.src.products); err != nil {
		return err
	}
	if err := s.subscribe(ctx, chanTrades, s.src.products); err != nil {
		return err
	}

	for {
		msg, err := s.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("coinbase: read: %w", err)
		}
		if err := s.handle(ctx, msg, s.src.clock.Now()); err != nil {
			return err
		}
	}
}

func (s *session) handle(ctx context.Context, msg []byte, received time.Time) error {
	var env envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		return fmt.Errorf("coinbase: decode message: %w", err)
	}
	if env.Type == "error" {
		return fmt.Errorf("coinbase: error message: %s", env.Message)
	}
	if env.SequenceNum != nil {
		seq := *env.SequenceNum
		switch {
		case !s.seqStarted || seq == s.lastSeq+1:
			s.seqStarted, s.lastSeq = true, seq
		case seq <= s.lastSeq:
			s.src.log.DebugContext(ctx, "ignoring late message", "sequence_num", seq, "last", s.lastSeq)
			return nil
		default:
			s.src.log.WarnContext(ctx, "sequence gap", "sequence_num", seq, "last", s.lastSeq)
			s.resetAll(ctx, exchange.ResetSequence)
			return fmt.Errorf("%w: got %d after %d", errSequenceGap, seq, s.lastSeq)
		}
	}

	switch env.Channel {
	case "l2_data":
		var events []l2Event
		if err := json.Unmarshal(env.Events, &events); err != nil {
			return fmt.Errorf("coinbase: decode l2_data: %w", err)
		}
		for i := range events {
			if err := s.handleBook(ctx, &events[i], env.Timestamp, received); err != nil {
				return err
			}
		}
	case "market_trades":
		var events []tradesEvent
		if err := json.Unmarshal(env.Events, &events); err != nil {
			return fmt.Errorf("coinbase: decode market_trades: %w", err)
		}
		for _, e := range events {
			for i := range e.Trades {
				s.handleTrade(ctx, &e.Trades[i], received)
			}
		}
	case "subscriptions":
		var events []subscriptionsEvent
		if err := json.Unmarshal(env.Events, &events); err != nil {
			return fmt.Errorf("coinbase: decode subscriptions: %w", err)
		}
		for _, e := range events {
			if err := s.handleAck(ctx, e.Subscriptions); err != nil {
				return err
			}
		}
	case "heartbeats":
		// Liveness only; the read already restarted the idle timeout.
	default:
		s.src.log.DebugContext(ctx, "ignoring message", "channel", env.Channel)
	}
	return nil
}

// handleAck counts subscriptions acks. The last one lists everything the connection is
// subscribed to: a product missing from it is unknown to Coinbase, which fails the same way on
// every reconnect.
func (s *session) handleAck(ctx context.Context, subs map[string][]string) error {
	if s.pendingAcks == 0 {
		return nil
	}
	s.pendingAcks--
	if s.pendingAcks > 0 {
		return nil
	}
	var missing []string
	for _, ch := range []string{chanLevel2, chanTrades} {
		for _, p := range s.src.products {
			if !slices.Contains(subs[ch], p) {
				missing = append(missing, ch+":"+p)
			}
		}
	}
	if len(missing) > 0 {
		return exchange.Permanent(fmt.Errorf("coinbase: subscriptions not accepted (unknown products?): %s",
			strings.Join(missing, ", ")))
	}
	s.src.log.InfoContext(ctx, "subscribed", "symbols", s.src.products)
	s.healthy()
	return nil
}

func (s *session) handleBook(ctx context.Context, e *l2Event, sent, received time.Time) error {
	st, ok := s.books[e.ProductID]
	if !ok {
		s.src.log.WarnContext(ctx, "book for an unsubscribed product", "product_id", e.ProductID)
		return nil
	}
	if e.Type != "snapshot" && (e.Type != "update" || !st.live) {
		return nil
	}
	bids, asks, exchangeTime, err := parseUpdates(e.Updates)
	if err != nil {
		// The book can no longer be trusted, and Coinbase has no per-product resync.
		s.src.log.WarnContext(ctx, "book update cannot be applied", "product_id", e.ProductID, "error", err)
		exchange.Send(ctx, s.out, s.reset(e.ProductID, st, exchange.ResetProtocol))
		return fmt.Errorf("coinbase: %s: %w", e.ProductID, err)
	}
	if e.Type == "snapshot" {
		// The whole book, best first: Load sorts it once instead of inserting level by level.
		st.book.Load(bids, asks)
		st.live = true
	} else {
		// The two sides are independent, so applying them one after the other keeps the order
		// that matters: per price.
		for _, l := range bids {
			st.book.Apply(book.Bid, l.Price, l.Quantity)
		}
		for _, l := range asks {
			st.book.Apply(book.Ask, l.Price, l.Quantity)
		}
	}
	if exchangeTime.IsZero() {
		exchangeTime = sent
	}

	topBids, topAsks := st.book.Top(s.src.depth)
	exchange.Send(ctx, s.out, exchange.BookUpdate{
		Exchange:     Exchange,
		Symbol:       e.ProductID,
		Bids:         topBids,
		Asks:         topAsks,
		BidLevels:    st.book.Len(book.Bid),
		AskLevels:    st.book.Len(book.Ask),
		ExchangeTime: exchangeTime,
		ReceivedTime: received,
	})
	return nil
}

// parseUpdates splits updates into bid and ask levels, each in message order, and returns the
// newest event time among them.
func parseUpdates(updates []l2Update) (bids, asks []book.Level, newest time.Time, err error) {
	for _, u := range updates {
		price, err := decimal.Parse(u.PriceLevel)
		if err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("price: %w", err)
		}
		qty, err := decimal.Parse(u.NewQuantity)
		if err != nil {
			return nil, nil, time.Time{}, fmt.Errorf("quantity: %w", err)
		}
		l := book.Level{Price: price, Quantity: qty}
		switch u.Side {
		case "bid":
			bids = append(bids, l)
		case "offer":
			asks = append(asks, l)
		default:
			return nil, nil, time.Time{}, fmt.Errorf("unknown side %q", u.Side)
		}
		if u.EventTime.After(newest) {
			newest = u.EventTime
		}
	}
	return bids, asks, newest, nil
}

// resetAll takes every live book out of sync.
func (s *session) resetAll(ctx context.Context, reason exchange.ResetReason) {
	for _, p := range s.src.products {
		if st := s.books[p]; st.live {
			exchange.Send(ctx, s.out, s.reset(p, st, reason))
		}
	}
}

func (s *session) reset(product string, st *bookState, reason exchange.ResetReason) exchange.BookReset {
	st.live = false
	st.book.Reset()
	return exchange.BookReset{Exchange: Exchange, Symbol: product, Reason: reason}
}

func (s *session) handleTrade(ctx context.Context, t *wireTrade, received time.Time) {
	if _, ok := s.books[t.ProductID]; !ok {
		s.src.log.WarnContext(ctx, "trade for an unsubscribed product", "product_id", t.ProductID)
		return
	}
	// Coinbase reports the maker's side; the taker was on the other one.
	var taker exchange.Side
	switch t.Side {
	case "BUY":
		taker = exchange.SideSell
	case "SELL":
		taker = exchange.SideBuy
	default:
		s.src.log.WarnContext(ctx, "trade with unknown side", "product_id", t.ProductID, "side", t.Side)
		return
	}
	price, perr := decimal.Parse(t.Price)
	qty, qerr := decimal.Parse(t.Size)
	if err := errors.Join(perr, qerr); err != nil || t.TradeID == "" {
		s.src.log.WarnContext(ctx, "invalid trade", "product_id", t.ProductID, "trade_id", t.TradeID, "error", err)
		return
	}
	exchange.Send(ctx, s.out, exchange.Trade{
		Exchange:     Exchange,
		Symbol:       t.ProductID,
		TradeID:      t.TradeID,
		Price:        price,
		Quantity:     qty,
		TakerSide:    taker,
		ExchangeTime: t.Time,
		ReceivedTime: received,
	})
}

func (s *session) subscribe(ctx context.Context, channel string, products []string) error {
	b, err := json.Marshal(request{Type: "subscribe", ProductIDs: products, Channel: channel})
	if err != nil {
		return fmt.Errorf("coinbase: encode subscribe: %w", err)
	}
	if err := s.conn.Write(ctx, b); err != nil {
		return fmt.Errorf("coinbase: subscribe %s: %w", channel, err)
	}
	return nil
}
