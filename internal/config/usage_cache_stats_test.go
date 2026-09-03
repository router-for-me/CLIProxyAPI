package config

import (
	"testing"
	"time"
)

func TestParseConfigBytesUsageCacheStatsDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("usage-cache-stats:\n  enabled: true\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error = %v", err)
	}
	stats := cfg.UsageCacheStats
	if !stats.Enabled {
		t.Error("Enabled = false, want true")
	}
	if stats.MaxSessions != defaultUsageCacheStatsMaxSessions {
		t.Errorf("MaxSessions = %d, want %d", stats.MaxSessions, defaultUsageCacheStatsMaxSessions)
	}
	if stats.PerSessionRequests != defaultUsageCacheStatsPerSessionRequest {
		t.Errorf("PerSessionRequests = %d, want %d", stats.PerSessionRequests, defaultUsageCacheStatsPerSessionRequest)
	}
	if stats.IdleTTL != defaultUsageCacheStatsIdleTTL {
		t.Errorf("IdleTTL = %s, want %s", stats.IdleTTL, defaultUsageCacheStatsIdleTTL)
	}
}

func TestParseConfigBytesUsageCacheStatsDisabledByDefault(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("debug: false\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error = %v", err)
	}
	if cfg.UsageCacheStats.Enabled {
		t.Error("UsageCacheStats.Enabled = true, want false for an omitted block")
	}
}

func TestParseConfigBytesUsageCacheStatsOverrides(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("usage-cache-stats:\n  enabled: true\n  max-sessions: 12\n  per-session-requests: 7\n  idle-ttl: 90m\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error = %v", err)
	}
	stats := cfg.UsageCacheStats
	if stats.MaxSessions != 12 || stats.PerSessionRequests != 7 || stats.IdleTTL != 90*time.Minute {
		t.Fatalf("overrides not applied: %+v", stats)
	}
}

func TestUsageCacheStatsValidate(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{name: "disabled ignores zeros", yaml: "usage-cache-stats:\n  max-sessions: 0\n"},
		{name: "enabled defaults are valid", yaml: "usage-cache-stats:\n  enabled: true\n"},
		{name: "zero max sessions", yaml: "usage-cache-stats:\n  enabled: true\n  max-sessions: 0\n", wantErr: true},
		{name: "zero per session requests", yaml: "usage-cache-stats:\n  enabled: true\n  per-session-requests: 0\n", wantErr: true},
		{name: "zero idle ttl", yaml: "usage-cache-stats:\n  enabled: true\n  idle-ttl: 0s\n", wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ParseConfigBytes([]byte(testCase.yaml))
			if testCase.wantErr && err == nil {
				t.Fatal("ParseConfigBytes succeeded, want error")
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("ParseConfigBytes error = %v", err)
			}
		})
	}
}
