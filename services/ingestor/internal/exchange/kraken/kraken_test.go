package kraken_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock/clocktest"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/exchangetest"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange/kraken"
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
	src, err := kraken.New(
		kraken.Config{Symbols: symbols, Depth: depth, IdleTimeout: idle},
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

// --- frame builders ---------------------------------------------------------------------

// lv is a level as "price:qty".
func levels(ls ...string) string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		p, q, _ := strings.Cut(l, ":")
		parts[i] = fmt.Sprintf(`{"price":%s,"qty":%s}`, p, q)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// pairs turns "price:qty" levels into the form kraken.Checksum takes.
func pairs(ls ...string) [][2]string {
	out := make([][2]string, len(ls))
	for i, l := range ls {
		p, q, _ := strings.Cut(l, ":")
		out[i] = [2]string{p, q}
	}
	return out
}

// bookFrame is a book message. Its checksum is computed from wantAsks and wantBids, the state
// the local book must be in after applying it (best first, top 10).
func bookFrame(typ, symbol string, bids, asks, wantBids, wantAsks []string) string {
	sum := kraken.Checksum(pairs(wantAsks...), pairs(wantBids...))
	return bookFrameSum(typ, symbol, bids, asks, sum)
}

func bookFrameSum(typ, symbol string, bids, asks []string, sum uint32) string {
	return fmt.Sprintf(
		`{"channel":"book","type":%q,"data":[{"symbol":%q,"bids":%s,"asks":%s,"checksum":%d,"timestamp":"2026-11-24T11:59:59.5Z"}]}`,
		typ, symbol, levels(bids...), levels(asks...), sum)
}

func ack(channel, symbol string) string {
	return fmt.Sprintf(`{"method":"subscribe","result":{"channel":%q,"symbol":%q},"success":true}`, channel, symbol)
}

func render(ls []book.Level) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Price.String() + ":" + l.Quantity.String()
	}
	return out
}

func equal(a, b []string) bool { return strings.Join(a, " ") == strings.Join(b, " ") }

// --- tests ------------------------------------------------------------------------------

func TestSubscribeDepth(t *testing.T) {
	tests := map[int]int{1: 10, 10: 10, 11: 25, 25: 25, 26: 100, 50: 100, 100: 100}
	for depth, want := range tests {
		if got := kraken.SubscribeDepth(depth); got != want {
			t.Errorf("SubscribeDepth(%d) = %d, want %d", depth, got, want)
		}
	}
}

func TestNewValidates(t *testing.T) {
	tests := []kraken.Config{
		{Symbols: []string{"BTC-USD"}, Depth: 0, IdleTimeout: idle},
		{Symbols: []string{"BTC-USD"}, Depth: 101, IdleTimeout: idle},
		{Symbols: []string{"BTC-USD"}, Depth: 10},
		{Depth: 10, IdleTimeout: idle},
		{Symbols: []string{"BTCUSD"}, Depth: 10, IdleTimeout: idle},
		{Symbols: []string{"BTC-USD", "BTC-USD"}, Depth: 10, IdleTimeout: idle},
	}
	for _, cfg := range tests {
		if _, err := kraken.New(cfg, &connector{}, clocktest.NewFake(start), slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("New(%+v) succeeded, want an error", cfg)
		}
	}
}

func TestSubscribes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 50, "BTC-USD", "ETH-USD")
		synctest.Wait()

		want := []string{
			`{"method":"subscribe","params":{"channel":"book","symbol":["BTC/USD","ETH/USD"],"depth":100,"snapshot":true}}`,
			`{"method":"subscribe","params":{"channel":"trade","symbol":["BTC/USD","ETH/USD"],"snapshot":true}}`,
		}
		if got := h.conn.Written(); !equal(got, want) {
			t.Errorf("written:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}

		// healthy() is reported once every subscription is acknowledged.
		h.conn.Push(ack("book", "BTC/USD"), ack("book", "ETH/USD"), ack("trade", "BTC/USD"))
		synctest.Wait()
		if h.connector.healthy != 0 {
			t.Fatal("healthy before all acks")
		}
		h.conn.Push(ack("trade", "ETH/USD"))
		synctest.Wait()
		if h.connector.healthy != 1 {
			t.Fatalf("healthy = %d after all acks, want 1", h.connector.healthy)
		}
	})
}

