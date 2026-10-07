package netease

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestBuildPEAuthPlain395(t *testing.T) {
	guid := "00000000-0000-4000-8000-000000000000"
	body, err := buildPEAuthPlain(guid, "{\"device\":\"Pixel\"}\n", json.RawMessage(`{"sdkuid":"9001"}`))
	if err != nil {
		t.Fatal(err)
	}
	wantMessage := EngineVersion + libMinecraftPE + PatchVersion + patchHash + apkSignHash + guid
	if !strings.Contains(string(body), `"message":"`+wantMessage+`"`) {
		t.Fatalf("body does not contain exact message: %s", body)
	}
	if !strings.Contains(string(body), `"sa_data":"{\"device\":\"Pixel\"}\n"`) {
		t.Fatalf("sa_data newline/escaping changed: %s", body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body is invalid JSON: %v", err)
	}
	if decoded["sign"] != CountSignBase64(wantMessage) {
		t.Fatalf("wrong sign")
	}
}

func TestParseSAuth(t *testing.T) {
	for _, input := range []string{
		`{"sdkuid":"1","sessionid":"x"}`,
		`{"sauth_json":{"sdkuid":"1","sessionid":"x"}}`,
		`{"sauth_json":"{\"sdkuid\":\"1\",\"sessionid\":\"x\"}"}`,
	} {
		got, err := ParseSAuth([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `{"sdkuid":"1","sessionid":"x"}` {
			t.Fatalf("ParseSAuth(%s) = %s", input, got)
		}
	}
	cookieJSON := `{"sdkuid":"1","sessionid":"x"}`
	cookieHeader := "foo=bar; sauth_json=" + url.PathEscape(cookieJSON) + "; tail=value"
	got, err := ParseSAuth([]byte(cookieHeader))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != cookieJSON {
		t.Fatalf("ParseSAuth(cookie header) = %s", got)
	}
}

func TestAuthEntityMixedScalarJSON(t *testing.T) {
	for _, raw := range []string{
		`{"entity_id":643673007,"token":"fixture","ignored":{"field":"value"}}`,
		`{"entity_id":"643673007","token":"fixture"}`,
	} {
		var entity AuthEntity
		if err := json.Unmarshal([]byte(raw), &entity); err != nil {
			t.Fatal(err)
		}
		if entity.EntityID != "643673007" || entity.Token != "fixture" {
			t.Fatalf("unexpected entity: %#v", entity)
		}
	}
	for _, raw := range []string{`{"entity_id":{},"token":"fixture"}`, `{"entity_id":true,"token":"fixture"}`, `{"entity_id":1,"token":42}`} {
		var entity AuthEntity
		if json.Unmarshal([]byte(raw), &entity) == nil {
			t.Fatalf("accepted invalid entity: %s", raw)
		}
	}
}

func TestParseSAuthRejectsInvalidPayload(t *testing.T) {
	for _, raw := range []string{"", "not-a-cookie", "null", "[]", `{"sauth_json":null}`, `{"sauth_json":"broken"}`, "sauth_json=%zz"} {
		if _, err := ParseSAuth([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid sauth: %s", raw)
		}
	}
}
