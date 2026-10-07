package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNamelessCookiesAndChannelIsolation(t *testing.T) {
	s := testStore(t)
	batch := `[
		{"cookie":{"sdkuid":"synthetic-shared-id","sessionid":"synthetic-session","login_channel":"netease"}},
		{"name":"   ","cookie":{"sauth_json":{"sdkuid":"synthetic-shared-id","sessionid":"synthetic-session","login_channel":"4399pc"}}},
		{"name":null,"sauth_json":{"sdkuid":"synthetic-pe-id","sessionid":"synthetic-session","login_channel":"4399com"}},
		"Uauth=4399|1|synthetic|synthetic|1700000000000; Pauth=synthetic-token"
	]`
	added, updated, err := s.Import(batch)
	if err != nil || added != 4 || updated != 0 {
		t.Fatalf("import: %d %d %v", added, updated, err)
	}
	accounts := s.Accounts()
	for _, a := range accounts {
		if strings.TrimSpace(a.Name) == "" || a.Channel == "" {
			t.Fatal("missing generated name or channel")
		}
	}
	if accounts[0].ID == accounts[1].ID || accounts[0].Channel == accounts[1].Channel {
		t.Fatal("different channels merged")
	}
	if _, updated, err := s.Import(`{"name":"自定义名称","cookie":{"sdkuid":"synthetic-shared-id","sessionid":"synthetic-updated","login_channel":"4399pc"}}`); err != nil || updated != 1 {
		t.Fatalf("update: %d %v", updated, err)
	}
	if _, updated, err := s.Import(`{"cookie":{"sdkuid":"synthetic-shared-id","sessionid":"synthetic-refreshed","login_channel":"4399pc"}}`); err != nil || updated != 1 {
		t.Fatalf("nameless update: %d %v", updated, err)
	}
	if s.Accounts()[1].Name != "自定义名称" {
		t.Fatal("nameless update erased custom name")
	}
	if _, updated, err := s.Import("Uauth=4399|1|synthetic|synthetic|1700000000000; Pauth=synthetic-token"); err != nil || updated != 1 {
		t.Fatalf("browser cookie duplicate: %d %v", updated, err)
	}
	public, _ := json.Marshal(s.Accounts())
	for _, secret := range []string{"Uauth=", "synthetic-token", "synthetic-refreshed", "sessionid"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("credential in public account view")
		}
	}
	if _, _, err := s.Import(`{"cookie":{"sdkuid":"valid-prefix","sessionid":"synthetic"}}` + "\n" + "Uauth=invalid"); err == nil || len(s.Accounts()) != 4 {
		t.Fatal("invalid cookie batch partially imported")
	}
}

func TestNameOptionalCookieHeaderFormats(t *testing.T) {
	for _, raw := range []string{
		`{"cookie":"{\"sauth_json\":\"{\\\"sdkuid\\\":\\\"synthetic\\\",\\\"sessionid\\\":\\\"synthetic\\\"}\"}"}`,
		`{"cookie":["Uauth=4399|1|synthetic|synthetic|1700000000000; Path=/; HttpOnly","Pauth=synthetic-only; Secure"]}`,
		`cookie: sauth_json=%7B%22sdkuid%22%3A%22synthetic%22%2C%22sessionid%22%3A%22synthetic%22%7D`,
	} {
		items, err := parseImports(raw)
		if err != nil || len(items) != 1 || items[0].name == "" {
			t.Fatalf("nameless input rejected: %v", err)
		}
	}
}
