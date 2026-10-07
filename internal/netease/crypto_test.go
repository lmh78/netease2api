package netease

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"strings"
	"testing"
)

func TestDynamicTokenGolden(t *testing.T) {
	tests := []struct{ path, body, token, want string }{
		{"/a/b/c", "BODY", "TOKEN", "d2G7uuggmumvKyEo1"},
		{"/pe-authentication", "{\"hello\":\"world\"}", "deadbeefdeadbeefdeadbeefdeadbeef", "fCyuuoqueHj6ri911"},
	}
	for _, test := range tests {
		if got := DynamicToken(test.path, test.body, test.token); got != test.want {
			t.Errorf("DynamicToken(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestPERequestAndResponseRoundTrip(t *testing.T) {
	iv := []byte("0123456789abcdef")
	suffix := []byte("ABCDEFGHIJKLMNOP")
	wire, err := encryptHTTPRequestFixed([]byte("{\"ok\":true}"), iv, 5, suffix)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(wire, "54") {
		t.Fatalf("wire suffix = %q, want 54", wire[len(wire)-2:])
	}

	rawRequest, err := hex.DecodeString(wire[:len(wire)-2])
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(effectiveHTTPKeys[5][:])
	requestPlain := make([]byte, len(rawRequest)-16)
	cipher.NewCBCDecrypter(block, rawRequest[:16]).CryptBlocks(requestPlain, rawRequest[16:])
	if got, want := string(requestPlain[:len("{\"ok\":true}\nABCDEFGHIJKLMNOP")]), "{\"ok\":true}\nABCDEFGHIJKLMNOP"; got != want {
		t.Fatalf("request plaintext = %q, want %q", got, want)
	}

	responsePlain := []byte("{\"code\":0,\"entity\":{\"entity_id\":123,\"token\":\"fixture\"}}RANDOM-FILLER-16")
	responsePlain = append(responsePlain, make([]byte, (16-len(responsePlain)%16)%16)...)
	responseCipher := make([]byte, len(responsePlain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(responseCipher, responsePlain)
	responseRaw := append(append(append([]byte{}, iv...), responseCipher...), byte(5<<4|0x0c))
	got, err := decryptHTTPResponse(responseRaw)
	if err != nil {
		t.Fatal(err)
	}
	jsonText, err := extractLeadingJSONObject(string(got))
	if err != nil {
		t.Fatal(err)
	}
	if jsonText != "{\"code\":0,\"entity\":{\"entity_id\":123,\"token\":\"fixture\"}}" {
		t.Fatalf("JSON = %q", jsonText)
	}
}
