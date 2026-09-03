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
