// Package config loads the ingestor's configuration from environment variables and validates
// it at startup.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Defaults and limits.
const (
	DefaultBookDepth     = 50
	MaxBookDepth         = 100 // the OrderBook contract limit
	DefaultThrottle      = 50 * time.Millisecond
	DefaultKeepalive     = time.Second
	DefaultSymbols       = "BTC-USD"
	DefaultNATSURL       = "nats://localhost:4222"
	DefaultKrakenWSURL   = "wss://ws.kraken.com/v2"
	DefaultCoinbaseWSURL = "wss://advanced-trade-ws.coinbase.com"
	DefaultHTTPAddr      = ":8080"
	DefaultLogLevel      = "info"
)

// Environment variables read by Load.
const (
	EnvSymbols         = "SYMBOLS"
	EnvBookDepth       = "BOOK_DEPTH"
	EnvBookThrottleMS  = "BOOK_THROTTLE_MS"
	EnvBookKeepaliveMS = "BOOK_KEEPALIVE_MS"
	EnvNATSURL         = "NATS_URL"
	EnvKrakenWSURL     = "KRAKEN_WS_URL"
	EnvCoinbaseWSURL   = "COINBASE_WS_URL"
	EnvHTTPAddr        = "HTTP_ADDR"
	EnvLogLevel        = "LOG_LEVEL"
)

// Variables lists every environment variable Load reads. deploy/ingestor.env documents each
// of them, and a test keeps the two in step.
var Variables = []string{
	EnvSymbols, EnvBookDepth, EnvBookThrottleMS, EnvBookKeepaliveMS, EnvNATSURL, EnvKrakenWSURL,
	EnvCoinbaseWSURL, EnvHTTPAddr, EnvLogLevel,
}

// Config is the validated configuration.
type Config struct {
	BookDepth     int           // BOOK_DEPTH: levels per side in each published OrderBook
	BookThrottle  time.Duration // BOOK_THROTTLE_MS: minimum interval between publishes of one book; 0 publishes every update
	BookKeepalive time.Duration // BOOK_KEEPALIVE_MS: publish an unchanged book again after this long; 0 never does
	Symbols       []string      // SYMBOLS: comma-separated canonical symbols, e.g. "BTC-USD,ETH-USD"
	NATSURL       string        // NATS_URL
	KrakenWSURL   string        // KRAKEN_WS_URL
	CoinbaseWSURL string        // COINBASE_WS_URL
	HTTPAddr      string        // HTTP_ADDR: listen address of /metrics and /healthz, e.g. ":8080"
	LogLevel      slog.Level    // LOG_LEVEL: debug, info, warn or error
}

// symbolPattern is a canonical BASE-QUOTE symbol: upper-case letters and digits, which are
// also valid NATS subject tokens.
var symbolPattern = regexp.MustCompile(`^[A-Z0-9]+-[A-Z0-9]+$`)

// Load reads the configuration through lookup (os.LookupEnv in production). Unset variables
// take their defaults. Every invalid variable is reported, not only the first.
func Load(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	var errs []error
	cfg := Config{
		NATSURL:       get(EnvNATSURL, DefaultNATSURL),
		KrakenWSURL:   get(EnvKrakenWSURL, DefaultKrakenWSURL),
		CoinbaseWSURL: get(EnvCoinbaseWSURL, DefaultCoinbaseWSURL),
		HTTPAddr:      get(EnvHTTPAddr, DefaultHTTPAddr),
	}
	if _, port, err := net.SplitHostPort(cfg.HTTPAddr); err != nil {
		errs = append(errs, fmt.Errorf("HTTP_ADDR: %w", err))
	} else if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		errs = append(errs, fmt.Errorf("HTTP_ADDR: %q is not a port number 1-65535", port))
	}

	depth, err := strconv.Atoi(get(EnvBookDepth, strconv.Itoa(DefaultBookDepth)))
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("BOOK_DEPTH: not an integer: %w", err))
	case depth < 1 || depth > MaxBookDepth:
		errs = append(errs, fmt.Errorf("BOOK_DEPTH: %d is out of range 1-%d", depth, MaxBookDepth))
	}
	cfg.BookDepth = depth

	ms, err := strconv.Atoi(get(EnvBookThrottleMS, strconv.FormatInt(DefaultThrottle.Milliseconds(), 10)))
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("BOOK_THROTTLE_MS: not an integer: %w", err))
	case ms < 0:
		errs = append(errs, fmt.Errorf("BOOK_THROTTLE_MS: %d is negative", ms))
	}
	cfg.BookThrottle = time.Duration(ms) * time.Millisecond

	ms, err = strconv.Atoi(get(EnvBookKeepaliveMS, strconv.FormatInt(DefaultKeepalive.Milliseconds(), 10)))
	cfg.BookKeepalive = time.Duration(ms) * time.Millisecond
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("BOOK_KEEPALIVE_MS: not an integer: %w", err))
	case ms < 0:
		errs = append(errs, fmt.Errorf("BOOK_KEEPALIVE_MS: %d is negative", ms))
	case ms > 0 && cfg.BookKeepalive < cfg.BookThrottle:
		errs = append(errs, fmt.Errorf("BOOK_KEEPALIVE_MS: %d is shorter than BOOK_THROTTLE_MS (%d): use 0 or at least that",
			ms, cfg.BookThrottle.Milliseconds()))
	}

	seen := make(map[string]bool)
	for s := range strings.SplitSeq(get(EnvSymbols, DefaultSymbols), ",") {
		s = strings.TrimSpace(s)
		switch {
		case !symbolPattern.MatchString(s):
			errs = append(errs, fmt.Errorf("SYMBOLS: %q is not a canonical BASE-QUOTE symbol such as BTC-USD", s))
		case seen[s]:
			errs = append(errs, fmt.Errorf("SYMBOLS: %q is listed twice", s))
		default:
			seen[s] = true
			cfg.Symbols = append(cfg.Symbols, s)
		}
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(get(EnvLogLevel, DefaultLogLevel))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}