func TestRejectedSubscribeIsPermanent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "NOPE-USD")
		h.conn.Push(`{"error":"Currency pair not supported NOPE/USD","method":"subscribe","success":false,"symbol":"NOPE/USD"}`)
		err := <-h.done
		if _, ok := errors.AsType[*exchange.PermanentError](err); !ok {
			t.Fatalf("Run() = %v, want a PermanentError", err)
		}
		if !strings.Contains(err.Error(), "Currency pair not supported") {
			t.Errorf("error %q does not carry Kraken's reason", err)
		}
	})
}

func TestBookSnapshotAndUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 2, "BTC-USD")
		h.clock.Advance(time.Second)

		// An update before the first snapshot is ignored.
		h.conn.Push(bookFrameSum("update", "BTC/USD", []string{"99:1"}, nil, 0))
		if evs := h.events(); len(evs) != 0 {
			t.Fatalf("events before the snapshot: %v", evs)
		}

		bids := []string{"100.0:1.00000000", "99.5:2.00000000", "99.0:3.00000000"}
		asks := []string{"100.5:0.50000000", "101.0:0.10000000"}
		h.conn.Push(bookFrame("snapshot", "BTC/USD", bids, asks, bids, asks))
		evs := h.events()
		if len(evs) != 1 {
			t.Fatalf("got %d events after the snapshot, want 1: %v", len(evs), evs)
		}
		u, ok := evs[0].(exchange.BookUpdate)
		if !ok {
			t.Fatalf("event = %T, want BookUpdate", evs[0])
		}
		// Depth 2: the third bid is in the local book (and the checksum) but not emitted.
		if !equal(render(u.Bids), bids[:2]) || !equal(render(u.Asks), asks) {
			t.Errorf("BookUpdate bids %v asks %v, want %v %v", render(u.Bids), render(u.Asks), bids[:2], asks)
		}
		if u.Exchange != "kraken" || u.Symbol != "BTC-USD" {
			t.Errorf("BookUpdate is %s %s, want kraken BTC-USD", u.Exchange, u.Symbol)
		}
		if want := time.Date(2026, 11, 24, 11, 59, 59, 500e6, time.UTC); !u.ExchangeTime.Equal(want) {
			t.Errorf("ExchangeTime = %v, want %v", u.ExchangeTime, want)
		}
		if want := start.Add(time.Second); !u.ReceivedTime.Equal(want) {
			t.Errorf("ReceivedTime = %v, want the clock's %v", u.ReceivedTime, want)
		}

		// An update that changes, removes and adds levels, the same price twice.
		h.conn.Push(bookFrame("update", "BTC/USD",
			[]string{"100.0:0.00000000", "99.8:4.00000000", "99.8:4.50000000"},
			[]string{"100.5:0.25000000"},
			[]string{"99.8:4.50000000", "99.5:2.00000000", "99.0:3.00000000"},
			[]string{"100.5:0.25000000", "101.0:0.10000000"},
		))
		evs = h.events()
		if len(evs) != 1 {
			t.Fatalf("got %d events after the update, want 1: %v", len(evs), evs)
		}
		u = evs[0].(exchange.BookUpdate)
		if want := []string{"99.8:4.50000000", "99.5:2.00000000"}; !equal(render(u.Bids), want) {
			t.Errorf("bids after update = %v, want %v", render(u.Bids), want)
		}
	})
}

func TestBookTruncatesToSubscribedDepth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD") // subscribes at depth 10
		var bids []string
		for i := range 10 {
			bids = append(bids, fmt.Sprintf("%d:1", 100-i)) // 100 … 91
		}
		asks := []string{"101:1"}
		h.conn.Push(bookFrame("snapshot", "BTC/USD", bids, asks, bids, asks))

		// A better bid arrives. Kraken drops 91 from its depth-10 book without telling us; the
		// checksum only matches if we drop it too.
		want := append([]string{"100.5:1"}, bids[:9]...)
		h.conn.Push(bookFrame("update", "BTC/USD", []string{"100.5:1"}, nil, want, asks))

		// Later, 100 is removed. Kraken now sends 91 again, because it came back into range.
		want2 := append([]string{"100.5:1"}, bids[1:]...)
		h.conn.Push(bookFrame("update", "BTC/USD", []string{"100:0", "91:1"}, nil, want2, asks))

		evs := h.events()
		if len(evs) != 3 {
			t.Fatalf("got %d events, want 3 BookUpdates: %v", len(evs), evs)
		}
		for i, e := range evs {
			if _, ok := e.(exchange.BookUpdate); !ok {
				t.Fatalf("event %d = %#v, want BookUpdate", i, e)
			}
		}
		if got := render(evs[2].(exchange.BookUpdate).Bids); !equal(got, want2) {
			t.Errorf("bids = %v, want %v", got, want2)
		}
	})
}

