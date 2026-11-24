// Package metrics records the ingestor's Prometheus metrics. Metrics implements the interfaces
// its consumers define, pipeline.Metrics and exchange.ReconnectObserver, so they never see
// Prometheus.
package metrics

import (
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/exchange"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/pipeline"
)

// Metrics holds the ingestor's collectors, registered on the Registerer given to New.
type Metrics struct {
	updates         *prometheus.CounterVec
	booksPublished  *prometheus.CounterVec
	trades          *prometheus.CounterVec
	resyncs         *prometheus.CounterVec
	reconnects      *prometheus.CounterVec
	bookLevels      *prometheus.GaugeVec
	publishDuration *prometheus.HistogramVec
	publishErrors   *prometheus.CounterVec
}

var _ pipeline.Metrics = (*Metrics)(nil)

// New creates the collectors and registers them on reg.
func New(reg prometheus.Registerer) (*Metrics, error) {
	book := []string{"exchange", "symbol"}
	m := &Metrics{
		updates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "updates_total",
			Help: "In-sync book updates received from the exchanges, before the throttle.",
		}, book),
		booksPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "books_published_total",
			Help: "OrderBooks published. Compare with updates_total to see what the throttle saves.",
		}, book),
		trades: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trades_total",
			Help: "Trades received from the exchanges.",
		}, book),
		resyncs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "resyncs_total",
			Help: "Books taken out of sync, by reason: checksum, sequence, protocol or reconnect.",
		}, []string{"exchange", "symbol", "reason"}),
		reconnects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reconnects_total",
			Help: "Exchange connections lost or dials failed, each followed by a reconnect.",
		}, []string{"exchange"}),
		bookLevels: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "book_levels",
			Help: "Price levels in the ingestor's local book, which can be deeper than what is published.",
		}, []string{"exchange", "symbol", "side"}),
		publishDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "publish_duration_seconds",
			Help: "Time from publishing a message to JetStream's ack, by kind: book or trade.",
			// 100µs to about 1.6s: a local JetStream acks in well under a millisecond.
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 15),
		}, []string{"kind"}),
		publishErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "publish_errors_total",
			Help: "Publishes that failed, by kind: book or trade.",
		}, []string{"kind"}),
	}
	err := errors.Join(
		reg.Register(m.updates), reg.Register(m.booksPublished), reg.Register(m.trades),
		reg.Register(m.resyncs), reg.Register(m.reconnects), reg.Register(m.bookLevels),
		reg.Register(m.publishDuration), reg.Register(m.publishErrors),
	)
	if err != nil {
		return nil, err
	}
	// Show both kinds from the start, so a rate() over them works before the first failure.
	for _, k := range []pipeline.Kind{pipeline.KindBook, pipeline.KindTrade} {
		m.publishErrors.WithLabelValues(string(k))
	}
	return m, nil
}

// BookUpdated implements pipeline.Metrics.
func (m *Metrics) BookUpdated(ex, symbol string, bidLevels, askLevels int) {
	m.updates.WithLabelValues(ex, symbol).Inc()
	m.bookLevels.WithLabelValues(ex, symbol, "bid").Set(float64(bidLevels))
	m.bookLevels.WithLabelValues(ex, symbol, "ask").Set(float64(askLevels))
}

// BookReset implements pipeline.Metrics. An out-of-sync book is empty until its next snapshot.
func (m *Metrics) BookReset(ex, symbol string, reason exchange.ResetReason) {
	m.resyncs.WithLabelValues(ex, symbol, string(reason)).Inc()
	m.bookLevels.WithLabelValues(ex, symbol, "bid").Set(0)
	m.bookLevels.WithLabelValues(ex, symbol, "ask").Set(0)
}

// TradeReceived implements pipeline.Metrics.
func (m *Metrics) TradeReceived(ex, symbol string) {
	m.trades.WithLabelValues(ex, symbol).Inc()
}

// Published implements pipeline.Metrics.
func (m *Metrics) Published(kind pipeline.Kind, ex, symbol string, took time.Duration, err error) {
	m.publishDuration.WithLabelValues(string(kind)).Observe(took.Seconds())
	switch {
	case err != nil:
		m.publishErrors.WithLabelValues(string(kind)).Inc()
	case kind == pipeline.KindBook:
		m.booksPublished.WithLabelValues(ex, symbol).Inc()
	}
}

// Reconnects returns the ReconnectObserver for one exchange's Reconnector.
func (m *Metrics) Reconnects(ex string) exchange.ReconnectObserver {
	return reconnectCounter{m.reconnects.WithLabelValues(ex)} // the series exists from the start, at 0
}

type reconnectCounter struct{ c prometheus.Counter }

func (r reconnectCounter) Reconnecting() { r.c.Inc() }
