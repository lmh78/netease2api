package netease

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Credential is the normalized, local input. Browser cookies remain private
// and are exchanged only when an account is checked or used.
type Credential struct {
	Source     json.RawMessage
	SAuth      json.RawMessage
	Cookie4399 string
	SDKUID     string
	Channel    string
	Identity   string
}

func ParseCredential(input []byte) (*Credential, error) {
	return parseCredential(input, 0)
}

func parseCredential(input []byte, depth int) (*Credential, error) {
	if depth > 4 {
		return nil, fmt.Errorf("Cookie 包装层数过多")
	}
	input = bytes.TrimSpace(input)
	if len(input) == 0 {
		return nil, fmt.Errorf("Cookie 为空")
	}
	if json.Valid(input) {
		var text string
		if json.Unmarshal(input, &text) == nil {
			return parseCredential([]byte(text), depth+1)
		}
		var parts []string
		if json.Unmarshal(input, &parts) == nil {
			return parseCredential([]byte(strings.Join(parts, "; ")), depth+1)
		}
		var wrapper map[string]json.RawMessage
		if json.Unmarshal(input, &wrapper) == nil {
			for _, key := range []string{"cookie", "4399_cookie"} {
				if raw, ok := wrapper[key]; ok {
					return parseCredential(raw, depth+1)
				}
			}
		}
	}
	header := strings.TrimSpace(string(input))
	if len(header) >= 7 && strings.EqualFold(header[:7], "Cookie:") {
		header = strings.TrimSpace(header[7:])
	}
	if _, exists := findCookieValue(header, "Uauth"); exists {
		cookie, _, err := normalize4399Cookie(header)
		if err != nil {
			return nil, err
		}
		source, _ := json.Marshal(map[string]string{"4399_cookie": cookie})
		return &Credential{Source: source, Cookie4399: cookie, Channel: "4399pc", Identity: "4399pc-cookie:" + cookie}, nil
	}
	sauth, err := ParseSAuth([]byte(header))
	if err != nil {
		return nil, fmt.Errorf("需要 sauth_json 或含 Uauth 的 4399 Cookie")
	}
	var fields struct {
		SDKUID     json.RawMessage `json:"sdkuid"`
		Session    string          `json:"sessionid"`
		Channel    string          `json:"login_channel"`
		AppChannel string          `json:"app_channel"`
	}
	if err := json.Unmarshal(sauth, &fields); err != nil {
		return nil, fmt.Errorf("sauth 必须是对象")
	}
	uid, err := decodeJSONScalarString(fields.SDKUID)
	if err != nil || strings.TrimSpace(uid) == "" || strings.TrimSpace(fields.Session) == "" {
		return nil, fmt.Errorf("sauth 缺少有效 sdkuid/sessionid")
	}
	channel := fields.Channel
	if channel == "" {
		channel = fields.AppChannel
	}
	if channel == "" {
		channel = "netease"
	}
	return &Credential{Source: sauth, SAuth: sauth, SDKUID: uid, Channel: channel, Identity: channel + ":" + uid}, nil
}
