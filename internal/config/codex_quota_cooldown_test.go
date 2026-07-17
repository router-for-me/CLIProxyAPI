package config

import "testing"

func TestParseConfigBytes_CodexQuotaCooldown(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
codex:
  quota-cooldown:
    enabled: true
    unauthorized-cooldown-seconds: 10800
    transient-backoff-seconds: [15, 30, 60, 120, 300]
    jitter-percent: 20
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() returned error: %v", errParse)
	}
	got := cfg.Codex.QuotaCooldown
	if !got.Enabled || got.UnauthorizedCooldownSeconds != 10800 || got.JitterPercent != 20 {
		t.Fatalf("quota cooldown = %+v", got)
	}
	if len(got.TransientBackoffSeconds) != 5 || got.TransientBackoffSeconds[4] != 300 {
		t.Fatalf("transient backoff = %v", got.TransientBackoffSeconds)
	}
}

func TestParseConfigBytes_CodexQuotaCooldownDefaultsDisabled(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() returned error: %v", errParse)
	}
	if cfg.Codex.QuotaCooldown.Enabled {
		t.Fatal("Codex quota cooldown should be disabled by default")
	}
}
