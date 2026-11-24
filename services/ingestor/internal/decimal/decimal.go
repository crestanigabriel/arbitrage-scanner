// Package decimal holds exact decimal values as text, in the wire format of the market data
// contracts: ^[0-9]+(\.[0-9]+)?$ (no sign, no exponent, "." as the decimal point).
//
// The ingestor never does arithmetic on prices or quantities. It only orders them, tells zero
// apart, and passes the exchange's own digits on, so a value keeps the exact text it was parsed
// from.
package decimal

import (
	"fmt"
	"strings"
)

// Decimal is a non-negative exact decimal. The zero value is invalid; use Parse.
type Decimal struct {
	text     string // as parsed, for checksums and publishing
	intPart  string // integer digits without leading zeros; "" for zero
	fracPart string // fraction digits without trailing zeros; "" for none
}

// Parse validates s and returns it as a Decimal. It keeps s exactly as given.
func Parse(s string) (Decimal, error) {
	intPart, fracPart, hasPoint := strings.Cut(s, ".")
	if !allDigits(intPart) || (hasPoint && !allDigits(fracPart)) {
		return Decimal{}, fmt.Errorf("invalid decimal %q", s)
	}
	return Decimal{
		text:     s,
		intPart:  strings.TrimLeft(intPart, "0"),
		fracPart: strings.TrimRight(fracPart, "0"),
	}, nil
}

// MustParse is Parse for constants in tests and examples. It panics on invalid input.
func MustParse(s string) Decimal {
	d, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// String returns the text the value was parsed from.
func (d Decimal) String() string { return d.text }

// IsZero reports whether the value is zero, whatever its scale ("0", "0.00000000").
func (d Decimal) IsZero() bool { return d.intPart == "" && d.fracPart == "" }

// Cmp compares d and e by value, ignoring leading and trailing zeros. It returns -1 if d < e,
// 0 if they are equal, and +1 if d > e.
func (d Decimal) Cmp(e Decimal) int {
	if c := cmpInt(len(d.intPart), len(e.intPart)); c != 0 {
		return c
	}
	if c := strings.Compare(d.intPart, e.intPart); c != 0 {
		return c
	}
	// Without trailing zeros, fraction digits compare lexicographically: "4" < "45" < "5".
	return strings.Compare(d.fracPart, e.fracPart)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