func TestChecksumMismatchResubscribes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD", "ETH-USD")
		bids, asks := []string{"100:1"}, []string{"101:1"}
		h.conn.Push(
			bookFrame("snapshot", "BTC/USD", bids, asks, bids, asks),
			bookFrame("snapshot", "ETH/USD", bids, asks, bids, asks),
		)
		h.events()
		writesBefore := len(h.conn.Written())

		h.conn.Push(bookFrameSum("update", "BTC/USD", []string{"100:2"}, nil, 12345))
		evs := h.events()
		want := exchange.BookReset{Exchange: "kraken", Symbol: "BTC-USD", Reason: exchange.ResetChecksum}
		if len(evs) != 1 || evs[0] != want {
			t.Fatalf("events = %v, want [%v]", evs, want)
		}
		wantWrites := []string{
			`{"method":"unsubscribe","params":{"channel":"book","symbol":["BTC/USD"],"depth":10}}`,
			`{"method":"subscribe","params":{"channel":"book","symbol":["BTC/USD"],"depth":10,"snapshot":true}}`,
		}
		if got := h.conn.Written()[writesBefore:]; !equal(got, wantWrites) {
			t.Errorf("written after mismatch:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantWrites, "\n"))
		}

		// Until the new snapshot, BTC updates are ignored; ETH is unaffected.
		h.conn.Push(
			bookFrame("update", "BTC/USD", []string{"100:3"}, nil, []string{"100:3"}, asks),
			bookFrame("update", "ETH/USD", []string{"100:3"}, nil, []string{"100:3"}, asks),
		)
		evs = h.events()
		if len(evs) != 1 || evs[0].(exchange.BookUpdate).Symbol != "ETH-USD" {
			t.Fatalf("events while BTC resyncs = %v, want one ETH-USD BookUpdate", evs)
		}

		h.conn.Push(bookFrame("snapshot", "BTC/USD", []string{"100:4"}, asks, []string{"100:4"}, asks))
		evs = h.events()
		if len(evs) != 1 || evs[0].(exchange.BookUpdate).Symbol != "BTC-USD" {
			t.Fatalf("events after the new snapshot = %v, want one BTC-USD BookUpdate", evs)
		}
	})
}

func TestInvalidNumberResyncs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD")
		bids, asks := []string{"100:1"}, []string{"101:1"}
		h.conn.Push(bookFrame("snapshot", "BTC/USD", bids, asks, bids, asks))
		h.events()

		h.conn.Push(bookFrameSum("update", "BTC/USD", []string{"1e2:1"}, nil, 0))
		evs := h.events()
		want := exchange.BookReset{Exchange: "kraken", Symbol: "BTC-USD", Reason: exchange.ResetProtocol}
		if len(evs) != 1 || evs[0] != want {
			t.Fatalf("events = %v, want [%v]", evs, want)
		}
	})
}

func TestTrades(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD")
		h.conn.Push(`{"channel":"trade","type":"update","data":[` +
			`{"symbol":"BTC/USD","side":"buy","price":59977.0,"qty":0.08896249,"ord_type":"market","trade_id":117760264,"timestamp":"2026-11-24T18:22:08.018565Z"},` +
			`{"symbol":"BTC/USD","side":"sell","price":"59980.3","qty":"0.00006120","ord_type":"limit","trade_id":117760265,"timestamp":"2026-11-24T18:22:08.5Z"},` +
			`{"symbol":"BTC/USD","side":"sideways","price":1,"qty":1,"trade_id":1,"timestamp":"2026-11-24T18:22:08.5Z"},` +
			`{"symbol":"BTC/USD","side":"buy","price":-1,"qty":1,"trade_id":2,"timestamp":"2026-11-24T18:22:08.5Z"}]}`)
		evs := h.events()
		if len(evs) != 2 {
			t.Fatalf("got %d events, want 2 trades (invalid ones dropped): %v", len(evs), evs)
		}
		tr := evs[0].(exchange.Trade)
		if tr.Exchange != "kraken" || tr.Symbol != "BTC-USD" || tr.TradeID != "117760264" ||
			tr.Price.String() != "59977.0" || tr.Quantity.String() != "0.08896249" ||
			tr.TakerSide != exchange.SideBuy {
			t.Errorf("trade = %+v", tr)
		}
		if want := time.Date(2026, 11, 24, 18, 22, 8, 18565000, time.UTC); !tr.ExchangeTime.Equal(want) {
			t.Errorf("ExchangeTime = %v, want %v", tr.ExchangeTime, want)
		}
		if tr2 := evs[1].(exchange.Trade); tr2.TakerSide != exchange.SideSell || tr2.Quantity.String() != "0.00006120" {
			t.Errorf("second trade (quoted numbers) = %+v", tr2)
		}
	})
}

