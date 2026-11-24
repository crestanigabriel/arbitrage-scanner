// Package kraken is the exchange adapter for Kraken's public WebSocket API v2
// (wss://ws.kraken.com/v2, no authentication).
//
// One connection carries the book and trade channels for every configured symbol. The adapter
// keeps a local book per symbol, verifies it against the checksum Kraken sends with every book
// message, and resubscribes a symbol whose book fails. It emits only books that pass.
package kraken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
)

// Exchange is the exchange id used in events and subjects.
const Exchange = "kraken"

// subscribeDepths are the book depths Kraken accepts, smallest first.
var subscribeDepths = []int{10, 25, 100, 500, 1000}

// maxDepth is the most levels per side the adapter emits (the OrderBook contract limit).
const maxDepth = 100

// Config configures the adapter.
type Config struct {
	// Symbols are canonical BASE-QUOTE symbols, e.g. "BTC-USD".
	Symbols []string
	// Depth is how many levels per side each BookUpdate carries, 1 to 100.
	Depth int
	// IdleTimeout ends a connection that receives nothing for this long. Kraken sends a
	// heartbeat every second while subscribed.
	IdleTimeout time.Duration
}

// Source streams Kraken books and trades. It implements exchange.MarketSource.
type Source struct {
	symbols        map[string]string // Kraken symbol → canonical symbol
	krakenSymbols  []string          // in configuration order
	depth          int
	subscribeDepth int
	idleTimeout    time.Duration
	connector      exchange.Connector
	clock          clock.Clock
	log            *slog.Logger
}

var _ exchange.MarketSource = (*Source)(nil)

// New returns a Source. connector supplies connections to Kraken (and reconnects them), clk
// timestamps events and times the idle timeout.
func New(cfg Config, connector exchange.Connector, clk clock.Clock, log *slog.Logger) (*Source, error) {
	if cfg.Depth < 1 || cfg.Depth > maxDepth {
		return nil, fmt.Errorf("kraken: depth %d out of range 1-%d", cfg.Depth, maxDepth)
	}
	if cfg.IdleTimeout <= 0 {
		return nil, fmt.Errorf("kraken: idle timeout must be positive, got %v", cfg.IdleTimeout)
	}
	if len(cfg.Symbols) == 0 {
		return nil, errors.New("kraken: no symbols")
	}
	s := &Source{
		symbols:        make(map[string]string, len(cfg.Symbols)),
		depth:          cfg.Depth,
		subscribeDepth: SubscribeDepth(cfg.Depth),
		idleTimeout:    cfg.IdleTimeout,
		connector:      connector,
		clock:          clk,
		log:            log.With("exchange", Exchange),
	}
	for _, sym := range cfg.Symbols {
		k, err := krakenSymbol(sym)
		if err != nil {
			return nil, err
		}
		if _, dup := s.symbols[k]; dup {
			return nil, fmt.Errorf("kraken: duplicate symbol %q", sym)
		}
		s.symbols[k] = sym
		s.krakenSymbols = append(s.krakenSymbols, k)
	}
	return s, nil
}

// SubscribeDepth returns the smallest Kraken book depth that covers depth levels, and never less
// than the 10 levels the checksum covers.
func SubscribeDepth(depth int) int {
	for _, d := range subscribeDepths {
		if d >= max(depth, checksumLevels) {
			return d
		}
	}
	return subscribeDepths[len(subscribeDepths)-1]
}

// krakenSymbol maps a canonical symbol ("BTC-USD") to Kraken's v2 name ("BTC/USD").
func krakenSymbol(canonical string) (string, error) {
	base, quote, ok := strings.Cut(canonical, "-")
	if !ok || base == "" || quote == "" || strings.Contains(quote, "-") {
		return "", fmt.Errorf("kraken: symbol %q is not BASE-QUOTE", canonical)
	}
	return base + "/" + quote, nil
}

