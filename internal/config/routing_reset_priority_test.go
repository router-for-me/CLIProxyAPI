package config

import "testing"

func TestParseConfigResetPriorityRouting(t *testing.T) {
	raw := []byte(`routing:
  strategy: reset-priority
  reset-priority-urgent-seconds: 1200
  reset-priority-jitter-seconds: 30
  reset-priority-fallback: round-robin
`)
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.Routing.Strategy != "reset-priority" {
		t.Fatalf("routing.strategy = %q, want reset-priority", cfg.Routing.Strategy)
	}
	if cfg.Routing.ResetPriorityUrgentSeconds != 1200 {
		t.Fatalf("reset-priority-urgent-seconds = %d, want 1200", cfg.Routing.ResetPriorityUrgentSeconds)
	}
	if cfg.Routing.ResetPriorityJitterSeconds != 30 {
		t.Fatalf("reset-priority-jitter-seconds = %d, want 30", cfg.Routing.ResetPriorityJitterSeconds)
	}
	if cfg.Routing.ResetPriorityFallback != "round-robin" {
		t.Fatalf("reset-priority-fallback = %q, want round-robin", cfg.Routing.ResetPriorityFallback)
	}
}

func TestParseConfigResetPriorityRoutingDefaultsRemainZero(t *testing.T) {
	raw := []byte(`routing:
  strategy: reset-priority
`)
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	// Runtime defaults (900s / 60s / weighted) are applied when the selector is
	// constructed, so the parsed config keeps zero values for unset keys.
	if cfg.Routing.ResetPriorityUrgentSeconds != 0 ||
		cfg.Routing.ResetPriorityJitterSeconds != 0 ||
		cfg.Routing.ResetPriorityFallback != "" {
		t.Fatalf("unexpected defaults parsed: %+v", cfg.Routing)
	}
}
