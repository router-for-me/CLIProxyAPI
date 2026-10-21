package config

import "testing"

func TestNormalizeOAuthOfficialClientOnly(t *testing.T) {
	if got := NormalizeOAuthOfficialClientOnly(nil); got != nil {
		t.Fatalf("nil input = %v, want nil", got)
	}
	if got := NormalizeOAuthOfficialClientOnly(map[string]bool{"  ": true}); got != nil {
		t.Fatalf("blank key kept: %v", got)
	}
	for i := 0; i < 50; i++ {
		got := NormalizeOAuthOfficialClientOnly(map[string]bool{"Codex": false, "codex": true, " CLAUDE ": false})
		if !got["codex"] || got["claude"] || len(got) != 2 {
			t.Fatalf("got %v, want codex=true claude=false", got)
		}
	}
}