// Run implements exchange.MarketSource.
func (s *Source) Run(ctx context.Context, out chan<- exchange.Event) error {
	return s.connector.Run(ctx, func(ctx context.Context, conn exchange.Conn, healthy func()) error {
		sess := &session{
			src:         s,
			conn:        exchange.WithIdleTimeout(conn, s.clock, s.idleTimeout),
			out:         out,
			healthy:     healthy,
			pendingAcks: 2 * len(s.krakenSymbols), // one per symbol for book and for trade
			books:       make(map[string]*bookState, len(s.krakenSymbols)),
		}
		for _, k := range s.krakenSymbols {
			sess.books[k] = &bookState{canonical: s.symbols[k], book: book.New()}
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
	books       map[string]*bookState // by Kraken symbol
}

type bookState struct {
	canonical string
	book      *book.Book
	// live is true between a snapshot and the next failure. Updates are ignored while it is
	// false: before the first snapshot and while a resubscribe is pending.
	live bool
}

func (s *session) run(ctx context.Context) error {
	defer func() {
		// The books die with the connection. On shutdown nobody is listening, and Send gives up.
		for _, k := range s.src.krakenSymbols {
			if st := s.books[k]; st.live {
				exchange.Send(ctx, s.out, s.reset(st, exchange.ResetReconnect))
			}
		}
	}()

	if err := s.subscribe(ctx, "book", s.src.krakenSymbols); err != nil {
		return err
	}
	if err := s.subscribe(ctx, "trade", s.src.krakenSymbols); err != nil {
		return err
	}

	for {
		msg, err := s.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("kraken: read: %w", err)
		}
		if err := s.handle(ctx, msg, s.src.clock.Now()); err != nil {
			return err
		}
	}
}

func (s *session) handle(ctx context.Context, msg []byte, received time.Time) error {
	var env envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		return fmt.Errorf("kraken: decode message: %w", err)
	}
	switch {
	case env.Method != "":
		return s.handleAck(ctx, &env)
	case env.Channel == "book":
		var data []bookData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return fmt.Errorf("kraken: decode book: %w", err)
		}
		for i := range data {
			if err := s.handleBook(ctx, env.Type, &data[i], received); err != nil {
				return err
			}
		}
	case env.Channel == "trade":
		var data []tradeData
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return fmt.Errorf("kraken: decode trades: %w", err)
		}
		for i := range data {
			s.handleTrade(ctx, &data[i], received)
		}
	case env.Channel == "heartbeat", env.Channel == "status":
		// Liveness only; the read already counted as activity.
	default:
		s.src.log.DebugContext(ctx, "ignoring message", "channel", env.Channel, "type", env.Type)
	}
	return nil
}

func (s *session) handleAck(ctx context.Context, env *envelope) error {
	success := env.Success != nil && *env.Success
	switch env.Method {
	case "subscribe":
		if !success {
			// A rejected subscription (an unknown pair) fails the same way on every reconnect.
			return exchange.Permanent(fmt.Errorf("kraken: subscribe %s rejected: %s", env.Symbol, env.Error))
		}
		if s.pendingAcks > 0 {
			s.pendingAcks--
			if s.pendingAcks == 0 {
				s.src.log.InfoContext(ctx, "subscribed", "symbols", s.src.krakenSymbols, "depth", s.src.subscribeDepth)
				s.healthy()
			}
		}
	case "unsubscribe":
		if !success {
			s.src.log.WarnContext(ctx, "unsubscribe rejected", "symbol", env.Symbol, "error", env.Error)
		}
	}
	return nil
}

func (s *session) handleBook(ctx context.Context, typ string, d *bookData, received time.Time) error {
	st, ok := s.books[d.Symbol]
	if !ok {
		s.src.log.WarnContext(ctx, "book for an unsubscribed symbol", "symbol", d.Symbol)
		return nil
	}
	switch typ {
	case "snapshot":
		st.book.Reset()
		st.live = true
	case "update":
		if !st.live {
			return nil // stale update from before a resubscribe; the snapshot is on its way
		}
	default:
		return nil
	}

	if err := applyLevels(st.book, d); err != nil {
		s.src.log.WarnContext(ctx, "book update cannot be applied", "symbol", d.Symbol, "error", err)
		return s.resync(ctx, d.Symbol, st, exchange.ResetProtocol)
	}
	// Kraken sends no removal for levels pushed out of the subscribed depth.
	st.book.Truncate(s.src.subscribeDepth)
	if got := checksum(st.book); got != d.Checksum {
		s.src.log.WarnContext(ctx, "book checksum mismatch", "symbol", d.Symbol, "got", got, "want", d.Checksum)
		return s.resync(ctx, d.Symbol, st, exchange.ResetChecksum)
	}

	bids, asks := st.book.Top(s.src.depth)
	exchange.Send(ctx, s.out, exchange.BookUpdate{
		Exchange:     Exchange,
		Symbol:       st.canonical,
		Bids:         bids,
		Asks:         asks,
		ExchangeTime: d.Timestamp,
		ReceivedTime: received,
	})
	return nil
}

