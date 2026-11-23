package main

import (
	"bytes"
	"testing"
)

func TestRunWritesGreeting(t *testing.T) {
	var out bytes.Buffer

	if err := run(&out); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	if got, want := out.String(), "ingestor: hello\n"; got != want {
		t.Errorf("run() output = %q, want %q", got, want)
	}
}
