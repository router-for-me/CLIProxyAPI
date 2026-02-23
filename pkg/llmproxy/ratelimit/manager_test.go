package ratelimit

import (
	"encoding/json"
	"testing"
)

func TestParseRateLimitConfigFromMap_AliasKeys(t *testing.T) {
	cfg := ParseRateLimitConfigFromMap(map[string]interface{}{
		"requests_per_minute": json.Number("60"),
		"TokensPerMinute":     "120",
		"requests_per_day":    300.0,
		"tokensperday":        480,
		"wait-on-limit":       true,
		"max-wait-seconds":    45.0,
	})

	if cfg.RPM != 60 {
		t.Fatalf("RPM = %d, want %d", cfg.RPM, 60)
	}
	if cfg.TPM != 120 {
		t.Fatalf("TPM = %d, want %d", cfg.TPM, 120)
	}
	if cfg.RPD != 300 {
		t.Fatalf("RPD = %d, want %d", cfg.RPD, 300)
	}
	if cfg.TPD != 480 {
		t.Fatalf("TPD = %d, want %d", cfg.TPD, 480)
	}
	if !cfg.WaitOnLimit {
		t.Fatal("WaitOnLimit = false, want true")
	}
	if cfg.MaxWaitSeconds != 45 {
		t.Fatalf("MaxWaitSeconds = %d, want %d", cfg.MaxWaitSeconds, 45)
	}
}
