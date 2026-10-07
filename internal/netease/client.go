package netease

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PE login is adapted from the user-provided nethard-core archive.
// Declared source: https://github.com/nethard-project/nethard-core-channel
// See PROTOCOL_SOURCE.md for the tested archive and version parameters.
// Client implements only PE authentication and the fox chat HTTP APIs.
type Client struct {
	HTTP          *http.Client
	Random        io.Reader
	PECoreBaseURL string
	APIBaseURL    string
	Games4399     *Games4399CookieClient
}

func NewClient(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &Client{
		HTTP:          &http.Client{Timeout: timeout},
		Random:        cryptorand.Reader,
		PECoreBaseURL: peCoreBaseURL,
		APIBaseURL:    apiBaseURL,
	}
}

type Account struct {
	UID    int64
	Token  string
	SDKUID string
}

type AuthEntity struct {
	EntityID string `json:"entity_id"`
	Token    string `json:"token"`
}

// The PE endpoint returns entity_id as either a JSON number or a string.
func (e *AuthEntity) UnmarshalJSON(data []byte) error {
	var wire struct {
		EntityID json.RawMessage `json:"entity_id"`
		Token    string          `json:"token"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	id, err := decodeJSONScalarString(wire.EntityID)
	if err != nil {
		return fmt.Errorf("decode entity_id: %w", err)
	}
	e.EntityID, e.Token = id, wire.Token
	return nil
}

func decodeJSONScalarString(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		return value, nil
	}
	var scalar any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&scalar); err != nil {
		return "", err
	}
	switch value := scalar.(type) {
	case json.Number:
		return value.String(), nil
	default:
		return "", fmt.Errorf("expected JSON scalar, got %T", scalar)
	}
}

type authResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Details string      `json:"details"`
	Entity  *AuthEntity `json:"entity"`
}

// PELogin performs /pe-authentication using a captured sauth wrapper or a raw
// sauth_json object. saData must include the reference trailing newline.
func (c *Client) PELogin(ctx context.Context, sauthInput []byte, saData string) (*Account, error) {
	credential, err := ParseCredential(sauthInput)
	if err != nil {
		return nil, err
	}
	sauth := credential.SAuth
	if credential.Cookie4399 != "" {
		channel := c.Games4399
		if channel == nil {
			channel = NewGames4399CookieClient(c.HTTP)
		}
		sauth, err = channel.Exchange(ctx, credential.Cookie4399)
		if err != nil {
			return nil, err
		}
		credential, err = ParseCredential(sauth)
		if err != nil {
			return nil, fmt.Errorf("%w: 4399 登录态无效", ErrInvalidCookie)
		}
	}
	if strings.TrimSpace(saData) == "" {
		saData, err = buildSAData(sauth)
		if err != nil {
			return nil, err
		}
	}
	seed := uuid.NewSHA1(uuid.New(), []byte(strconv.FormatInt(time.Now().UnixMilli(), 10))).String()
	body, err := buildPEAuthPlain(seed, saData, sauth)
	if err != nil {
		return nil, err
	}
	wire, err := encryptHTTPRequest(body, c.Random)
	if err != nil {
		return nil, fmt.Errorf("encrypt PE authentication: %w", err)
	}
	response, err := c.post(ctx, c.PECoreBaseURL+peAuthPath, wire, map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   httpUserAgent,
	})
	if err != nil {
		return nil, fmt.Errorf("PE authentication: %w", err)
	}
	plain, err := decryptHTTPResponseHex(string(response))
	if err != nil {
		return nil, fmt.Errorf("decrypt PE authentication response: %w", err)
	}
	jsonText, err := extractLeadingJSONObject(plain)
	if err != nil {
		return nil, err
	}
	var decoded authResponse
	if err := json.Unmarshal([]byte(jsonText), &decoded); err != nil {
		return nil, fmt.Errorf("decode PE authentication response: %w", err)
	}
	if decoded.Code != 0 {
		return nil, fmt.Errorf("PE authentication rejected: code=%d message=%s details=%s", decoded.Code, decoded.Message, decoded.Details)
	}
	if decoded.Entity == nil || decoded.Entity.EntityID == "" || decoded.Entity.Token == "" {
		return nil, fmt.Errorf("PE authentication response has no uid/token")
	}
	uid, err := strconv.ParseInt(decoded.Entity.EntityID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse PE uid %q: %w", decoded.Entity.EntityID, err)
	}
	return &Account{UID: uid, Token: decoded.Entity.Token, SDKUID: credential.SDKUID}, nil
}

func buildPEAuthPlain(guid, saData string, sauth json.RawMessage) ([]byte, error) {
	if guid == "" {
		return nil, fmt.Errorf("PE authentication guid is empty")
	}
	if !json.Valid(sauth) {
		return nil, fmt.Errorf("sauth_json is invalid JSON")
	}
	message := EngineVersion + libMinecraftPE + PatchVersion + patchHash + apkSignHash + guid
	sign := CountSignBase64(message)

	var body bytes.Buffer
	body.WriteString(`{"engine_version":`)
	body.WriteString(jsonQuote(EngineVersion))
	body.WriteString(`,"extra_param":"extra","message":`)
	body.WriteString(jsonQuote(message))
	body.WriteString(`,"patch_version":`)
	body.WriteString(jsonQuote(PatchVersion))
	body.WriteString(`,"pay_channel":"dashen_cloudgame","sa_data":`)
	body.WriteString(jsonQuote(saData))
	body.WriteString(`,"sauth_json":`)
	body.Write(sauth)
	body.WriteString(`,"seed":`)
	body.WriteString(jsonQuote(guid))
	body.WriteString(`,"sign":`)
	body.WriteString(jsonQuote(sign))
	body.WriteByte('}')
	return body.Bytes(), nil
}

func jsonQuote(s string) string {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(out.String(), "\n")
}

func (c *Client) postJSON(ctx context.Context, host, path, body string, account *Account) ([]byte, error) {
	return c.post(ctx, strings.TrimRight(host, "/")+path, body, map[string]string{
		"Content-Type": "application/json",
		"User-Agent":   httpUserAgent,
		"user-id":      strconv.FormatInt(account.UID, 10),
		"user-token":   DynamicToken(path, body, account.Token),
	})
}

func (c *Client) post(ctx context.Context, endpoint, body string, headers map[string]string) ([]byte, error) {
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 45 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(response)))
	}
	return response, nil
}
