package config

import "testing"

func TestSanitizeClaudeOAuthFingerprint_Defaults(t *testing.T) {
	cfg := &Config{}
	cfg.SanitizeClaudeOAuthFingerprint()
	if cfg.ClaudeOAuthFingerprint.Mode != "monitor" {
		t.Fatalf("mode = %q, want monitor", cfg.ClaudeOAuthFingerprint.Mode)
	}
	if cfg.ClaudeOAuthFingerprint.MaxSessions != 4 {
		t.Fatalf("max-sessions = %d, want 4", cfg.ClaudeOAuthFingerprint.MaxSessions)
	}
	if cfg.ClaudeOAuthFingerprint.SessionTTL != "1h" {
		t.Fatalf("session-ttl = %q, want 1h", cfg.ClaudeOAuthFingerprint.SessionTTL)
	}
}
