package config

import "testing"

func TestSanitizeClaudeKeys_NormalizesCloakReplacements(t *testing.T) {
	cfg := &Config{
		ClaudeKey: []ClaudeKey{{
			APIKey: "key-123",
			Cloak: &CloakConfig{
				RequestReplacements: []TextReplacement{
					{Find: " OpenClaw ", Replace: " OCPlatform "},
					{Find: "OpenClaw", Replace: "ignored"},
					{Find: "", Replace: "skip"},
				},
				ResponseReplacements: []TextReplacement{
					{Find: " create_task ", Replace: " sessions_spawn "},
					{Find: "create_task", Replace: "ignored"},
					{Find: " ", Replace: "skip"},
				},
			},
		}},
	}

	cfg.SanitizeClaudeKeys()

	cloak := cfg.ClaudeKey[0].Cloak
	if cloak == nil {
		t.Fatalf("expected cloak config to be preserved")
	}

	// Whitespace is preserved; " OpenClaw " and "OpenClaw" are distinct keys.
	if len(cloak.RequestReplacements) != 2 {
		t.Fatalf("request replacements len = %d, want 2", len(cloak.RequestReplacements))
	}
	if got := cloak.RequestReplacements[0]; got.Find != " OpenClaw " || got.Replace != " OCPlatform " {
		t.Fatalf("request replacement[0] = %#v, want \" OpenClaw \" -> \" OCPlatform \"", got)
	}
	if got := cloak.RequestReplacements[1]; got.Find != "OpenClaw" || got.Replace != "ignored" {
		t.Fatalf("request replacement[1] = %#v, want OpenClaw -> ignored", got)
	}

	// " " is empty after trim so it's skipped; the other two are distinct.
	if len(cloak.ResponseReplacements) != 2 {
		t.Fatalf("response replacements len = %d, want 2", len(cloak.ResponseReplacements))
	}
	if got := cloak.ResponseReplacements[0]; got.Find != " create_task " || got.Replace != " sessions_spawn " {
		t.Fatalf("response replacement[0] = %#v, want \" create_task \" -> \" sessions_spawn \"", got)
	}
	if got := cloak.ResponseReplacements[1]; got.Find != "create_task" || got.Replace != "ignored" {
		t.Fatalf("response replacement[1] = %#v, want create_task -> ignored", got)
	}
}
