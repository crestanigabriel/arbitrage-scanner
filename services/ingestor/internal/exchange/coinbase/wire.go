package coinbase

import (
	"encoding/json"
	"time"
)

// Wire types of the Coinbase Advanced Trade WebSocket feed
// (https://docs.cdp.coinbase.com/coinbase-app/advanced-trade-apis/websocket/websocket-channels).
// They stay inside this package; the adapter emits exchange events. Shapes not covered by the
// docs (the subscriptions ack, errors) were taken from the live feed on 2026-11-24.

// envelope is the union of the top-level fields of every message Coinbase sends.
type envelope struct {
	// Channel messages: l2_data, market_trades, heartbeats, subscriptions.
	Channel     string          `json:"channel"`
	Timestamp   time.Time       `json:"timestamp"`
	SequenceNum *int64          `json:"sequence_num"` // per connection, across every channel
	Events      json.RawMessage `json:"events"`

	// Errors: {"type":"error","message":"..."}.
	Type    string `json:"type"`
	Message string `json:"message"`
}

// l2Event is one event of an l2_data message. A snapshot is the whole book in one message.
type l2Event struct {
	Type      string     `json:"type"` // "snapshot" or "update"
	ProductID string     `json:"product_id"`
	Updates   []l2Update `json:"updates"`
}

type l2Update struct {
	Side        string    `json:"side"` // "bid" or "offer"
	EventTime   time.Time `json:"event_time"`
	PriceLevel  string    `json:"price_level"`
	NewQuantity string    `json:"new_quantity"` // absolute; "0" removes the level
}

type tradesEvent struct {
	Type   string      `json:"type"` // "snapshot" (recent trades) or "update"
	Trades []wireTrade `json:"trades"`
}

type wireTrade struct {
	ProductID string    `json:"product_id"`
	TradeID   string    `json:"trade_id"`
	Price     string    `json:"price"`
	Size      string    `json:"size"` // base units: our quantity
	Side      string    `json:"side"` // the maker's side: "BUY" or "SELL"
	Time      time.Time `json:"time"`
}

// subscriptionsEvent is the ack Coinbase sends after each subscribe message. It lists every
// subscription of the connection so far, by channel. A product Coinbase does not know is left
// out, with no error.
type subscriptionsEvent struct {
	Subscriptions map[string][]string `json:"subscriptions"`
}

// request is a subscribe message. Coinbase accepts one channel per message.
type request struct {
	Type       string   `json:"type"`
	ProductIDs []string `json:"product_ids,omitempty"`
	Channel    string   `json:"channel"`
}
