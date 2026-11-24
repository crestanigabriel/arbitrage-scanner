package kraken

import "hash/crc32"

// crc is an independent, string-based statement of Kraken's checksum for tests: CRC32 of the
// asks then the bids, each price and quantity without "." and leading zeros.
func crc(asks, bids [][2]string) uint32 {
	var s []byte
	for _, side := range [][][2]string{asks, bids} {
		for _, l := range side {
			for _, v := range l {
				s = appendDigits(s, v)
			}
		}
	}
	return crc32.ChecksumIEEE(s)
}

// Checksum exposes crc to the external test package.
var Checksum = crc
