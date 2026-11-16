package config

import (
	"math"
	"testing"
	"time"
)

// TestSDKConfigClaudeAwaitKeepAlive pins the opt-in contract: a positive
// value in seconds enables the keepalive, while zero or negative values
// disable it.
func TestSDKConfigClaudeAwaitKeepAlive(t *testing.T) {
	tests := []struct {
		name string
		cfg  *SDKConfig
		want time.Duration
	}{
		{name: "nil receiver disables", cfg: nil, want: 0},
		{name: "zero disables", cfg: &SDKConfig{ClaudeAwaitKeepAliveSeconds: 0}, want: 0},
		{name: "negative disables", cfg: &SDKConfig{ClaudeAwaitKeepAliveSeconds: -5}, want: 0},
		{name: "positive seconds convert", cfg: &SDKConfig{ClaudeAwaitKeepAliveSeconds: 40}, want: 40 * time.Second},
		{name: "overflowing seconds saturate", cfg: &SDKConfig{ClaudeAwaitKeepAliveSeconds: math.MaxInt}, want: time.Duration(math.MaxInt64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.ClaudeAwaitKeepAlive(); got != tt.want {
				t.Fatalf("ClaudeAwaitKeepAlive() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseConfigBytesRequestsClaudeAwaitKeepAlive ensures the setting lives
// under requests in the v8 layout and keeps the flat legacy spelling.
func TestParseConfigBytesRequestsClaudeAwaitKeepAlive(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{name: "canonical layout", yaml: "requests:\n  claude-await-keepalive-seconds: 40\n", want: 40},
		{name: "legacy flat layout", yaml: "claude-await-keepalive-seconds: 30\n", want: 30},
		{name: "unset defaults to disabled", yaml: "requests:\n  proxy-url: \"\"\n", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if cfg.ClaudeAwaitKeepAliveSeconds != tt.want {
				t.Fatalf("claude-await-keepalive-seconds = %d, want %d", cfg.ClaudeAwaitKeepAliveSeconds, tt.want)
			}
		})
	}
}
