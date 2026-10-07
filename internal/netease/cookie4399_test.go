package netease

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const synthetic4399Cookie = "Uauth=4399|1|fixture|fixture|1700000000000; Pauth=synthetic-only; USESSIONID=discard"

// All account values in tests are synthetic and cannot authenticate to 4399.
func Test4399CookieToPELoginAndFox(t *testing.T) {
	var calls []string
	var stateMu sync.Mutex
	var sid, cmid string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stateMu.Lock()
		defer stateMu.Unlock()
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/check":
			if r.URL.Query().Get("rand_time") != "1700000000000" || r.URL.Query().Get("appId") != "kid_wdsj" {
				t.Error("incorrect cookie exchange query")
			}
			cookie := r.Header.Get("Cookie")
			if !strings.Contains(cookie, "Pauth=synthetic-only") || strings.Contains(cookie, "USESSIONID") {
				t.Error("incorrect cookie exchange header")
			}
			w.Header().Set("Location", "https://never-follow.invalid/callback?ticket=synthetic&app=kid_wdsj")
			w.WriteHeader(http.StatusFound)
		case "/info":
			if r.Header.Get("Cookie") != "" {
				t.Error("browser cookie leaked to SDK endpoint")
			}
			if r.URL.Query().Get("queryStr") != "ticket=synthetic&app=kid_wdsj" {
				t.Error("incorrect SDK exchange query")
			}
			io.WriteString(w, `{"code":0,"data":{"sdk_login_data":"uid=synthetic-uid&token=synthetic-session&time=1700000000000&username=synthetic-user"}}`)
		case "/pe-authentication":
			wire, _ := io.ReadAll(r.Body)
			plain, err := decryptHTTPResponseHex(string(wire))
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			object, err := extractLeadingJSONObject(plain)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			var login struct {
				SAuth   map[string]string `json:"sauth_json"`
				Channel string            `json:"pay_channel"`
				Message string            `json:"message"`
				Sign    string            `json:"sign"`
			}
			if err := json.Unmarshal([]byte(object), &login); err != nil {
				t.Error(err)
				return
			}
			if login.SAuth["login_channel"] != "4399pc" || login.SAuth["sdkuid"] != "synthetic-uid" || login.SAuth["sessionid"] != "synthetic-session" || login.Channel != "dashen_cloudgame" {
				t.Error("4399 sauth not forwarded to PE login")
			}
			if login.Sign != CountSignBase64(login.Message) {
				t.Error("invalid PE login signature")
			}
			response, err := encryptHTTPRequestFixed([]byte(`{"code":0,"entity":{"entity_id":42,"token":"synthetic-game-token"}}`), []byte("0123456789abcdef"), 5, []byte("abcdefghijklmnop"))
			if err != nil {
				t.Error(err)
				return
			}
			io.WriteString(w, response)
		case "/pet-agent/chat", "/pet-agent/history":
			body, _ := io.ReadAll(r.Body)
			if r.Header.Get("user-id") != "42" || r.Header.Get("user-token") != DynamicToken(r.URL.Path, string(body), "synthetic-game-token") {
				t.Error("incorrect fox authentication")
			}
			if r.URL.Path == "/pet-agent/chat" {
				var request PetChatRequest
				json.Unmarshal(body, &request)
				sid, cmid = request.SessionID, request.ClientMessageID
				io.WriteString(w, `{"code":0,"entity":{}}`)
			} else {
				json.NewEncoder(w).Encode(map[string]any{"code": 0, "entity": PetHistoryEntity{Rows: []PetHistoryRow{{SessionID: sid, ClientMessageID: cmid, AssistantMessage: "synthetic answer"}}}})
			}
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := NewClient(time.Second)
	client.PECoreBaseURL, client.APIBaseURL = server.URL, server.URL
	client.Games4399 = &Games4399CookieClient{HTTP: server.Client(), CheckURL: server.URL + "/check", InfoURL: server.URL + "/info"}
	account, err := client.PELogin(context.Background(), []byte(synthetic4399Cookie), "")
	if err != nil {
		t.Fatal(err)
	}
	if account.UID != 42 || account.SDKUID != "synthetic-uid" {
		t.Fatalf("wrong account: %#v", account)
	}
	if _, err := client.PetChat(context.Background(), account, PetChatRequest{SessionID: "test-session", ClientMessageID: "test-message", Content: "hello", SkinName: "狐狸"}); err != nil {
		t.Fatal(err)
	}
	answer, err := client.FindPetAnswer(context.Background(), account, "test-session", "test-message", 1)
	if err != nil || answer == nil || answer.AssistantMessage != "synthetic answer" {
		t.Fatalf("answer missing: %v", err)
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if strings.Join(calls, ",") != "/check,/info,/pe-authentication,/pet-agent/chat,/pet-agent/history" {
		t.Fatalf("unexpected flow: %v", calls)
	}
}

func Test4399RejectsExpiredCookiesWithoutFollowingRedirect(t *testing.T) {
	for _, status := range []int{200, 401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			client := &Games4399CookieClient{HTTP: server.Client(), CheckURL: server.URL, InfoURL: "https://never-call.invalid"}
			_, err := client.Exchange(context.Background(), synthetic4399Cookie)
			if !errors.Is(err, ErrInvalidCookie) {
				t.Fatalf("expired cookie not recognized: %v", err)
			}
		})
	}
}

func TestCookieNormalizationAndChannels(t *testing.T) {
	for _, input := range []string{
		synthetic4399Cookie,
		"Cookie: " + synthetic4399Cookie,
		`["Uauth=4399|1|fixture|fixture|1700000000000; Path=/; HttpOnly","Pauth=synthetic-only; Secure"]`,
		`{"cookie":{"sauth_json":{"sdkuid":"synthetic","sessionid":"synthetic","login_channel":"4399com","app_channel":"4399com"}}}`,
		`{"cookie":"{\"sdkuid\":\"synthetic\",\"sessionid\":\"synthetic\",\"login_channel\":\"4399pc\"}"}`,
	} {
		credential, err := ParseCredential([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(credential.Channel, "4399") {
			t.Fatalf("wrong channel: %s", credential.Channel)
		}
		if strings.Contains(credential.Cookie4399, "USESSIONID") || strings.Contains(credential.Cookie4399, "Path=") {
			t.Fatal("cookie attributes not removed")
		}
	}
	for _, input := range []string{"Uauth=broken", "Uauth=4399|1|a|b|not-time", "Uauth=4399|1|a|b|1700000000000\r\nInjected=value", "Uauth=4399|1|a|b|1700000000000; Uauth=conflict"} {
		if _, err := ParseCredential([]byte(input)); err == nil {
			t.Fatalf("accepted invalid cookie: %q", input)
		}
	}
}
