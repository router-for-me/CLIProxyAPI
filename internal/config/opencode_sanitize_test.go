package config

import "testing"

// TestSanitizeOpenCodeKeysPreservesEntryWithoutBaseURL is the root-cause guard
// for "OpenCode models absent from /v1/models". dc8 configures
// `opencode-api-key:` WITHOUT a base-url; the previous sanitizer dropped such
// entries (reusing the Codex rule), which produced 0 OpenCode auths and removed
// OpenCode from /v1/models. The OpenCode executor supplies a gateway default
// base-url (https://opencode.ai/zen -> https://opencode.ai/zen/go), so an
// empty BaseURL is valid and must survive config load.
func TestSanitizeOpenCodeKeysPreservesEntryWithoutBaseURL(t *testing.T) {
	cfg := &Config{
		OpenCodeKey: []OpenCodeKey{
			{
				APIKey: "opencode-validation-key",
				Prefix: " dev ",
			},
		},
	}
	cfg.SanitizeOpenCodeKeys()
	if got := len(cfg.OpenCodeKey); got != 1 {
		t.Fatalf("OpenCodeKey entries after sanitize = %d, want 1 (no-base-url must survive)", got)
	}
	if cfg.OpenCodeKey[0].APIKey != "opencode-validation-key" {
		t.Errorf("APIKey = %q, want preserved", cfg.OpenCodeKey[0].APIKey)
	}
	if cfg.OpenCodeKey[0].Prefix != "dev" {
		t.Errorf("Prefix = %q, want trimmed 'dev'", cfg.OpenCodeKey[0].Prefix)
	}
	if cfg.OpenCodeKey[0].BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty (executor defaults it at Execute time)", cfg.OpenCodeKey[0].BaseURL)
	}
}

// TestSanitizeCodexKeysStillDropsEntryWithoutBaseURL is a regression guard: the
// refactor that made OpenCode preserve empty base-urls must NOT change Codex /
// Poolside / XAI behavior (those still use the drop-empty-base-url path).
func TestSanitizeCodexKeysStillDropsEntryWithoutBaseURL(t *testing.T) {
	cfg := &Config{
		CodexKey: []CodexKey{
			{APIKey: "codex-key", Prefix: "dev"},
		},
	}
	cfg.SanitizeCodexKeys()
	if got := len(cfg.CodexKey); got != 0 {
		t.Fatalf("CodexKey entries after sanitize = %d, want 0 (no-base-url codex dropped)", got)
	}
}
