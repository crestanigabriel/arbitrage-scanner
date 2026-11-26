package config_test

import (
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/config"
)

func env(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := kv[k]
		return v, ok
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := config.Load(env(nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.Config{
		BookDepth:     50,
		BookThrottle:  50 * time.Millisecond,
		BookKeepalive: time.Second,
		Symbols:       []string{"BTC-USD"},
		NATSURL:       "nats://localhost:4222",
		KrakenWSURL:   "wss://ws.kraken.com/v2",
		CoinbaseWSURL: "wss://advanced-trade-ws.coinbase.com",
		HTTPAddr:      ":8080",
		LogLevel:      slog.LevelInfo,
	}
	if !equal(cfg, want) {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestOverrides(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{
		"BOOK_DEPTH":        "100",
		"BOOK_THROTTLE_MS":  "0",
		"BOOK_KEEPALIVE_MS": "0",
		"SYMBOLS":           " BTC-USD, ETH-USD ",
		"NATS_URL":          "nats://nats:4222",
		"KRAKEN_WS_URL":     "ws://localhost:9999",
		"COINBASE_WS_URL":   "ws://localhost:9998",
		"HTTP_ADDR":         "127.0.0.1:9100",
		"LOG_LEVEL":         "DEBUG",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.Config{
		BookDepth:     100,
		BookThrottle:  0,
		BookKeepalive: 0,
		Symbols:       []string{"BTC-USD", "ETH-USD"},
		NATSURL:       "nats://nats:4222",
		KrakenWSURL:   "ws://localhost:9999",
		CoinbaseWSURL: "ws://localhost:9998",
		HTTPAddr:      "127.0.0.1:9100",
		LogLevel:      slog.LevelDebug,
	}
	if !equal(cfg, want) {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestEmptyMeansDefault(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{"BOOK_DEPTH": "", "SYMBOLS": "  "}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BookDepth != 50 || !slices.Equal(cfg.Symbols, []string{"BTC-USD"}) {
		t.Errorf("Load() = %+v, want the defaults", cfg)
	}
}

func TestInvalid(t *testing.T) {
	tests := []struct {
		env  map[string]string
		want []string // substrings of the error
	}{
		{map[string]string{"BOOK_DEPTH": "0"}, []string{"BOOK_DEPTH: 0 is out of range 1-100"}},
		{map[string]string{"BOOK_DEPTH": "101"}, []string{"BOOK_DEPTH: 101 is out of range"}},
		{map[string]string{"BOOK_DEPTH": "fifty"}, []string{"BOOK_DEPTH: not an integer"}},
		{map[string]string{"BOOK_THROTTLE_MS": "-1"}, []string{"BOOK_THROTTLE_MS: -1 is negative"}},
		{map[string]string{"BOOK_KEEPALIVE_MS": "-1"}, []string{"BOOK_KEEPALIVE_MS: -1 is negative"}},
		{map[string]string{"BOOK_KEEPALIVE_MS": "1s"}, []string{"BOOK_KEEPALIVE_MS: not an integer"}},
		{
			map[string]string{"BOOK_THROTTLE_MS": "500", "BOOK_KEEPALIVE_MS": "100"},
			[]string{"BOOK_KEEPALIVE_MS: 100 is shorter than BOOK_THROTTLE_MS (500)"},
		},
		{map[string]string{"SYMBOLS": "BTC/USD"}, []string{`"BTC/USD" is not a canonical`}},
		{map[string]string{"SYMBOLS": "btc-usd"}, []string{`"btc-usd" is not a canonical`}},
		{map[string]string{"SYMBOLS": "BTC-USD,"}, []string{`"" is not a canonical`}},
		{map[string]string{"SYMBOLS": "BTC-USD,BTC-USD"}, []string{`"BTC-USD" is listed twice`}},
		{map[string]string{"LOG_LEVEL": "loud"}, []string{"LOG_LEVEL"}},
		{map[string]string{"HTTP_ADDR": "8080"}, []string{"HTTP_ADDR"}},
		{map[string]string{"HTTP_ADDR": ":http"}, []string{`HTTP_ADDR: "http" is not a port number`}},
		{map[string]string{"HTTP_ADDR": ":0"}, []string{"HTTP_ADDR"}},
		{
			// Every problem is reported at once.
			map[string]string{"BOOK_DEPTH": "0", "BOOK_THROTTLE_MS": "x", "SYMBOLS": "BTC.USD"},
			[]string{"BOOK_DEPTH", "BOOK_THROTTLE_MS", "SYMBOLS"},
		},
	}
	for _, tt := range tests {
		_, err := config.Load(env(tt.env))
		if err == nil {
			t.Errorf("Load(%v) succeeded, want an error", tt.env)
			continue
		}
		for _, w := range tt.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("Load(%v) error %q does not contain %q", tt.env, err, w)
			}
		}
	}
}

func equal(a, b config.Config) bool {
	return a.BookDepth == b.BookDepth && a.BookThrottle == b.BookThrottle &&
		a.BookKeepalive == b.BookKeepalive &&
		slices.Equal(a.Symbols, b.Symbols) && a.NATSURL == b.NATSURL &&
		a.KrakenWSURL == b.KrakenWSURL && a.CoinbaseWSURL == b.CoinbaseWSURL &&
		a.HTTPAddr == b.HTTPAddr && a.LogLevel == b.LogLevel
}

// TestEnvFile checks deploy/ingestor.env, the file `make run-ingestor` loads: it must list
// exactly the variables Load reads (a misspelt key would be silently ignored), and its values
// must load. The values themselves are free to edit.
func TestEnvFile(t *testing.T) {
	root, err := os.OpenRoot(filepath.Join("..", "..", "..", "..", "deploy"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	b, err := root.ReadFile("ingestor.env")
	if err != nil {
		t.Fatal(err)
	}

	values := make(map[string]string)
	for i, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			t.Fatalf("ingestor.env line %d is not KEY=VALUE: %q", i+1, line)
		}
		if _, dup := values[key]; dup {
			t.Errorf("ingestor.env sets %s twice", key)
		}
		values[key] = value
	}

	keys := slices.Sorted(maps.Keys(values))
	want := slices.Sorted(slices.Values(config.Variables))
	if !slices.Equal(keys, want) {
		t.Errorf("ingestor.env sets %v, want exactly %v", keys, want)
	}
	if _, err := config.Load(env(values)); err != nil {
		t.Errorf("ingestor.env does not load: %v", err)
	}
}

func TestKeepaliveEqualToThrottle(t *testing.T) {
	cfg, err := config.Load(env(map[string]string{"BOOK_THROTTLE_MS": "500", "BOOK_KEEPALIVE_MS": "500"}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.BookKeepalive != 500*time.Millisecond {
		t.Errorf("BookKeepalive = %v, want 500ms", cfg.BookKeepalive)
	}
}
