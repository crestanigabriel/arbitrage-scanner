package decimal_test

import (
	"testing"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/decimal"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in     string
		valid  bool
		isZero bool
	}{
		{"0", true, true},
		{"0.00000000", true, true},
		{"000", true, true},
		{"1", true, false},
		{"59979.0", true, false},
		{"0.00010000", true, false},
		{"", false, false},
		{".5", false, false},
		{"5.", false, false},
		{"-1", false, false},
		{"+1", false, false},
		{"1e-8", false, false},
		{"1,000", false, false},
		{"1.2.3", false, false},
		{" 1", false, false},
		{"NaN", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			d, err := decimal.Parse(tt.in)
			if (err == nil) != tt.valid {
				t.Fatalf("Parse(%q) error = %v, want valid = %v", tt.in, err, tt.valid)
			}
			if !tt.valid {
				return
			}
			if d.String() != tt.in {
				t.Errorf("String() = %q, want the original text %q", d.String(), tt.in)
			}
			if d.IsZero() != tt.isZero {
				t.Errorf("IsZero() = %v, want %v", d.IsZero(), tt.isZero)
			}
		})
	}
}

func TestCmp(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.5", "1.50", 0},
		{"001.5", "1.5", 0},
		{"0", "0.000", 0},
		{"1", "2", -1},
		{"10", "9", 1},
		{"9.99", "10", -1},
		{"0.4", "0.45", -1},
		{"0.45", "0.5", -1},
		{"59989.7", "59989.4", 1},
		{"0.00000001", "0", 1},
		{"100000", "99999.99999999", 1},
	}
	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			a, b := decimal.MustParse(tt.a), decimal.MustParse(tt.b)
			if got := a.Cmp(b); got != tt.want {
				t.Errorf("Cmp(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			if got := b.Cmp(a); got != -tt.want {
				t.Errorf("Cmp(%s, %s) = %d, want %d", tt.b, tt.a, got, -tt.want)
			}
		})
	}
}
