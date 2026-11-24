package publish_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	marketdatav1 "github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/gen/arbscanner/marketdata/v1"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/publish"
)

// startJetStream runs an in-process NATS server with JetStream and returns a client for it.
func startJetStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	srv, err := server.NewServer(&server.Options{
		Host:      "127.0.0.1",
		Port:      server.RANDOM_PORT,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
	})
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go srv.Start()
	t.Cleanup(srv.Shutdown)
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats server not ready")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	return js
}

func setup(t *testing.T) (context.Context, jetstream.JetStream, *publish.JetStream) {
	t.Helper()
	ctx := t.Context()
	js := startJetStream(t)
	if err := publish.EnsureStreams(ctx, js); err != nil {
		t.Fatalf("EnsureStreams() error = %v", err)
	}
	return ctx, js, publish.NewJetStream(js)
}

func TestEnsureStreams(t *testing.T) {
	ctx, js, _ := setup(t)

	// Running it again (every ingestor start) is fine.
	if err := publish.EnsureStreams(ctx, js); err != nil {
		t.Fatalf("second EnsureStreams() error = %v", err)
	}

	books, err := js.Stream(ctx, publish.BooksStream)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := books.CachedInfo().Config; cfg.MaxMsgsPerSubject != 1 || cfg.Subjects[0] != "md.book.>" {
		t.Errorf("%s config: subjects %v, max msgs per subject %d", publish.BooksStream, cfg.Subjects, cfg.MaxMsgsPerSubject)
	}
	trades, err := js.Stream(ctx, publish.TradesStream)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := trades.CachedInfo().Config; cfg.MaxAge != publish.TradesMaxAge ||
		cfg.Duplicates != publish.TradesDuplicateWindow || cfg.Subjects[0] != "md.trade.>" {
		t.Errorf("%s config: subjects %v, max age %v, duplicates %v", publish.TradesStream, cfg.Subjects, cfg.MaxAge, cfg.Duplicates)
	}
}

func TestPublishBookKeepsLatestPerSubject(t *testing.T) {
	ctx, js, p := setup(t)

	for seq := uint64(1); seq <= 3; seq++ {
		for _, sym := range []string{"BTC-USD", "ETH-USD"} {
			b := &marketdatav1.OrderBook{Exchange: "kraken", Symbol: sym, Sequence: seq, Depth: 50}
			if err := p.PublishBook(ctx, b); err != nil {
				t.Fatalf("PublishBook() error = %v", err)
			}
		}
	}

	stream, err := js.Stream(ctx, publish.BooksStream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 2 {
		t.Errorf("stream holds %d messages, want 2 (one per subject)", info.State.Msgs)
	}

	msg, err := stream.GetLastMsgForSubject(ctx, "md.book.kraken.BTC-USD")
	if err != nil {
		t.Fatal(err)
	}
	var got marketdatav1.OrderBook
	if err := proto.Unmarshal(msg.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.GetSequence() != 3 || got.GetSymbol() != "BTC-USD" {
		t.Errorf("last book = %s sequence %d, want BTC-USD sequence 3", got.GetSymbol(), got.GetSequence())
	}
}

func TestPublishTradeDeduplicates(t *testing.T) {
	ctx, js, p := setup(t)

	tr := &marketdatav1.Trade{Exchange: "kraken", Symbol: "BTC-USD", TradeId: "117760264", Price: "59977.0", Quantity: "0.08896249"}
	for range 2 {
		if err := p.PublishTrade(ctx, tr); err != nil {
			t.Fatalf("PublishTrade() error = %v", err)
		}
	}
	other := proto.CloneOf(tr)
	other.TradeId = "117760265"
	if err := p.PublishTrade(ctx, other); err != nil {
		t.Fatal(err)
	}

	stream, err := js.Stream(ctx, publish.TradesStream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.State.Msgs != 2 {
		t.Errorf("stream holds %d trades, want 2 (the duplicate dropped)", info.State.Msgs)
	}
	msg, err := stream.GetMsg(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.Header.Get(jetstream.MsgIDHeader); got != "kraken.BTC-USD.117760264" {
		t.Errorf("Nats-Msg-Id = %q, want kraken.BTC-USD.117760264", got)
	}
	if msg.Subject != "md.trade.kraken.BTC-USD" {
		t.Errorf("subject = %q, want md.trade.kraken.BTC-USD", msg.Subject)
	}
}

func TestSubject(t *testing.T) {
	if got, err := publish.Subject("book", "kraken", "BTC-USD"); err != nil || got != "md.book.kraken.BTC-USD" {
		t.Errorf("Subject() = %q, %v", got, err)
	}
	for _, bad := range []string{"", "BTC.USD", "BTC*", "BTC>", "BTC USD"} {
		if _, err := publish.Subject("book", "kraken", bad); err == nil {
			t.Errorf("Subject(%q) succeeded, want an error", bad)
		}
	}
}
