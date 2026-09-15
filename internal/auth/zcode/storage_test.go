package zcode

import "testing"

func TestTokenStorage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	ts := &TokenStorage{APIKey: "k", Secret: "s", JWT: "j", UserID: "u", DeviceMid: "dev", Provider: "zcode"}
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
}
