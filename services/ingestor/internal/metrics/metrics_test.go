package metrics_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/metrics"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/pipeline"
)

func newMetrics(t *testing.T) (*metrics.Metrics, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := metrics.New(reg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return m, reg
}

func TestCounters(t *testing.T) {
	m, reg := newMetrics(t)
	m.BookUpdated("coinbase", "BTC-USD", 11417, 25535)
	m.BookUpdated("coinbase", "BTC-USD", 11418, 25535)
	m.Published(pipeline.KindBook, "coinbase", "BTC-USD", 2*time.Millisecond, nil)
	m.Published(pipeline.KindBook, "coinbase", "BTC-USD", time.Millisecond, errors.New("timeout"))
	m.TradeReceived("kraken", "BTC-USD")
	m.Published(pipeline.KindTrade, "kraken", "BTC-USD", time.Millisecond, nil)
	m.BookReset("kraken", "BTC-USD", exchange.ResetChecksum)
	m.Reconnects("kraken").Reconnecting()
	_ = m.Reconnects("coinbase") // present at 0 from the start

	want := `
# HELP book_levels Price levels in the ingestor's local book, which can be deeper than what is published.
# TYPE book_levels gauge
book_levels{exchange="coinbase",side="ask",symbol="BTC-USD"} 25535
book_levels{exchange="coinbase",side="bid",symbol="BTC-USD"} 11418
book_levels{exchange="kraken",side="ask",symbol="BTC-USD"} 0
book_levels{exchange="kraken",side="bid",symbol="BTC-USD"} 0
# HELP books_published_total OrderBooks published. Compare with updates_total to see what the throttle saves.
# TYPE books_published_total counter
books_published_total{exchange="coinbase",symbol="BTC-USD"} 1
# HELP publish_errors_total Publishes that failed, by kind: book or trade.
# TYPE publish_errors_total counter
publish_errors_total{kind="book"} 1
publish_errors_total{kind="trade"} 0
# HELP reconnects_total Exchange connections lost or dials failed, each followed by a reconnect.
# TYPE reconnects_total counter
reconnects_total{exchange="coinbase"} 0
reconnects_total{exchange="kraken"} 1
# HELP resyncs_total Books taken out of sync, by reason: checksum, sequence, protocol or reconnect.
# TYPE resyncs_total counter
resyncs_total{exchange="kraken",reason="checksum",symbol="BTC-USD"} 1
# HELP trades_total Trades received from the exchanges.
# TYPE trades_total counter
trades_total{exchange="kraken",symbol="BTC-USD"} 1
# HELP updates_total In-sync book updates received from the exchanges, before the throttle.
# TYPE updates_total counter
updates_total{exchange="coinbase",symbol="BTC-USD"} 2
`
	names := []string{
		"book_levels", "books_published_total", "publish_errors_total", "reconnects_total",
		"resyncs_total", "trades_total", "updates_total",
	}
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), names...); err != nil {
		t.Error(err)
	}
}

func TestPublishDuration(t *testing.T) {
	m, reg := newMetrics(t)
	m.Published(pipeline.KindBook, "kraken", "BTC-USD", 300*time.Microsecond, nil)
	m.Published(pipeline.KindTrade, "kraken", "BTC-USD", time.Millisecond, errors.New("x"))
	if n := testutil.CollectAndCount(reg, "publish_duration_seconds"); n != 2 {
		t.Errorf("publish_duration_seconds has %d series, want one per kind (2)", n)
	}
}

func TestRegistersOnce(t *testing.T) {
	_, reg := newMetrics(t)
	if _, err := metrics.New(reg); err == nil {
		t.Error("New() on the same registry twice succeeded, want a duplicate registration error")
	}
}
