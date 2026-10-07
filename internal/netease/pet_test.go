package netease

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPetSignedRequestAndPagination(t *testing.T) {
	account := &Account{UID: 123, Token: "test-token"}
	chatCalled, historyCalls := false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("user-id") != "123" || r.Header.Get("user-token") != DynamicToken(r.URL.Path, string(body), account.Token) {
			t.Errorf("incorrect method or signed request bytes")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/pet-agent/chat":
			var request PetChatRequest
			if err := json.Unmarshal(body, &request); err != nil || request.Content != "你好" || request.SkinName != "狐狸" {
				t.Errorf("incorrect Chinese chat payload")
			}
			chatCalled = true
			io.WriteString(w, `{"code":0,"entity":{}}`)
		case "/pet-agent/history":
			historyCalls++
			var request PetHistoryRequest
			_ = json.Unmarshal(body, &request)
			if request.RecordIDCursor == "" {
				io.WriteString(w, `{"code":0,"entity":{"rows":[{"session_id":"wrong-session","client_message_id":"msg-test","assistant_message":"wrong answer"}],"record_id_cursor":"page-2"}}`)
			} else if request.RecordIDCursor == "page-2" {
				io.WriteString(w, `{"code":0,"entity":{"rows":[{"session_id":"1234","client_message_id":"msg-test","assistant_message":"你好，我是狐狸。"}],"record_id_cursor":""}}`)
			} else {
				t.Errorf("unexpected cursor")
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClient(0)
	client.APIBaseURL = server.URL
	if _, err := client.PetChat(context.Background(), account, PetChatRequest{"msg-test", "1234", "你好", "狐狸"}); err != nil {
		t.Fatal(err)
	}
	row, err := client.FindPetAnswer(context.Background(), account, "1234", "msg-test", 5)
	if err != nil || row == nil || row.AssistantMessage != "你好，我是狐狸。" || !chatCalled || historyCalls != 2 {
		t.Fatalf("matching/pagination failed: row=%+v err=%v calls=%d", row, err, historyCalls)
	}
}

func TestPetRejectsMalformedAndErrorResponses(t *testing.T) {
	for _, response := range []string{`{}`, `{"code":5,"message":"daily limit"}`, `not json`, `{"code":0}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, response) }))
		client := NewClient(0)
		client.APIBaseURL = server.URL
		if _, err := client.PetHistory(context.Background(), &Account{UID: 123, Token: "test"}, "", 20); err == nil {
			t.Errorf("accepted malformed/error response %s", response)
		}
		server.Close()
	}
}

func TestPetHistoryStopsOnRepeatedCursor(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"code":0,"entity":{"rows":[{"session_id":"old"}],"record_id_cursor":"same"}}`)
	}))
	defer server.Close()
	client := NewClient(0)
	client.APIBaseURL = server.URL
	row, err := client.FindPetAnswer(context.Background(), &Account{UID: 123, Token: "test"}, "new", "msg-new", 5)
	if row != nil || err != nil || calls != 2 {
		t.Fatalf("cursor loop handling failed: row=%+v err=%v calls=%d", row, err, calls)
	}
}

func TestPetIDsMatchClientFormat(t *testing.T) {
	sid, cmid := NewPetIDs(1234)
	_, next := NewPetIDs(1234)
	if sid != "1234" || !strings.HasPrefix(cmid, "msg_") || cmid == next {
		t.Fatal("IDs do not match client format or collide")
	}
}
