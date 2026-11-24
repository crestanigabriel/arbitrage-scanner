package kraken

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Wire types of Kraken WS v2 (https://docs.kraken.com/api/docs/websocket-v2/book and
// .../trade). They stay inside this package; the adapter emits exchange events.

// envelope is the union of the top-level fields of every message Kraken sends.
type envelope struct {
	// Channel messages: book, trade, heartbeat, status.
	Channel string          `json:"channel"`
	Type    string          `json:"type"` // "snapshot" or "update"
	Data    json.RawMessage `json:"data"`

	// Method responses: subscribe and unsubscribe acks, pong.
	Method  string     `json:"method"`
	Success *bool      `json:"success"`
	Error   string     `json:"error"`
	Symbol  string     `json:"symbol"` // set on failed acks, which have no result
	Result  *ackResult `json:"result"`
}

type ackResult struct {
	Channel string `json:"channel"`
	Symbol  string `json:"symbol"`
}

type bookData struct {
	Symbol    string      `json:"symbol"`
	Bids      []wireLevel `json:"bids"`
	Asks      []wireLevel `json:"asks"`
	Checksum  uint32      `json:"checksum"`
	Timestamp time.Time   `json:"timestamp"`
}

type wireLevel struct {
	Price number `json:"price"`
	Qty   number `json:"qty"`
}

type tradeData struct {
	Symbol    string      `json:"symbol"`
	Side      string      `json:"side"` // the taker's side: "buy" or "sell"
	Price     number      `json:"price"`
	Qty       number      `json:"qty"`
	TradeID   json.Number `json:"trade_id"`
	Timestamp time.Time   `json:"timestamp"`
}

// number holds the literal text of a JSON number, so the exchange's exact digits survive for
// the checksum and for publishing. Decoding into float64 would lose them: 0.00010000 would
// come back as 0.0001. Kraken sends bare numbers, but a quoted string is accepted too, as the
// docs show both forms. The text is validated as a decimal when it is used.
type number string

var errNullNumber = errors.New("kraken: null number")

func (n *number) UnmarshalJSON(b []byte) error {
	switch {
	case bytes.Equal(b, []byte("null")):
		return errNullNumber
	case len(b) > 0 && b[0] == '"':
		s, err := strconv.Unquote(string(b))
		if err != nil {
			return err
		}
		*n = number(s)
	default:
		*n = number(b)
	}
	return nil
}

// request is a subscribe or unsubscribe message.
type request struct {
	Method string `json:"method"`
	Params params `json:"params"`
}

type params struct {
	Channel  string   `json:"channel"`
	Symbol   []string `json:"symbol"`
	Depth    int      `json:"depth,omitempty"`
	Snapshot *bool    `json:"snapshot,omitempty"`
}