func TestReadErrorResetsLiveBooks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD", "ETH-USD")
		bids, asks := []string{"100:1"}, []string{"101:1"}
		h.conn.Push(bookFrame("snapshot", "BTC/USD", bids, asks, bids, asks))
		h.events()

		cause := errors.New("connection reset by peer")
		h.conn.Fail(cause)
		if err := <-h.done; !errors.Is(err, cause) {
			t.Fatalf("Run() = %v, want it to wrap %v", err, cause)
		}
		// Only BTC was live; ETH never got a snapshot, so there is nothing to reset.
		want := exchange.BookReset{Exchange: "kraken", Symbol: "BTC-USD", Reason: exchange.ResetReconnect}
		if evs := h.events(); len(evs) != 1 || evs[0] != want {
			t.Fatalf("events = %v, want [%v]", evs, want)
		}
	})
}

func TestIdleTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := runSource(t, 10, "BTC-USD")
		synctest.Wait()

		// Heartbeats keep the connection alive.
		for range 3 {
			h.clock.Advance(idle - time.Millisecond)
			h.conn.Push(`{"channel":"heartbeat"}`)
			synctest.Wait()
		}
		select {
		case err := <-h.done:
			t.Fatalf("Run() returned %v while heartbeats arrived", err)
		default:
		}

		h.clock.Advance(idle)
		err := <-h.done
		if err == nil || !strings.Contains(err.Error(), "idle timeout") {
			t.Fatalf("Run() = %v, want the idle timeout error", err)
		}
	})
}

// TestRecordedFrames replays live Kraken frames recorded on 2026-11-24 (BTC/USD, depth 10 and
// depth 100 with trades). Every book message carries Kraken's own checksum, so a single
// BookReset means the book or checksum code disagrees with the exchange.
func TestRecordedFrames(t *testing.T) {
	tests := []struct {
		file   string
		depth  int
		trades bool
	}{
		{"btcusd_depth10.ndjson", 10, false},
		{"btcusd_depth100_trade.ndjson", 50, true},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := runSource(t, tt.depth, "BTC-USD")
				books, trades := pushFile(t, h.conn, tt.file)

				var gotBooks, gotTrades int
				for _, e := range h.events() {
					switch e := e.(type) {
					case exchange.BookUpdate:
						gotBooks++
						if len(e.Bids) > tt.depth || len(e.Asks) > tt.depth {
							t.Fatalf("BookUpdate deeper than %d: %d bids, %d asks", tt.depth, len(e.Bids), len(e.Asks))
						}
					case exchange.Trade:
						gotTrades++
					case exchange.BookReset:
						t.Fatalf("BookReset after %d good books: %+v", gotBooks, e)
					}
				}
				t.Logf("%d books, %d trades", gotBooks, gotTrades)
				if gotBooks != books {
					t.Errorf("BookUpdates = %d, want one per book message (%d)", gotBooks, books)
				}
				if gotTrades != trades {
					t.Errorf("Trades = %d, want %d", gotTrades, trades)
				}
				if tt.trades && trades == 0 {
					t.Error("recording has no trades")
				}
			})
		})
	}
}

// pushFile queues every frame of an NDJSON recording in testdata/ and counts its book entries and trades.
func pushFile(t *testing.T, conn *exchangetest.Conn, path string) (books, trades int) {
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
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m struct {
			Channel string            `json:"channel"`
			Data    []json.RawMessage `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			switch m.Channel {
			case "book":
				books += len(m.Data)
			case "trade":
				trades += len(m.Data)
			}
		}
		conn.Push(sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return books, trades
}
