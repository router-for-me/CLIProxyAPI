package zcode

import "testing"

func TestTokenStorage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	ts := &TokenStorage{
		APIKey: "k", Secret: "s", JWT: "j", UserID: "u", DeviceMid: "dev", Provider: "zcode",
		BaseURL: "https://open.bigmodel.cn/api/anthropic",
		Headers: map[string]string{"User-Agent": "ZCode/3.12.0", "X-ZCode-Agent": "glm"},
	}
	path := dir + "/zcode.json"
	if err := ts.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	ts2 := &TokenStorage{}
	if err := ts2.LoadTokenFromFile(path); err != nil {
		t.Fatal(err)
	}
	if ts2.FullCredential().FullKey() != "k.s" || ts2.DeviceMid != "dev" {
		t.Fatalf("roundtrip mismatch: %+v", ts2)
	}
	// base_url and the identity headers must survive the file round-trip so a
	// restart does not silently fall back to an empty endpoint / no headers.
	if ts2.BaseURL != "https://open.bigmodel.cn/api/anthropic" {
		t.Fatalf("base_url lost: %q", ts2.BaseURL)
	}
	if ts2.Headers["User-Agent"] != "ZCode/3.12.0" || ts2.Headers["X-ZCode-Agent"] != "glm" {
		t.Fatalf("headers lost: %+v", ts2.Headers)
	}
}
