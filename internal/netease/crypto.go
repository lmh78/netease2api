package netease

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
)

const (
	httpBlockSize     = 16
	httpVersionNibble = byte(0x04)
	dynamicTokenSalt  = "0eGsBkhl"
)

// Adapted from nethard-core src/crypto/index.ts (2026-10-07).
// Source: https://github.com/nethard-project/nethard-core-channel
// DynamicToken signs the exact JSON body and path of fox HTTP requests.
func DynamicToken(path, body, token string) string {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(token) == "" {
		return ""
	}
	h1 := md5Hex([]byte(token))
	h2 := md5Hex([]byte(h1 + body + dynamicTokenSalt + path))
	raw := []byte(h2)
	xored := make([]byte, len(raw))
	for i := range raw {
		rotated := (raw[i] << 6) | (raw[(i+1)%len(raw)] >> 2)
		xored[i] = rotated ^ raw[i]
	}
	encoded := base64.StdEncoding.EncodeToString(xored)
	encoded = strings.NewReplacer("/", "o", "+", "m").Replace(encoded)
	return encoded[:16] + "1"
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func encryptHTTPRequest(plain []byte, random io.Reader) (string, error) {
	if random == nil {
		random = cryptorand.Reader
	}
	selector, err := cryptorand.Int(random, big.NewInt(14))
	if err != nil {
		return "", fmt.Errorf("read AES key selector: %w", err)
	}
	keyIndex := int(selector.Int64())
	iv := make([]byte, httpBlockSize)
	if _, err := io.ReadFull(random, iv); err != nil {
		return "", fmt.Errorf("read AES IV: %w", err)
	}
	rand16, err := randomASCII16(random)
	if err != nil {
		return "", err
	}
	return encryptHTTPRequestFixed(plain, iv, keyIndex, rand16)
}

func encryptHTTPRequestFixed(plain, iv []byte, keyIndex int, rand16 []byte) (string, error) {
	if len(iv) != httpBlockSize {
		return "", fmt.Errorf("AES IV length is %d, want 16", len(iv))
	}
	if keyIndex < 0 || keyIndex >= len(effectiveHTTPKeys) {
		return "", fmt.Errorf("AES key index %d out of range", keyIndex)
	}
	if len(rand16) != 16 {
		return "", fmt.Errorf("random suffix length is %d, want 16", len(rand16))
	}
	full := append(make([]byte, 0, len(plain)+33), plain...)
	full = append(full, '\n')
	full = append(full, rand16...)
	full = append(full, make([]byte, httpBlockSize-len(full)%httpBlockSize)...)
	block, err := aes.NewCipher(effectiveHTTPKeys[keyIndex][:])
	if err != nil {
		return "", err
	}
	ciphertext := make([]byte, len(full))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, full)
	wire := make([]byte, 0, len(iv)+len(ciphertext)+1)
	wire = append(wire, iv...)
	wire = append(wire, ciphertext...)
	encoded := hex.EncodeToString(wire)
	return encoded + fmt.Sprintf("%x%x", keyIndex, httpVersionNibble), nil
}

func decryptHTTPResponseHex(response string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(response))
	if err != nil {
		return "", fmt.Errorf("decode encrypted response hex: %w", err)
	}
	plain, err := decryptHTTPResponse(raw)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func decryptHTTPResponse(raw []byte) ([]byte, error) {
	if len(raw) < 16+16+1 {
		return nil, fmt.Errorf("encrypted response is too short: %d", len(raw))
	}
	iv, keyByte := raw[:16], raw[len(raw)-1]
	// The source supports V4 and V12 with the same key pool. These are PE
	// response frame versions, not additional login workflows.
	if version := keyByte & 0x0f; version != 0x04 && version != 0x0c {
		return nil, fmt.Errorf("unsupported PE response version marker 0x%x", version)
	}
	ciphertext := raw[16 : len(raw)-1]
	if len(ciphertext)%httpBlockSize != 0 {
		return nil, fmt.Errorf("encrypted response ciphertext length %d is not block aligned", len(ciphertext))
	}
	keyIndex := int(keyByte >> 4)
	if keyIndex >= len(effectiveHTTPKeys) {
		return nil, fmt.Errorf("encrypted response key index %d out of range", keyIndex)
	}
	block, err := aes.NewCipher(effectiveHTTPKeys[keyIndex][:])
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)
	return bytes.TrimRight(plain, "\x00"), nil
}

func randomASCII16(random io.Reader) ([]byte, error) {
	// Match randomString(16), including its exclusive upper bound of 61.
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ123456789"
	out := make([]byte, 16)
	for i := range out {
		index, err := cryptorand.Int(random, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return nil, fmt.Errorf("read random suffix: %w", err)
		}
		out[i] = alphabet[index.Int64()]
	}
	return out, nil
}

// extractLeadingJSONObject removes the random filler appended after encrypted
// JSON responses. Braces inside quoted strings are ignored.
func extractLeadingJSONObject(s string) (string, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", errors.New("decrypted response has no JSON object")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", errors.New("decrypted response contains an unbalanced JSON object")
}
