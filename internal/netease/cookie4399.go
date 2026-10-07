package netease

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Adapted from src/channel/4399/client.ts ClientPCImpl.getSAuthJson.
// Source: https://github.com/nethard-project/nethard-core-channel
// Only existing-cookie exchange is included; no registration/password/captcha.
var ErrInvalidCookie = errors.New("login cookie rejected")

type Games4399CookieClient struct {
	HTTP     *http.Client
	CheckURL string
	InfoURL  string
}

func NewGames4399CookieClient(client *http.Client) *Games4399CookieClient {
	return &Games4399CookieClient{
		HTTP:     client,
		CheckURL: "https://ptlogin.4399.com/ptlogin/checkKidLoginUserCookie.do",
		InfoURL:  "https://microgame.5054399.net/v2/service/sdk/info",
	}
}

func normalize4399Cookie(header string) (string, string, error) {
	if strings.ContainsAny(header, "\r\n") {
		return "", "", fmt.Errorf("Cookie 不能包含换行")
	}
	pairs := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "path", "domain", "expires", "max-age", "samesite", "usessionid":
			continue
		}
		if key == "" {
			continue
		}
		if old, exists := pairs[key]; exists && old != value {
			return "", "", fmt.Errorf("Cookie 中有冲突的重复字段")
		}
		pairs[key] = value
	}
	uauth, err := url.PathUnescape(strings.Trim(pairs["Uauth"], "\""))
	if err != nil {
		return "", "", fmt.Errorf("Uauth 编码无效")
	}
	parts := strings.Split(uauth, "|")
	if len(parts) < 5 || parts[0] != "4399" {
		return "", "", fmt.Errorf("需要有效的 4399 Uauth Cookie")
	}
	timestamp := parts[4]
	if value, err := strconv.ParseInt(timestamp, 10, 64); err != nil || value <= 0 {
		return "", "", fmt.Errorf("Uauth 时间戳无效")
	}
	keys := make([]string, 0, len(pairs))
	for key := range pairs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]string, 0, len(keys))
	for _, key := range keys {
		items = append(items, key+"="+pairs[key])
	}
	return strings.Join(items, "; "), timestamp, nil
}

func randomDeviceID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(cryptorand.Reader, value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (c *Games4399CookieClient) Exchange(ctx context.Context, header string) (json.RawMessage, error) {
	cookie, timestamp, err := normalize4399Cookie(header)
	if err != nil {
		return nil, fmt.Errorf("%w: 4399 Cookie 格式无效", ErrInvalidCookie)
	}
	client := &http.Client{Timeout: 25 * time.Second}
	if c.HTTP != nil {
		*client = *c.HTTP
	}
	// Never follow redirects with account cookies or call the returned URL.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	checkURL, err := url.Parse(c.CheckURL)
	if err != nil {
		return nil, fmt.Errorf("invalid 4399 check endpoint")
	}
	query := checkURL.Query()
	query.Set("appId", "kid_wdsj")
	query.Set("gameUrl", "http://cdn.h5wan.4399sj.com/microterminal-h5-frame?game_id=500352")
	query.Set("rand_time", timestamp)
	query.Set("nick", "null")
	query.Set("onLineStart", "false")
	query.Set("show", "1")
	query.Set("isCrossDomain", "1")
	query.Set("retUrl", "http://ptlogin.4399.com/resource/ucenter.html")
	checkURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36")
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("4399 cookie service unavailable")
	}
	response.Body.Close()
	if response.StatusCode >= 500 || response.StatusCode == 429 {
		return nil, fmt.Errorf("4399 cookie service HTTP %d", response.StatusCode)
	}
	if response.StatusCode != 302 && response.StatusCode != 200 {
		return nil, fmt.Errorf("%w: 4399 Cookie 已失效", ErrInvalidCookie)
	}
	redirect, err := url.Parse(response.Header.Get("Location"))
	if err != nil || redirect == nil || redirect.RawQuery == "" {
		return nil, fmt.Errorf("%w: 4399 Cookie 无法授权", ErrInvalidCookie)
	}
	infoURL, err := url.Parse(c.InfoURL)
	if err != nil {
		return nil, fmt.Errorf("invalid 4399 SDK endpoint")
	}
	params := infoURL.Query()
	params.Set("queryStr", redirect.RawQuery)
	infoURL.RawQuery = params.Encode()
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, infoURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36")
	response, err = client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("4399 SDK service unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("4399 SDK service HTTP %d", response.StatusCode)
	}
	var sdk struct {
		Data struct {
			LoginData string `json:"sdk_login_data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&sdk); err != nil {
		return nil, fmt.Errorf("%w: 4399 SDK 响应无效", ErrInvalidCookie)
	}
	login, err := url.ParseQuery(sdk.Data.LoginData)
	if err != nil || login.Get("uid") == "" || login.Get("token") == "" {
		return nil, fmt.Errorf("%w: 4399 SDK 未返回登录态", ErrInvalidCookie)
	}
	id, err := randomDeviceID()
	if err != nil {
		return nil, err
	}
	device, err := randomDeviceID()
	if err != nil {
		return nil, err
	}
	udid, err := randomDeviceID()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{
		"aim_info":    `{"aim":"","country":"CN","tz":"+0800","tzid":"Asia\/Shanghai"}`,
		"app_channel": "4399pc", "login_channel": "4399pc", "platform": "pc", "source_platform": "pc",
		"client_login_sn": id, "deviceid": device, "gameid": "x19", "gas_token": "", "ip": "",
		"realname": `{"realname_type":"0"}`, "sdk_version": "1.0.0",
		"sdkuid": login.Get("uid"), "sessionid": login.Get("token"),
		"timestamp": login.Get("time"), "userid": login.Get("username"), "udid": udid,
	})
}