// applyLevels applies every level of a book message, in order: an update may change the same
// price more than once.
func applyLevels(b *book.Book, d *bookData) error {
	for _, side := range []struct {
		side   book.Side
		levels []wireLevel
	}{{book.Bid, d.Bids}, {book.Ask, d.Asks}} {
		for _, l := range side.levels {
			price, err := decimal.Parse(string(l.Price))
			if err != nil {
				return fmt.Errorf("price: %w", err)
			}
			qty, err := decimal.Parse(string(l.Qty))
			if err != nil {
				return fmt.Errorf("quantity: %w", err)
			}
			b.Apply(side.side, price, qty)
		}
	}
	return nil
}

// resync takes a book out of sync and asks Kraken for a fresh snapshot of it.
func (s *session) resync(ctx context.Context, symbol string, st *bookState, reason exchange.ResetReason) error {
	exchange.Send(ctx, s.out, s.reset(st, reason))
	if err := s.write(ctx, "unsubscribe", "book", []string{symbol}); err != nil {
		return err
	}
	return s.subscribe(ctx, "book", []string{symbol})
}

func (s *session) reset(st *bookState, reason exchange.ResetReason) exchange.BookReset {
	st.live = false
	st.book.Reset()
	return exchange.BookReset{Exchange: Exchange, Symbol: st.canonical, Reason: reason}
}

func (s *session) handleTrade(ctx context.Context, d *tradeData, received time.Time) {
	canonical, ok := s.src.symbols[d.Symbol]
	if !ok {
		s.src.log.WarnContext(ctx, "trade for an unsubscribed symbol", "symbol", d.Symbol)
		return
	}
	var side exchange.Side
	switch d.Side {
	case "buy":
		side = exchange.SideBuy
	case "sell":
		side = exchange.SideSell
	default:
		s.src.log.WarnContext(ctx, "trade with unknown side", "symbol", d.Symbol, "side", d.Side)
		return
	}
	price, perr := decimal.Parse(string(d.Price))
	qty, qerr := decimal.Parse(string(d.Qty))
	if err := errors.Join(perr, qerr); err != nil || d.TradeID == "" {
		s.src.log.WarnContext(ctx, "invalid trade", "symbol", d.Symbol, "trade_id", d.TradeID, "error", err)
		return
	}
	exchange.Send(ctx, s.out, exchange.Trade{
		Exchange:     Exchange,
		Symbol:       canonical,
		TradeID:      d.TradeID.String(),
		Price:        price,
		Quantity:     qty,
		TakerSide:    side, // Kraken reports the taker's side
		ExchangeTime: d.Timestamp,
		ReceivedTime: received,
	})
}

func (s *session) subscribe(ctx context.Context, channel string, symbols []string) error {
	return s.write(ctx, "subscribe", channel, symbols)
}

func (s *session) write(ctx context.Context, method, channel string, symbols []string) error {
	req := request{Method: method, Params: params{Channel: channel, Symbol: symbols}}
	if channel == "book" {
		req.Params.Depth = s.src.subscribeDepth
	}
	if method == "subscribe" {
		// Books need the snapshot to start from. For trades, the snapshot (the last 50) refills
		// what was missed during a reconnect; Nats-Msg-Id deduplication drops what was not.
		snapshot := true
		req.Params.Snapshot = &snapshot
	}
	b, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("kraken: encode %s: %w", method, err)
	}
	if err := s.conn.Write(ctx, b); err != nil {
		return fmt.Errorf("kraken: %s %s: %w", method, channel, err)
	}
	return nil
}
