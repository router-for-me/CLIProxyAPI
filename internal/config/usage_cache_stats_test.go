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

func TestParseConfigBytesUsageCacheStatsAlertDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("usage-cache-stats:\n  enabled: true\n  alert:\n    enabled: true\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error = %v", err)
	}
	alert := cfg.UsageCacheStats.Alert
	if !alert.Enabled {
		t.Error("alert.Enabled = false, want true")
	}
	if alert.LostTokensPerHour != defaultUsageCacheStatsAlertLostPerHour {
		t.Errorf("alert.LostTokensPerHour = %d, want %d", alert.LostTokensPerHour, defaultUsageCacheStatsAlertLostPerHour)
	}
}

func TestParseConfigBytesUsageCacheStatsAlertOverride(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("usage-cache-stats:\n  enabled: true\n  alert:\n    enabled: true\n    lost-tokens-per-hour: 250000\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error = %v", err)
	}
	if got := cfg.UsageCacheStats.Alert.LostTokensPerHour; got != 250000 {
		t.Fatalf("alert.LostTokensPerHour = %d, want 250000", got)
	}
}

func TestUsageCacheStatsAlertValidate(t *testing.T) {
	_, err := ParseConfigBytes([]byte("usage-cache-stats:\n  enabled: true\n  alert:\n    enabled: true\n    lost-tokens-per-hour: 0\n"))
	if err == nil {
		t.Fatal("ParseConfigBytes succeeded for a zero threshold, want error")
	}
	// A disabled alert keeps a zero threshold legal.
	if _, err := ParseConfigBytes([]byte("usage-cache-stats:\n  enabled: true\n  alert:\n    lost-tokens-per-hour: 0\n")); err != nil {
		t.Fatalf("ParseConfigBytes error for a disabled alert = %v", err)
	}
}
