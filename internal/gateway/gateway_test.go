package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"netease2api/internal/netease"
)

type fakeUpstream struct {
	mu        sync.Mutex
	calls     []string
	active    map[int64]bool
	overlap   bool
	answer    string
	failFirst bool
	block     <-chan struct{}
}

func (f *fakeUpstream) Login(ctx context.Context, raw []byte) (*netease.Account, error) {
	var auth struct {
		UID string `json:"sdkuid"`
	}
	_ = json.Unmarshal(raw, &auth)
	if f.failFirst && auth.UID == "first" {
		return nil, &Failure{"login", "测试登录失败", true}
	}
	uid := int64(1)
	if auth.UID == "second" {
		uid = 2
	}
	return &netease.Account{UID: uid, Token: "fixture-token"}, nil
}
func (f *fakeUpstream) Ask(ctx context.Context, a *netease.Account, text string) (string, error) {
	f.mu.Lock()
	if f.active == nil {
		f.active = map[int64]bool{}
	}
	if f.active[a.UID] {
		f.overlap = true
	}
	f.active[a.UID] = true
	f.calls = append(f.calls, fmt.Sprint(a.UID))
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.active[a.UID] = false; f.mu.Unlock() }()
	if f.block != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-f.block:
		}
	}
	time.Sleep(5 * time.Millisecond)
	if f.answer != "" {
		return f.answer, nil
	}
	return "你好，狐狸在这里。", nil
}
func rawAccount(uid string) string {
	return fmt.Sprintf(`{"name":"%s","cookie":"{\"sdkuid\":\"%s\",\"sessionid\":\"secret-session-%s\"}"}`, uid, uid, uid)
}
func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func importTwo(t *testing.T, s *Store) {
	t.Helper()
	if _, _, err := s.Import(rawAccount("first") + "\n" + rawAccount("second")); err != nil {
		t.Fatal(err)
	}
}
func testApp(t *testing.T, up Upstream) (*Server, *Store) {
	s := testStore(t)
	importTwo(t, s)
	if err := s.EnsureDefaultKey("sk-fixture-api-key"); err != nil {
		t.Fatal(err)
	}
	return NewServer(&Engine{Store: s, Upstream: up, Timeout: time.Second}, "admin-fixture-secret"), s
}
func perform(app *Server, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	return w
}

func TestImportFormatsEncryptionAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	added, updated, err := s.Import("[" + rawAccount("first") + "," + rawAccount("second") + "]")
	if err != nil || added != 2 || updated != 0 {
		t.Fatalf("import: %d %d %v", added, updated, err)
	}
	_, updated, err = s.Import(`{"sauth_json":"{\"sdkuid\":\"first\",\"sessionid\":\"new-session\"}"}`)
	if err != nil || updated != 1 || len(s.Accounts()) != 2 {
		t.Fatalf("update: %d %v", updated, err)
	}
	if _, _, err = s.Import("Cookie: sauth_json=%7B%22sdkuid%22%3A%22third%22%2C%22sessionid%22%3A%22s%22%7D\nsauth_json=%7B%22sdkuid%22%3A%22fourth%22%2C%22sessionid%22%3A%22s%22%7D"); err != nil {
		t.Fatal(err)
	}
	wire, _ := os.ReadFile(filepath.Join(dir, "accounts.enc"))
	if bytes.Contains(wire, []byte("new-session")) || bytes.Contains(wire, []byte("sdkuid")) {
		t.Fatal("credentials persisted in plaintext")
	}
	s2, err := NewStore(dir)
	if err != nil || len(s2.Accounts()) != 4 {
		t.Fatalf("reload: %v", err)
	}
	public, _ := json.Marshal(s2.Accounts())
	for _, secret := range []string{"source", "sessionid", "new-session", "fixture-token"} {
		if bytes.Contains(public, []byte(secret)) {
			t.Fatal("credential in public view")
		}
	}
	// Reject an invalid batch without importing its valid prefix.
	if _, _, err = s.Import(rawAccount("fifth") + "\n{} "); err == nil || len(s.Accounts()) != 4 {
		t.Fatal("invalid batch partially imported")
	}
	// Wrong key and missing key fail explicitly, never overwrite the vault.
	master, _ := os.ReadFile(filepath.Join(dir, "master.key"))
	master[0] ^= 1
	_ = os.WriteFile(filepath.Join(dir, "master.key"), master, 0600)
	if _, err := NewStore(dir); err == nil {
		t.Fatal("wrong key accepted")
	}
	_ = os.Remove(filepath.Join(dir, "master.key"))
	if _, err := NewStore(dir); err == nil {
		t.Fatal("missing key silently replaced")
	}
}
func TestAccountLeasesRotationAndBusySafety(t *testing.T) {
	s := testStore(t)
	importTwo(t, s)
	f := &fakeUpstream{}
	e := &Engine{Store: s, Upstream: f, Timeout: time.Second}
	for i := 0; i < 4; i++ {
		if _, err := e.Complete(context.Background(), "hi", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(f.calls, ",") != "1,2,1,2" {
		t.Fatalf("rotation: %v", f.calls)
	}
	a, err := s.acquire(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Import(rawAccount(a.SDKUID)); err == nil {
		t.Fatal("replaced busy credentials")
	}
	if err := s.Update(a.ID, nil, true); err == nil {
		t.Fatal("deleted busy account")
	}
	s.release(a, a.login, a.loginAt, 0, nil, "lease", true)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Complete(context.Background(), "hi", "concurrent")
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.overlap {
		t.Fatal("same account used concurrently")
	}
	for _, view := range s.Accounts() {
		if view.Busy {
			t.Fatal("lease leaked")
		}
	}
}
func TestLoginFailoverAndCancellation(t *testing.T) {
	s := testStore(t)
	importTwo(t, s)
	f := &fakeUpstream{failFirst: true}
	e := &Engine{Store: s, Upstream: f, Timeout: time.Second}
	if _, err := e.Complete(context.Background(), "hi", "retry"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "2" || s.Accounts()[0].Status != "login_required" {
		t.Fatal("login failover did not quarantine account")
	}
	block := make(chan struct{})
	f.block = block
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := e.Complete(ctx, "hi", "cancel"); err == nil {
		t.Fatal("cancellation ignored")
	}
	for _, a := range s.Accounts() {
		if a.Busy {
			t.Fatal("cancellation leaked lease")
		}
	}
}
func TestAPIAuthenticationJSONAndSSE(t *testing.T) {
	app, s := testApp(t, &fakeUpstream{answer: strings.Repeat("狐狸🦊", 25)})
	if got := perform(app, "GET", "/v1/models", "", "").Code; got != 401 {
		t.Fatal(got)
	}
	if got := perform(app, "GET", "/admin/accounts", "sk-fixture-api-key", "").Code; got != 401 {
		t.Fatal("API key gained admin access")
	}
	models := perform(app, "GET", "/v1/models", "sk-fixture-api-key", "")
	if models.Code != 200 || !strings.Contains(models.Body.String(), Model) {
		t.Fatal(models.Body.String())
	}
	request := `{"model":"netease-fox","messages":[{"role":"user","content":"你好"}]}`
	w := perform(app, "POST", "/v1/chat/completions", "sk-fixture-api-key", request)
	var reply struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != 200 || reply.Object != "chat.completion" || reply.Choices[0].Finish != "stop" {
		t.Fatalf("completion: %s", w.Body.String())
	}
	stream := perform(app, "POST", "/v1/chat/completions", "sk-fixture-api-key", strings.TrimSuffix(request, "}")+`,"stream":true}`)
	var content strings.Builder
	role, stop, done := false, false, false
	id := ""
	for _, line := range strings.Split(stream.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			done = true
			continue
		}
		var chunk struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Choices []struct {
				Delta  map[string]string `json:"delta"`
				Finish *string           `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatal(err)
		}
		if id == "" {
			id = chunk.ID
		}
		if id != chunk.ID || chunk.Object != "chat.completion.chunk" {
			t.Fatal("unstable SSE id/object")
		}
		delta := chunk.Choices[0]
		if delta.Delta["role"] == "assistant" {
			role = true
		}
		content.WriteString(delta.Delta["content"])
		if delta.Finish != nil && *delta.Finish == "stop" {
			stop = true
		}
	}
	if !role || !stop || !done || content.String() != reply.Choices[0].Message.Content {
		t.Fatalf("broken SSE: %s", stream.Body.String())
	}
	views := s.Accounts()
	if views[0].Success+views[1].Success != 2 {
		t.Fatal("success statistics incorrect")
	}
}
func TestRequestValidationAndKeyRevocation(t *testing.T) {
	app, s := testApp(t, &fakeUpstream{})
	for _, body := range []string{
		`{"model":"wrong","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"netease-fox","messages":[{"role":"user","content":"` + strings.Repeat("中", 201) + `"}]}`,
		`{"model":"netease-fox","messages":[{"role":"user","content":[{"type":"image_url"}]}]}`,
		`{"model":"netease-fox","messages":[{"role":"user","content":"hi"}],"tools":[{}]}`,
		`{"model":"netease-fox","messages":[{"role":"user","content":"hi"}]} {}`,
	} {
		if w := perform(app, "POST", "/v1/chat/completions", "sk-fixture-api-key", body); w.Code != 400 {
			t.Fatalf("bad request accepted %d: %s", w.Code, body)
		}
	}
	view, key, err := s.CreateKey("client")
	if err != nil || !s.Authenticate(key) {
		t.Fatal(err)
	}
	if err := s.RevokeKey(view.ID); err != nil || s.Authenticate(key) {
		t.Fatal("revocation failed")
	}
	reloaded, err := NewStore(s.dir)
	if err != nil || reloaded.Authenticate(key) {
		t.Fatal("revocation not persisted")
	}
	w := perform(app, "GET", "/admin/accounts", "admin-fixture-secret", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-session") {
		t.Fatal("admin account disclosure")
	}
	w = perform(app, "POST", "/admin/accounts/import", "admin-fixture-secret", `{"content":"{}"}`)
	if w.Code != 400 {
		t.Fatal("malformed import accepted")
	}
}
func TestNoAccountsStreamFailureIsNotSuccessful(t *testing.T) {
	s := testStore(t)
	_ = s.EnsureDefaultKey("sk-fixture-api-key")
	app := NewServer(&Engine{Store: s, Upstream: &fakeUpstream{}, Timeout: time.Second}, "admin-fixture-secret")
	body := `{"model":"netease-fox","messages":[{"role":"user","content":"hi"}],"stream":true}`
	w := perform(app, "POST", "/v1/chat/completions", "sk-fixture-api-key", body)
	if !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), `"finish_reason":"stop"`) || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal(w.Body.String())
	}
}
func TestHTTPRealTransportAssetsAndCors(t *testing.T) {
	app, _ := testApp(t, &fakeUpstream{})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	for _, path := range []string{"/", "/app.js", "/style.css", "/healthz"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || len(body) == 0 || response.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal(path)
		}
	}
	request, _ := http.NewRequest("OPTIONS", server.URL+"/v1/chat/completions", nil)
	request.Header.Set("Origin", "http://client.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 204 || response.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("CORS failed")
	}
}
