package coinbase_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock/clocktest"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/coinbase"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/exchangetest"
)

var start = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

const idle = 10 * time.Second

// connector runs a session once on a scripted Conn and returns its error, so a test drives
// exactly one connection. Reconnecting is the Reconnector's job and is tested there.
type connector struct {
	conn    *exchangetest.Conn
	healthy int
}

func (c *connector) Run(ctx context.Context, s exchange.Session) error {
	return s(ctx, c.conn, func() { c.healthy++ })
}

type harness struct {
	conn      *exchangetest.Conn
	connector *connector
	clock     *clocktest.Fake
	out       chan exchange.Event
	done      chan error
	seq       int
}

// runSource runs a Source for symbols at depth over a fresh scripted Conn. Call it inside a
// synctest bubble.
func runSource(t *testing.T, depth int, symbols ...string) *harness {
	t.Helper()
	h := &harness{
		conn:  exchangetest.NewConn(),
		clock: clocktest.NewFake(start),
		out:   make(chan exchange.Event, 10000),
		done:  make(chan error, 1),
	}
	h.connector = &connector{conn: h.conn}
	src, err := coinbase.New(
		coinbase.Config{Symbols: symbols, Depth: depth, IdleTimeout: idle},
		h.connector, h.clock, slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	go func() { h.done <- src.Run(t.Context(), h.out) }()
	return h
}

// events returns the events emitted so far, once the source is idle.
func (h *harness) events() []exchange.Event {
	synctest.Wait()
	var evs []exchange.Event
	for {
		select {
		case e := <-h.out:
			evs = append(evs, e)
		default:
			return evs
		}
	}
}

// push sends a message on channel with the next sequence number.
func (h *harness) push(channel, events string) {
	h.conn.Push(fmt.Sprintf(`{"channel":%q,"timestamp":"2026-11-24T11:59:59.9Z","sequence_num":%d,"events":%s}`,
		channel, h.seq, events))
	h.seq++
}

// subscribed acknowledges the three subscribe messages for products.
func (h *harness) subscribed(products ...string) {
	ps, _ := json.Marshal(products)
	h.push("subscriptions", `[{"subscriptions":{"heartbeats":["heartbeats"]}}]`)
	h.push("subscriptions", fmt.Sprintf(`[{"subscriptions":{"heartbeats":["heartbeats"],"level2":%s}}]`, ps))
	h.push("subscriptions", fmt.Sprintf(`[{"subscriptions":{"heartbeats":["heartbeats"],"level2":%s,"market_trades":%s}}]`, ps, ps))
}

// --- frame builders ---------------------------------------------------------------------

// l2 is an l2_data event. Levels are "bid:price:qty" or "offer:price:qty".
func l2(typ, product string, levels ...string) string {
	parts := make([]string, len(levels))
	for i, l := range levels {
		f := strings.SplitN(l, ":", 3)
		parts[i] = fmt.Sprintf(`{"side":%q,"event_time":"2026-11-24T11:59:59.%dZ","price_level":%q,"new_quantity":%q}`,
			f[0], i%10, f[1], f[2])
	}
	return fmt.Sprintf(`[{"type":%q,"product_id":%q,"updates":[%s]}]`, typ, product, strings.Join(parts, ","))
}

func trade(product, id, price, size, side string) string {
	return fmt.Sprintf(`[{"type":"update","trades":[{"product_id":%q,"trade_id":%q,"price":%q,"size":%q,"side":%q,"time":"2026-11-24T11:59:58Z"}]}]`,
		product, id, price, size, side)
}

func render(ls []book.Level) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Price.String() + ":" + l.Quantity.String()
	}
	return out
}

func books(evs []exchange.Event) []exchange.BookUpdate {
	var out []exchange.BookUpdate
	for _, e := range evs {
		if b, ok := e.(exchange.BookUpdate); ok {
			out = append(out, b)
		}
	}
	return out
}

// --- tests ------------------------------------------------------------------------------

func TestNewValidates(t *testing.T) {
	bad := map[string]coinbase.Config{
		"depth 0":        {Symbols: []string{"BTC-USD"}, Depth: 0, IdleTimeout: idle},
		"depth 101":      {Symbols: []string{"BTC-USD"}, Depth: 101, IdleTimeout: idle},
		"no idle":        {Symbols: []string{"BTC-USD"}, Depth: 10},
		"no symbols":     {Depth: 10, IdleTimeout: idle},
		"not BASE-QUOTE": {Symbols: []string{"BTCUSD"}, Depth: 10, IdleTimeout: idle},
		"duplicate":      {Symbols: []string{"BTC-USD", "BTC-USD"}, Depth: 10, IdleTimeout: idle},
		"USDC alias":     {Symbols: []string{"BTC-USDC"}, Depth: 10, IdleTimeout: idle},
	}
	for name, cfg := range bad {
		if _, err := coinbase.New(cfg, &connector{}, clocktest.NewFake(start), slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("%s: New() succeeded", name)
		}
	}
	ok := coinbase.Config{Symbols: []string{"BTC-USD", "USDT-USDC"}, Depth: 100, IdleTimeout: idle}
	if _, err := coinbase.New(ok, &connector{}, clocktest.NewFake(start), slog.New(slog.DiscardHandler)); err != nil {
		t.Errorf("New(%+v) error = %v", ok, err)
	}
}

