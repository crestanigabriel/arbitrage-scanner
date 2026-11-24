package main

import (
	"bytes"
	"strings"
	"testing"
)

func env(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := kv[k]
		return v, ok
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	var logs bytes.Buffer
	err := run(t.Context(), env(map[string]string{"BOOK_DEPTH": "0", "SYMBOLS": "btc"}), &logs)
	if err == nil {
		t.Fatal("run() succeeded with an invalid configuration")
	}
	for _, want := range []string{"BOOK_DEPTH", "SYMBOLS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("run() error %q does not mention %s", err, want)
		}
	}
}

func TestRunFailsWithoutNATS(t *testing.T) {
	var logs bytes.Buffer
	// Port 1 on loopback: nothing listens there, so the connection is refused at once.
	err := run(t.Context(), env(map[string]string{"NATS_URL": "nats://127.0.0.1:1"}), &logs)
	if err == nil || !strings.Contains(err.Error(), "connect to nats") {
		t.Fatalf("run() = %v, want a nats connection error", err)
	}
	if !strings.Contains(logs.String(), `"msg":"starting"`) {
		t.Errorf("no JSON start log line in %q", logs.String())
	}
}

func TestProbeURL(t *testing.T) {
	tests := map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/healthz",
		"[::]:8080":      "http://127.0.0.1:8080/healthz",
		"10.0.0.5:9100":  "http://10.0.0.5:9100/healthz",
		"localhost:8080": "http://localhost:8080/healthz",
		"[::1]:8080":     "http://[::1]:8080/healthz",
	}
	for addr, want := range tests {
		if got := probeURL(addr); got != want {
			t.Errorf("probeURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestHealthcheckFailsWithNothingListening(t *testing.T) {
	// Port 1 on loopback: nothing listens there.
	if err := healthcheck(env(map[string]string{"HTTP_ADDR": "127.0.0.1:1"})); err == nil {
		t.Fatal("healthcheck() succeeded with no server")
	}
}
