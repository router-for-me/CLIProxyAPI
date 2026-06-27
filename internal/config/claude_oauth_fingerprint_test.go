package config

import "testing"

func TestSanitizeClaudeOAuthFingerprint_Defaults(t *testing.T) {
	cfg := &Config{}
	cfg.SanitizeClaudeOAuthFingerprint()
	if cfg.ClaudeOAuthFingerprint.MaxSessions != 4 {
		t.Fatalf("max-sessions = %d, want 4", cfg.ClaudeOAuthFingerprint.MaxSessions)
	}
	if cfg.ClaudeOAuthFingerprint.SessionTTL != "1h" {
		t.Fatalf("session-ttl = %q, want 1h", cfg.ClaudeOAuthFingerprint.SessionTTL)
	}
	if cfg.ClaudeOAuthFingerprint.OverrideDevice {
		t.Fatal("override_device should default to false")
	}
	if cfg.ClaudeOAuthFingerprint.GenerateMissingProfile {
		t.Fatal("generate_missing_profile should default to false")
	}
}
