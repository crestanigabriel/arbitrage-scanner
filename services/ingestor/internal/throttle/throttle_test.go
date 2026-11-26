package throttle_test

import (
	"slices"
	"testing"
	"time"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/clock/clocktest"
	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/throttle"
)

const (
	interval  = 50 * time.Millisecond
	keepalive = time.Second
)

var start = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// step is one action in a scenario: advance the clock, then either offer a value or collect
// what is due. want lists what must be published at that step.
type step struct {
	advance time.Duration
	key     string
	offer   string // empty: call Due instead of Offer
	drop    bool
	want    []string
}

func run(t *testing.T, interval, keepalive time.Duration, steps []step) {
	t.Helper()
	c := clocktest.NewFake(start)
	th := throttle.New[string, string](c, interval, keepalive)
	for i, s := range steps {
		c.Advance(s.advance)
		var got []string
		switch {
		case s.drop:
			th.Drop(s.key)
		case s.offer != "":
			if v, ok := th.Offer(s.key, s.offer); ok {
				got = []string{v}
			}
		default:
			got = th.Due()
			slices.Sort(got) // equal deadlines on different keys come in no fixed order
		}
		if !slices.Equal(got, s.want) {
			t.Fatalf("step %d (%+v): published %v, want %v", i, s, got, s.want)
		}
	}
}

