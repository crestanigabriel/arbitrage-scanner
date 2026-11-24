package kraken

import (
	"hash/crc32"

	"github.com/crestanigabriel/arbitrage-scanner/services/ingestor/internal/book"
)

// checksumLevels is how many levels per side Kraken's checksum covers, whatever the depth.
const checksumLevels = 10

// checksum computes Kraken's book checksum (https://docs.kraken.com/api/docs/guides/spot-ws-book-v2):
// CRC32 (IEEE) over the top 10 asks, lowest first, then the top 10 bids, highest first. Each
// level adds its price and then its quantity, each with the "." removed and leading zeros
// stripped. The digits are the exchange's own text, trailing zeros included.
func checksum(b *book.Book) uint32 {
	bids, asks := b.Top(checksumLevels)
	buf := make([]byte, 0, 2*checksumLevels*32)
	for _, l := range asks {
		buf = appendLevel(buf, l)
	}
	for _, l := range bids {
		buf = appendLevel(buf, l)
	}
	return crc32.ChecksumIEEE(buf)
}

func appendLevel(buf []byte, l book.Level) []byte {
	buf = appendDigits(buf, l.Price.String())
	return appendDigits(buf, l.Quantity.String())
}

// appendDigits appends s without its decimal point and leading zeros: "0.00100000" → "100000".
func appendDigits(buf []byte, s string) []byte {
	leading := true
	for i := range len(s) {
		c := s[i]
		if c == '.' || (leading && c == '0') {
			continue
		}
		leading = false
		buf = append(buf, c)
	}
	return buf
}
