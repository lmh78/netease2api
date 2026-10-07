package netease

import (
	"encoding/base64"
	"encoding/binary"
)

// Adapted from nethard-core src/crypto/index.ts sign, using index 3 / 6 rounds.
// Source: https://github.com/nethard-project/nethard-core-channel
// Only the selected PE login signature parameters are included.
func CountSignBase64(message string) string {
	data := []byte(message)
	for len(data)%4 != 0 {
		data = append(data, '0')
	}
	words := make([]uint32, 0, len(data)/4+64)
	for i := 0; i < len(data); i += 4 {
		words = append(words, binary.BigEndian.Uint32(data[i:i+4]))
	}
	for len(words)%64 != 0 {
		words = append(words, 0xabcde987)
	}
	a, b, c, d := uint32(0x67452301), uint32(0xefcdab89), uint32(0x98badcfe), uint32(0x10325476)
	for round := 0; round < 6; round++ {
		for j := 0; j < len(words); j += 4 {
			m := words[j]
			nextA := b + ((a + m + 0x289b7ec6 + (d&^b | c&b)) << 3)
			nextB := b + ((nextA + m + 0xeaa127fa + (d&b | c&^d)) << 8)
			nextC := nextB + ((nextA + m + 0xd4ef3085 + (d ^ c ^ nextB)) << 11)
			nextD := nextB + ((nextA + m + 0x04881d05 + (nextC ^ (nextB | d))) << 5)
			a, b, c, d = nextA, nextB, nextC, nextD
		}
	}
	var digest [16]byte
	binary.LittleEndian.PutUint32(digest[0:4], a)
	binary.LittleEndian.PutUint32(digest[4:8], b)
	binary.LittleEndian.PutUint32(digest[8:12], c)
	binary.LittleEndian.PutUint32(digest[12:16], d)
	return base64.StdEncoding.EncodeToString(digest[:])
}