func TestSubscribes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD", "ETH-USD")
		synctest.Wait()
		want := []string{
			`{"type":"subscribe","channel":"heartbeats"}`,
			`{"type":"subscribe","product_ids":["BTC-USD","ETH-USD"],"channel":"level2"}`,
			`{"type":"subscribe","product_ids":["BTC-USD","ETH-USD"],"channel":"market_trades"}`,
		}
		if got := h.conn.Written(); !slices.Equal(got, want) {
			t.Fatalf("written = %q, want %q", got, want)
		}

		h.subscribed("BTC-USD", "ETH-USD")
		synctest.Wait()
		if h.connector.healthy != 1 {
			t.Errorf("healthy called %d times after the acks, want 1", h.connector.healthy)
		}
	})
}

func TestUnknownProductIsPermanent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD", "NOPE-USD")
		h.subscribed("BTC-USD") // Coinbase leaves unknown products out of the ack
		err := <-h.done
		if _, ok := errors.AsType[*exchange.PermanentError](err); !ok || !strings.Contains(err.Error(), "NOPE-USD") {
			t.Fatalf("Run() = %v, want a permanent error naming NOPE-USD", err)
		}
		if h.connector.healthy != 0 {
			t.Error("healthy called although a subscription was not accepted")
		}
	})
}

func TestSnapshotAndUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 2, "BTC-USD")
		h.push("l2_data", l2("update", "BTC-USD", "bid:99:1")) // before the snapshot: ignored
		h.push("l2_data", l2("snapshot", "BTC-USD",
			"bid:100:1", "bid:99:2", "bid:98:3", "offer:101:1", "offer:102:2", "offer:103:3"))
		h.push("l2_data", l2("update", "BTC-USD",
			"bid:100:0", "bid:99.5:4", "offer:101:0.5", "offer:100.5:7", "offer:104:9"))

		got := books(h.events())
		if len(got) != 2 {
			t.Fatalf("got %d BookUpdates, want 2 (the snapshot and the update)", len(got))
		}
		snap, upd := got[0], got[1]
		if b, a := render(snap.Bids), render(snap.Asks); !slices.Equal(b, []string{"100:1", "99:2"}) || !slices.Equal(a, []string{"101:1", "102:2"}) {
			t.Errorf("snapshot top = %v / %v", b, a)
		}
		if snap.BidLevels != 3 || snap.AskLevels != 3 {
			t.Errorf("snapshot local levels = %d/%d, want 3/3: the whole book is kept", snap.BidLevels, snap.AskLevels)
		}
		if b, a := render(upd.Bids), render(upd.Asks); !slices.Equal(b, []string{"99.5:4", "99:2"}) || !slices.Equal(a, []string{"100.5:7", "101:0.5"}) {
			t.Errorf("update top = %v / %v", b, a)
		}
		if upd.BidLevels != 3 || upd.AskLevels != 5 {
			t.Errorf("update local levels = %d/%d, want 3/5", upd.BidLevels, upd.AskLevels)
		}
		if want := time.Date(2026, 11, 24, 11, 59, 59, 400_000_000, time.UTC); !upd.ExchangeTime.Equal(want) {
			t.Errorf("ExchangeTime = %v, want the newest event_time %v", upd.ExchangeTime, want)
		}
		if !upd.ReceivedTime.Equal(start) || upd.Exchange != "coinbase" || upd.Symbol != "BTC-USD" {
			t.Errorf("update = %+v", upd)
		}
	})
}