func TestThrottle(t *testing.T) {
	tests := []struct {
		name      string
		interval  time.Duration
		keepalive time.Duration
		steps     []step
	}{
		{
			name:     "first value is published at once",
			interval: interval,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
			},
		},
		{
			name:     "values inside the interval fold into one trailing publish of the latest",
			interval: interval,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 10 * time.Millisecond, key: "a", offer: "a2"},
				{advance: 10 * time.Millisecond, key: "a", offer: "a3"},
				{advance: 10 * time.Millisecond, key: "a", offer: "a4"},
				{advance: 19 * time.Millisecond}, // 49ms: not due yet
				{advance: time.Millisecond, want: []string{"a4"}},
				{advance: time.Second}, // nothing left
			},
		},
		{
			name:     "a value after a quiet interval is a new leading edge",
			interval: interval,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: interval, key: "a", offer: "a2", want: []string{"a2"}},
				{advance: time.Second, key: "a", offer: "a3", want: []string{"a3"}},
			},
		},
		{
			name:     "a trailing publish starts the next interval",
			interval: interval,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 10 * time.Millisecond, key: "a", offer: "a2"},
				{advance: 40 * time.Millisecond, want: []string{"a2"}},
				{advance: 10 * time.Millisecond, key: "a", offer: "a3"}, // held: 10ms after a2
				{advance: 40 * time.Millisecond, want: []string{"a3"}},
			},
		},
		{
			name:     "a leading edge supersedes a value held from a missed deadline",
			interval: interval,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 10 * time.Millisecond, key: "a", offer: "a2"},
				{advance: 100 * time.Millisecond, key: "a", offer: "a3", want: []string{"a3"}},
				{}, // a2 is never published after a3
			},
		},
		{
			name:     "drop discards the held value",
			interval: interval,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 10 * time.Millisecond, key: "a", offer: "a2"},
				{key: "a", drop: true},
				{advance: time.Second},
			},
		},
		{
			name:     "drop of an unknown key is a no-op",
			interval: interval,
			steps: []step{
				{key: "x", drop: true},
				{key: "x", offer: "x1", want: []string{"x1"}},
			},
		},
		{
			name:     "keys are throttled independently",
			interval: interval,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 10 * time.Millisecond, key: "b", offer: "b1", want: []string{"b1"}},
				{advance: 10 * time.Millisecond, key: "a", offer: "a2"},
				{advance: 10 * time.Millisecond, key: "b", offer: "b2"},
				{advance: 20 * time.Millisecond, want: []string{"a2"}}, // 50ms: only a's interval ended
				{advance: 10 * time.Millisecond, want: []string{"b2"}}, // 60ms
			},
		},
		{
			name:     "interval 0 publishes every value",
			interval: 0,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{key: "a", offer: "a2", want: []string{"a2"}},
				{key: "a", offer: "a3", want: []string{"a3"}},
				{},
			},
		},
		{
			name:      "keepalive publishes the last value again after a quiet keepalive",
			interval:  interval,
			keepalive: keepalive,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: keepalive - time.Millisecond},
				{advance: time.Millisecond, want: []string{"a1"}},
				{advance: keepalive, want: []string{"a1"}}, // and again, one keepalive later
			},
		},
		{
			name:      "keepalive starts from the last publish, trailing edges included",
			interval:  interval,
			keepalive: keepalive,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 10 * time.Millisecond, key: "a", offer: "a2"},
				{advance: 40 * time.Millisecond, want: []string{"a2"}}, // trailing edge at 50ms
				{advance: keepalive - time.Millisecond},
				{advance: time.Millisecond, want: []string{"a2"}},
			},
		},
		{
			name:      "an offer restarts the keepalive",
			interval:  interval,
			keepalive: keepalive,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 600 * time.Millisecond, key: "a", offer: "a2", want: []string{"a2"}},
				{advance: 600 * time.Millisecond}, // 1.2s: only 600ms since a2
				{advance: 400 * time.Millisecond, want: []string{"a2"}},
			},
		},
		{
			name:      "a held value is published at its interval, not replaced by the keepalive",
			interval:  interval,
			keepalive: keepalive,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 10 * time.Millisecond, key: "a", offer: "a2"},
				{advance: 2 * keepalive, want: []string{"a2"}}, // a late Due: a2 once, never a1
				{},
			},
		},
		{
			name:      "drop stops the keepalive until the next offer",
			interval:  interval,
			keepalive: keepalive,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 10 * time.Millisecond, key: "a", drop: true},
				{advance: 5 * keepalive},
				{key: "a", offer: "a2", want: []string{"a2"}},
				{advance: keepalive, want: []string{"a2"}},
			},
		},
		{
			name:     "keepalive 0 never publishes a value twice",
			interval: interval,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: time.Hour},
			},
		},
		{
			name:      "keepalive with interval 0",
			interval:  0,
			keepalive: keepalive,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{key: "a", offer: "a2", want: []string{"a2"}},
				{advance: keepalive, want: []string{"a2"}},
			},
		},
		{
			name:      "keys keep alive independently",
			interval:  interval,
			keepalive: keepalive,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 300 * time.Millisecond, key: "b", offer: "b1", want: []string{"b1"}},
				{advance: 700 * time.Millisecond, want: []string{"a1"}},
				{advance: 300 * time.Millisecond, want: []string{"b1"}},
			},
		},
		{
			name:      "a keepalive shorter than the interval is raised to it",
			interval:  interval,
			keepalive: 10 * time.Millisecond,
			steps: []step{
				{key: "a", offer: "a1", want: []string{"a1"}},
				{advance: 49 * time.Millisecond},
				{advance: time.Millisecond, want: []string{"a1"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { run(t, tt.interval, tt.keepalive, tt.steps) })
	}
}

func TestNextDeadline(t *testing.T) {
	c := clocktest.NewFake(start)
	th := throttle.New[string, string](c, interval, 0)

	if _, ok := th.NextDeadline(); ok {
		t.Fatal("NextDeadline() ok on an empty throttle")
	}
	th.Offer("a", "a1") // published, nothing held
	if _, ok := th.NextDeadline(); ok {
		t.Fatal("NextDeadline() ok with nothing held")
	}

	c.Advance(10 * time.Millisecond)
	th.Offer("b", "b1")
	th.Offer("b", "b2") // held until 60ms
	c.Advance(10 * time.Millisecond)
	th.Offer("a", "a2") // held until 50ms

	d, ok := th.NextDeadline()
	if want := start.Add(interval); !ok || !d.Equal(want) {
		t.Errorf("NextDeadline() = %v, %v; want %v, true", d, ok, want)
	}

	th.Drop("a")
	d, ok = th.NextDeadline()
	if want := start.Add(10*time.Millisecond + interval); !ok || !d.Equal(want) {
		t.Errorf("after Drop, NextDeadline() = %v, %v; want %v, true", d, ok, want)
	}
}

func TestNextDeadlineKeepalive(t *testing.T) {
	c := clocktest.NewFake(start)
	th := throttle.New[string, string](c, interval, keepalive)

	th.Offer("a", "a1") // published: its keepalive ends at 1s
	d, ok := th.NextDeadline()
	if want := start.Add(keepalive); !ok || !d.Equal(want) {
		t.Errorf("NextDeadline() = %v, %v; want %v, true", d, ok, want)
	}

	c.Advance(10 * time.Millisecond)
	th.Offer("a", "a2") // held until 50ms, which comes before the keepalive
	d, ok = th.NextDeadline()
	if want := start.Add(interval); !ok || !d.Equal(want) {
		t.Errorf("with a held value, NextDeadline() = %v, %v; want %v, true", d, ok, want)
	}

	th.Drop("a")
	if d, ok := th.NextDeadline(); ok {
		t.Errorf("after Drop, NextDeadline() = %v, true; want ok false", d)
	}
}
