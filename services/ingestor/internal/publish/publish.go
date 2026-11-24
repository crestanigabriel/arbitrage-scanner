// Package publish sends market data to NATS JetStream.
//
// Subjects follow ADR-001: md.book.<exchange>.<symbol> and md.trade.<exchange>.<symbol>. Books
// and trades go to separate streams because their retention differs: only the latest book per
// subject matters, while trades are kept for a few minutes. JetStream applies its limits per
// stream, so one stream could not do both.
package publish

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	marketdatav1 "github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/gen/arbscanner/marketdata/v1"
)

// Publisher sends books and trades to the message bus.
type Publisher interface {
	PublishBook(ctx context.Context, b *marketdatav1.OrderBook) error
	PublishTrade(ctx context.Context, t *marketdatav1.Trade) error
}

// Stream names and settings.
const (
	BooksStream  = "MD_BOOKS"
	TradesStream = "MD_TRADES"

	// TradesMaxAge is how long trades are kept: long enough for a consumer restart, short
	// enough that the stream stays small.
	TradesMaxAge = 10 * time.Minute
	// TradesDuplicateWindow is how long a Nats-Msg-Id is remembered. A reconnect refills the
	// last trades from the exchange's snapshot, which is never older than this.
	TradesDuplicateWindow = 2 * time.Minute
)

// StreamConfigs returns the configuration of the market data streams.
func StreamConfigs() []jetstream.StreamConfig {
	return []jetstream.StreamConfig{
		{
			Name:        BooksStream,
			Description: "Latest order book per exchange and symbol (arbscanner.marketdata.v1.OrderBook)",
			Subjects:    []string{"md.book.>"},
			// Each OrderBook is complete, so only the latest per subject is worth keeping.
			// A consumer that starts reads it at once instead of waiting for the next publish.
			MaxMsgsPerSubject: 1,
			Discard:           jetstream.DiscardOld,
			// Books are rebuilt within milliseconds of a restart; there is nothing to persist.
			Storage:     jetstream.MemoryStorage,
			AllowDirect: true,
		},
		{
			Name:        TradesStream,
			Description: "Public trades (arbscanner.marketdata.v1.Trade)",
			Subjects:    []string{"md.trade.>"},
			MaxAge:      TradesMaxAge,
			Duplicates:  TradesDuplicateWindow,
			Discard:     jetstream.DiscardOld,
			Storage:     jetstream.FileStorage,
		},
	}
}

// StreamManager creates streams. *jetstream.JetStream implements it.
type StreamManager interface {
	CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error)
}

// EnsureStreams creates the market data streams, or updates them to the current settings.
func EnsureStreams(ctx context.Context, sm StreamManager) error {
	for _, cfg := range StreamConfigs() {
		if _, err := sm.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("create or update stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}

// MsgPublisher publishes one message and waits for JetStream's ack. *jetstream.JetStream
// implements it.
type MsgPublisher interface {
	PublishMsg(ctx context.Context, msg *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// JetStream is the Publisher for NATS JetStream.
type JetStream struct {
	js MsgPublisher
}

var _ Publisher = (*JetStream)(nil)

// NewJetStream returns a Publisher that sends through js.
func NewJetStream(js MsgPublisher) *JetStream { return &JetStream{js: js} }

// PublishBook implements Publisher.
func (p *JetStream) PublishBook(ctx context.Context, b *marketdatav1.OrderBook) error {
	subject, err := Subject("book", b.GetExchange(), b.GetSymbol())
	if err != nil {
		return err
	}
	return p.publish(ctx, subject, b)
}

// PublishTrade implements Publisher. The message id lets JetStream drop a trade published
// twice, as happens when a reconnect replays the exchange's recent trades.
func (p *JetStream) PublishTrade(ctx context.Context, t *marketdatav1.Trade) error {
	subject, err := Subject("trade", t.GetExchange(), t.GetSymbol())
	if err != nil {
		return err
	}
	if t.GetTradeId() == "" {
		return errors.New("publish trade: empty trade id")
	}
	id := t.GetExchange() + "." + t.GetSymbol() + "." + t.GetTradeId()
	return p.publish(ctx, subject, t, jetstream.WithMsgID(id))
}

func (p *JetStream) publish(ctx context.Context, subject string, m proto.Message, opts ...jetstream.PublishOpt) error {
	data, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal for %s: %w", subject, err)
	}
	if _, err := p.js.PublishMsg(ctx, &nats.Msg{Subject: subject, Data: data}, opts...); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

// Subject returns md.<kind>.<exchange>.<symbol>, rejecting tokens that NATS reserves.
func Subject(kind, exchange, symbol string) (string, error) {
	for _, tok := range []string{exchange, symbol} {
		if tok == "" || strings.ContainsAny(tok, ".*> \t\r\n") {
			return "", fmt.Errorf("invalid subject token %q", tok)
		}
	}
	return "md." + kind + "." + exchange + "." + symbol, nil
}
