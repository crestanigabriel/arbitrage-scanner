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
