package config

import (
	"testing"
	"time"
)

func TestParseConfigBytesClaudeCodeCacheKeepaliveDefaults(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	keepalive := cfg.ClaudeCode.CacheKeepalive
	if keepalive.Enabled {
		t.Fatalf("Enabled = true, want false by default")
	}
	if keepalive.BeforeExpiry != 5*time.Minute {
		t.Fatalf("BeforeExpiry = %s, want 5m", keepalive.BeforeExpiry)
	}
	if !keepalive.OnlyWhenAgentsActive {
		t.Fatalf("OnlyWhenAgentsActive = false, want true by default")
	}
	if keepalive.Liveness != ClaudeCodeKeepaliveLivenessClaudeCodeTasks {
		t.Fatalf("Liveness = %q, want %q", keepalive.Liveness, ClaudeCodeKeepaliveLivenessClaudeCodeTasks)
	}
	if keepalive.MaxProbes != 6 {
		t.Fatalf("MaxProbes = %d, want 6", keepalive.MaxProbes)
	}
	if keepalive.MaxTokens != 1 {
		t.Fatalf("MaxTokens = %d, want 1", keepalive.MaxTokens)
	}
	if keepalive.BeforeExpiry5m != 45*time.Second {
		t.Fatalf("BeforeExpiry5m = %s, want 45s", keepalive.BeforeExpiry5m)
	}
	if keepalive.Probe5m != ClaudeCodeKeepaliveProbe5mAuto {
		t.Fatalf("Probe5m = %q, want %q", keepalive.Probe5m, ClaudeCodeKeepaliveProbe5mAuto)
	}
	if len(keepalive.Probe5mModels) != 0 {
		t.Fatalf("Probe5mModels = %v, want empty so the built-in list applies", keepalive.Probe5mModels)
	}
	if keepalive.MaxProbes5m != 30 {
		t.Fatalf("MaxProbes5m = %d, want 30", keepalive.MaxProbes5m)
	}
}

func TestParseConfigBytesClaudeCodeCacheKeepaliveOverrides(t *testing.T) {
	yaml := "claude-code:\n" +
		"  cache-keepalive:\n" +
		"    enabled: true\n" +
		"    before-expiry: 58m\n" +
		"    only-when-agents-active: false\n" +
		"    liveness: always\n" +
		"    max-probes: 2\n" +
		"    max-tokens: 4\n" +
		"    before-expiry-5m: 20s\n" +
		"    probe-5m: always\n" +
		"    max-probes-5m: 12\n" +
		"    probe-5m-models:\n" +
		"      - claude-experimental-9\n" +
		"    task-state-dirs:\n" +
		"      - /tmp/state\n" +
		"    task-output-dirs:\n" +
		"      - /tmp/output\n"
	cfg, errParse := ParseConfigBytes([]byte(yaml))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	keepalive := cfg.ClaudeCode.CacheKeepalive
	if !keepalive.Enabled {
		t.Fatalf("Enabled = false, want true")
	}
	if keepalive.BeforeExpiry != 58*time.Minute {
		t.Fatalf("BeforeExpiry = %s, want 58m", keepalive.BeforeExpiry)
	}
	if keepalive.OnlyWhenAgentsActive {
		t.Fatalf("OnlyWhenAgentsActive = true, want the explicit false to survive defaults")
	}
	if keepalive.Liveness != ClaudeCodeKeepaliveLivenessAlways {
		t.Fatalf("Liveness = %q, want %q", keepalive.Liveness, ClaudeCodeKeepaliveLivenessAlways)
	}
	if keepalive.MaxProbes != 2 {
		t.Fatalf("MaxProbes = %d, want 2", keepalive.MaxProbes)
	}
	if keepalive.MaxTokens != 4 {
		t.Fatalf("MaxTokens = %d, want 4", keepalive.MaxTokens)
	}
	if len(keepalive.TaskStateDirs) != 1 || keepalive.TaskStateDirs[0] != "/tmp/state" {
		t.Fatalf("TaskStateDirs = %v, want [/tmp/state]", keepalive.TaskStateDirs)
	}
	if len(keepalive.TaskOutputDirs) != 1 || keepalive.TaskOutputDirs[0] != "/tmp/output" {
		t.Fatalf("TaskOutputDirs = %v, want [/tmp/output]", keepalive.TaskOutputDirs)
	}
	if keepalive.BeforeExpiry5m != 20*time.Second {
		t.Fatalf("BeforeExpiry5m = %s, want 20s", keepalive.BeforeExpiry5m)
	}
	if keepalive.Probe5m != ClaudeCodeKeepaliveProbe5mAlways {
		t.Fatalf("Probe5m = %q, want %q", keepalive.Probe5m, ClaudeCodeKeepaliveProbe5mAlways)
	}
	if keepalive.MaxProbes5m != 12 {
		t.Fatalf("MaxProbes5m = %d, want 12", keepalive.MaxProbes5m)
	}
	if len(keepalive.Probe5mModels) != 1 || keepalive.Probe5mModels[0] != "claude-experimental-9" {
		t.Fatalf("Probe5mModels = %v, want [claude-experimental-9]", keepalive.Probe5mModels)
	}
}

func TestClaudeCodeCacheKeepaliveValidate(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name:    "disabled skips validation",
			yaml:    "claude-code:\n  cache-keepalive:\n    liveness: nonsense\n",
			wantErr: false,
		},
		{
			name:    "unknown liveness rejected when enabled",
			yaml:    "claude-code:\n  cache-keepalive:\n    enabled: true\n    liveness: nonsense\n",
			wantErr: true,
		},
		{
			name:    "non-positive max-probes rejected",
			yaml:    "claude-code:\n  cache-keepalive:\n    enabled: true\n    max-probes: 0\n",
			wantErr: true,
		},
		{
			name:    "non-positive before-expiry rejected",
			yaml:    "claude-code:\n  cache-keepalive:\n    enabled: true\n    before-expiry: 0s\n",
			wantErr: true,
		},
		{
			name:    "enabled with defaults accepted",
			yaml:    "claude-code:\n  cache-keepalive:\n    enabled: true\n",
			wantErr: false,
		},
		{
			name:    "unknown probe-5m rejected when enabled",
			yaml:    "claude-code:\n  cache-keepalive:\n    enabled: true\n    probe-5m: sometimes\n",
			wantErr: true,
		},
		{
			name:    "before-expiry-5m at or above the 5m ttl rejected",
			yaml:    "claude-code:\n  cache-keepalive:\n    enabled: true\n    before-expiry-5m: 5m\n",
			wantErr: true,
		},
		{
			name:    "non-positive max-probes-5m rejected",
			yaml:    "claude-code:\n  cache-keepalive:\n    enabled: true\n    max-probes-5m: 0\n",
			wantErr: true,
		},
		{
			name:    "probe-5m never ignores the 5m knobs",
			yaml:    "claude-code:\n  cache-keepalive:\n    enabled: true\n    probe-5m: never\n    max-probes-5m: 0\n",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errParse := ParseConfigBytes([]byte(tt.yaml))
			if tt.wantErr && errParse == nil {
				t.Fatalf("ParseConfigBytes() error = nil, want an error")
			}
			if !tt.wantErr && errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v, want nil", errParse)
			}
		})
	}
}
