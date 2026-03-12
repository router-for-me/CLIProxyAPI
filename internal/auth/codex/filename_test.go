package codex

import "testing"

func TestCredentialFileNameNormalizesTeamAliases(t *testing.T) {
	got := CredentialFileName("tester@example.com", "business", "deadbeef", true)
	want := "codex-deadbeef-tester@example.com-team.json"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
