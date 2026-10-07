package netease

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ParseSAuth extracts the PE login payload from a raw object, sauth_json wrapper,
// or Cookie header. Account metadata and batch imports belong to the gateway.
func ParseSAuth(input []byte) (json.RawMessage, error) {
	input = bytes.TrimSpace(input)
	if len(input) == 0 {
		return nil, fmt.Errorf("sauth input is empty")
	}
	if !json.Valid(input) {
		cookieValue, ok := findCookieValue(string(input), "sauth_json")
		if !ok {
			return nil, fmt.Errorf("sauth input is neither JSON nor an HTTP Cookie header containing sauth_json")
		}
		decoded, err := url.PathUnescape(cookieValue)
		if err != nil {
			return nil, fmt.Errorf("percent-decode sauth_json cookie: %w", err)
		}
		input = bytes.TrimSpace([]byte(decoded))
		if !json.Valid(input) {
			return nil, fmt.Errorf("sauth_json cookie is invalid JSON")
		}
	}
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(input, &wrapper); err == nil {
		if raw, ok := wrapper["sauth_json"]; ok {
			raw = bytes.TrimSpace(raw)
			if len(raw) > 0 && raw[0] == '"' {
				var nested string
				if err := json.Unmarshal(raw, &nested); err != nil {
					return nil, fmt.Errorf("decode string sauth_json: %w", err)
				}
				raw = []byte(nested)
			}
			if !json.Valid(raw) {
				return nil, fmt.Errorf("nested sauth_json is invalid JSON")
			}
			return compactSAuth(raw)
		}
	}
	return compactSAuth(input)
}

func findCookieValue(header, name string) (string, bool) {
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(part, "=")
		if ok && strings.TrimSpace(key) == name {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

func compactSAuth(input []byte) (json.RawMessage, error) {
	input = bytes.TrimSpace(input)
	if len(input) == 0 || input[0] != '{' {
		return nil, fmt.Errorf("sauth_json must be an object")
	}
	var out bytes.Buffer
	if err := json.Compact(&out, input); err != nil {
		return nil, err
	}
	return json.RawMessage(out.Bytes()), nil
}