func TestSequenceGapResetsEveryBook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD", "ETH-USD")
		h.subscribed("BTC-USD", "ETH-USD")
		h.push("l2_data", l2("snapshot", "BTC-USD", "bid:100:1", "offer:101:1"))
		h.push("l2_data", l2("snapshot", "ETH-USD", "bid:10:1", "offer:11:1"))
		_ = h.events()

		h.seq++ // one message lost
		h.push("heartbeats", `[{"current_time":"x","heartbeat_counter":1}]`)
		err := <-h.done
		if err == nil || !strings.Contains(err.Error(), "sequence gap") {
			t.Fatalf("Run() = %v, want a sequence gap error", err)
		}
		if _, ok := errors.AsType[*exchange.PermanentError](err); ok {
			t.Fatal("a sequence gap must reconnect, not stop")
		}
		want := []exchange.Event{
			exchange.BookReset{Exchange: "coinbase", Symbol: "BTC-USD", Reason: exchange.ResetSequence},
			exchange.BookReset{Exchange: "coinbase", Symbol: "ETH-USD", Reason: exchange.ResetSequence},
		}
		if got := h.events(); !slices.Equal(got, want) {
			t.Fatalf("events = %v, want %v (once each)", got, want)
		}
	})
}

func TestLateMessageIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD")
		h.push("l2_data", l2("snapshot", "BTC-USD", "bid:100:1", "offer:101:1"))
		h.push("l2_data", l2("update", "BTC-USD", "bid:100:2"))
		h.seq = 0 // a duplicate of the first message
		h.push("l2_data", l2("update", "BTC-USD", "bid:100:9"))
		if got := books(h.events()); len(got) != 2 || render(got[1].Bids)[0] != "100:2" {
			t.Fatalf("BookUpdates = %+v, want the late message ignored", got)
		}
		select {
		case err := <-h.done:
			t.Fatalf("Run() = %v after a late message", err)
		default:
		}
	})
}

func TestBadUpdateResetsAndReconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD")
		h.push("l2_data", l2("snapshot", "BTC-USD", "bid:100:1", "offer:101:1"))
		h.push("l2_data", l2("update", "BTC-USD", "bid:1e5:1"))
		if err := <-h.done; err == nil {
			t.Fatal("Run() returned nil after an invalid price")
		}
		evs := h.events()
		want := exchange.BookReset{Exchange: "coinbase", Symbol: "BTC-USD", Reason: exchange.ResetProtocol}
		if len(evs) != 2 || evs[1] != want {
			t.Fatalf("events = %v, want the snapshot then %v", evs, want)
		}
	})
}

func TestConnectionLossResetsLiveBooks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD", "ETH-USD")
		h.push("l2_data", l2("snapshot", "BTC-USD", "bid:100:1", "offer:101:1"))
		_ = h.events()
		cause := errors.New("connection reset by peer")
		h.conn.Fail(cause)
		if err := <-h.done; !errors.Is(err, cause) {
			t.Fatalf("Run() = %v, want it to wrap %v", err, cause)
		}
		want := []exchange.Event{exchange.BookReset{Exchange: "coinbase", Symbol: "BTC-USD", Reason: exchange.ResetReconnect}}
		if got := h.events(); !slices.Equal(got, want) {
			t.Fatalf("events = %v, want %v", got, want)
		}
	})
}

func TestTradesTakeTheTakerSide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD")
		h.push("market_trades", trade("BTC-USD", "1", "100.5", "0.25", "BUY"))
		h.push("market_trades", trade("BTC-USD", "2", "100.6", "0.5", "SELL"))
		h.push("market_trades", trade("BTC-USD", "3", "100.6", "0.5", "MAYBE")) // dropped

		evs := h.events()
		want := []exchange.Event{
			exchange.Trade{
				Exchange: "coinbase", Symbol: "BTC-USD", TradeID: "1",
				Price: decimal.MustParse("100.5"), Quantity: decimal.MustParse("0.25"),
				TakerSide:    exchange.SideSell, // the maker bought, so the taker sold
				ExchangeTime: time.Date(2026, 11, 24, 11, 59, 58, 0, time.UTC), ReceivedTime: start,
			},
			exchange.Trade{
				Exchange: "coinbase", Symbol: "BTC-USD", TradeID: "2",
				Price: decimal.MustParse("100.6"), Quantity: decimal.MustParse("0.5"),
				TakerSide:    exchange.SideBuy,
				ExchangeTime: time.Date(2026, 11, 24, 11, 59, 58, 0, time.UTC), ReceivedTime: start,
			},
		}
		if !slices.Equal(evs, want) {
			t.Fatalf("events = %+v\nwant %+v", evs, want)
		}
	})
}

func TestErrorMessageReconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD")
		h.conn.Push(`{"type":"error","message":"authentication failure"}`)
		err := <-h.done
		if err == nil || !strings.Contains(err.Error(), "authentication failure") {
			t.Fatalf("Run() = %v, want the exchange's error", err)
		}
		if _, ok := errors.AsType[*exchange.PermanentError](err); ok {
			t.Fatal("an error message must reconnect, not stop")
		}
	})
}

func TestIdleTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD")
		synctest.Wait()
		for range 3 {
			h.clock.Advance(idle - time.Millisecond)
			h.push("heartbeats", `[{"current_time":"x","heartbeat_counter":1}]`)
			synctest.Wait()
		}
		select {
		case err := <-h.done:
			t.Fatalf("Run() returned %v while heartbeats arrived", err)
		default:
		}
		h.clock.Advance(idle)
		if err := <-h.done; !errors.Is(err, exchange.ErrIdle) {
			t.Fatalf("Run() = %v, want the idle timeout", err)
		}
	})
}

// TestRecordedFrames replays a live BTC-USD session recorded on 2026-11-24 (the snapshot
// trimmed to 200 levels per side). Coinbase has no checksum, so the oracle is a reference book
// kept by the test itself, in exact rationals. The last BookUpdate must equal its top.
func TestRecordedFrames(t *testing.T) {
	const depth = 50
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, depth, "BTC-USD")
		ref, l2msgs, trades := pushFile(t, h.conn, "btcusd.ndjson")

		var got []exchange.BookUpdate
		var gotTrades int
		for _, e := range h.events() {
			switch e := e.(type) {
			case exchange.BookUpdate:
				got = append(got, e)
			case exchange.Trade:
				gotTrades++
			case exchange.BookReset:
				t.Fatalf("BookReset after %d good books: %+v", len(got), e)
			}
		}
		if len(got) != l2msgs {
			t.Fatalf("BookUpdates = %d, want one per l2_data event (%d)", len(got), l2msgs)
		}
		if gotTrades != trades || trades == 0 {
			t.Errorf("Trades = %d, want %d (and more than 0)", gotTrades, trades)
		}
		last := got[len(got)-1]
		if b, want := render(last.Bids), ref.top("bid", depth); !slices.Equal(b, want) {
			t.Errorf("bids = %v\nwant %v", b, want)
		}
		if a, want := render(last.Asks), ref.top("offer", depth); !slices.Equal(a, want) {
			t.Errorf("asks = %v\nwant %v", a, want)
		}
		if last.BidLevels != len(ref["bid"]) || last.AskLevels != len(ref["offer"]) {
			t.Errorf("local levels = %d/%d, want %d/%d", last.BidLevels, last.AskLevels, len(ref["bid"]), len(ref["offer"]))
		}
		if bb, ba := rat(last.Bids[0].Price.String()), rat(last.Asks[0].Price.String()); bb.Cmp(ba) >= 0 {
			t.Errorf("crossed book: best bid %v >= best ask %v", bb, ba)
		}
		select {
		case err := <-h.done:
			t.Fatalf("Run() = %v", err)
		default:
		}
	})
}

// refBook is side → price text → quantity text.
type refBook map[string]map[string]string

func (r refBook) top(side string, n int) []string {
	prices := make([]string, 0, len(r[side]))
	for p := range r[side] {
		prices = append(prices, p)
	}
	slices.SortFunc(prices, func(a, b string) int {
		if side == "bid" {
			return rat(b).Cmp(rat(a))
		}
		return rat(a).Cmp(rat(b))
	})
	out := make([]string, 0, n)
	for _, p := range prices[:min(n, len(prices))] {
		out = append(out, p+":"+r[side][p])
	}
	return out
}

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("bad decimal " + s)
	}
	return r
}

// pushFile queues every frame of an NDJSON recording in testdata/, and builds the reference
// book from its l2_data events.
func pushFile(t *testing.T, conn *exchangetest.Conn, path string) (ref refBook, l2events, trades int) {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	ref = refBook{"bid": {}, "offer": {}}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m struct {
			Channel string `json:"channel"`
			Events  []struct {
				Type    string `json:"type"`
				Updates []struct {
					Side        string `json:"side"`
					PriceLevel  string `json:"price_level"`
					NewQuantity string `json:"new_quantity"`
				} `json:"updates"`
				Trades []json.RawMessage `json:"trades"`
			} `json:"events"`
		}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		for _, e := range m.Events {
			switch m.Channel {
			case "l2_data":
				l2events++
				if e.Type == "snapshot" {
					ref = refBook{"bid": {}, "offer": {}}
				}
				for _, u := range e.Updates {
					// Key by exact value, so "100.10" and "100.1" are one level.
					key := u.PriceLevel
					for p := range ref[u.Side] {
						if rat(p).Cmp(rat(key)) == 0 {
							key = p
						}
					}
					if rat(u.NewQuantity).Sign() == 0 {
						delete(ref[u.Side], key)
					} else {
						ref[u.Side][key] = u.NewQuantity
					}
				}
			case "market_trades":
				trades += len(e.Trades)
			}
		}
		conn.Push(sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return ref, l2events, trades
}
