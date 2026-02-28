package fallback

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func intPtr(v int) *int { return &v }

func TestTriggerAfterRetries_InheritsRequestRetryByDefault(t *testing.T) {
	cfg := &config.Config{
		RequestRetry: 3,
		ModelFallback: config.ModelFallback{
			Enabled: true,
		},
	}
	cfg.SanitizeModelFallback()
	if got := TriggerAfterRetries(cfg); got != 3 {
		t.Fatalf("TriggerAfterRetries() = %d, want 3", got)
	}
}

func TestTriggerAfterRetries_UsesExplicitModelFallbackValue(t *testing.T) {
	cfg := &config.Config{
		RequestRetry: 5,
		ModelFallback: config.ModelFallback{
			Enabled:             true,
			TriggerAfterRetries: intPtr(1),
		},
	}
	cfg.SanitizeModelFallback()
	if got := TriggerAfterRetries(cfg); got != 1 {
		t.Fatalf("TriggerAfterRetries() = %d, want 1", got)
	}
}

func TestTriggerAfterRetries_ClampToUpperBound(t *testing.T) {
	cfg := &config.Config{
		RequestRetry: 99,
		ModelFallback: config.ModelFallback{
			Enabled: true,
		},
	}
	cfg.SanitizeModelFallback()
	if got := TriggerAfterRetries(cfg); got != 10 {
		t.Fatalf("TriggerAfterRetries() = %d, want 10", got)
	}
}
