package auth

import "testing"

func TestSelectedAuthIDRoundTrip(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetSelectedAuthID("Codex", "auth-1")

	if got := mgr.SelectedAuthID("codex"); got != "auth-1" {
		t.Fatalf("expected selected auth auth-1, got %q", got)
	}
	if got := mgr.SelectedAuthID("CODEX"); got != "auth-1" {
		t.Fatalf("expected provider lookup to be case-insensitive, got %q", got)
	}
}
