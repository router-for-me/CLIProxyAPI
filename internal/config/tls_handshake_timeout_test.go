package config

import (
	"math"
	"testing"
	"time"
)

// TestSDKConfigTLSHandshakeTimeout pins the Issue #6615 contract for the
// opt-in upstream TLS handshake bound: a positive value in seconds enables
// the bound, while zero or negative values disable it.
func TestSDKConfigTLSHandshakeTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  *SDKConfig
		want time.Duration
	}{
		{name: "nil receiver disables", cfg: nil, want: 0},
		{name: "zero disables", cfg: &SDKConfig{TLSHandshakeTimeoutSeconds: 0}, want: 0},
		{name: "negative disables", cfg: &SDKConfig{TLSHandshakeTimeoutSeconds: -5}, want: 0},
		{name: "positive seconds convert", cfg: &SDKConfig{TLSHandshakeTimeoutSeconds: 10}, want: 10 * time.Second},
		{name: "overflowing seconds saturate", cfg: &SDKConfig{TLSHandshakeTimeoutSeconds: math.MaxInt}, want: time.Duration(math.MaxInt64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.TLSHandshakeTimeout(); got != tt.want {
				t.Fatalf("TLSHandshakeTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseConfigBytesRequestsTLSHandshakeTimeout ensures the canonical v8
// layout exposes the setting under requests and parses as integer seconds.
func TestParseConfigBytesRequestsTLSHandshakeTimeout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
requests:
  tls-handshake-timeout-seconds: 12
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.TLSHandshakeTimeoutSeconds != 12 {
		t.Fatalf("tls-handshake-timeout-seconds = %d, want 12", cfg.TLSHandshakeTimeoutSeconds)
	}
	if got := cfg.TLSHandshakeTimeout(); got != 12*time.Second {
		t.Fatalf("TLSHandshakeTimeout() = %v, want 12s", got)
	}
}

// TestParseConfigBytesRequestsTLSHandshakeTimeoutLegacyLayout ensures the
// pre-v8 flat layout maps tls-handshake-timeout-seconds onto the same field.
func TestParseConfigBytesRequestsTLSHandshakeTimeoutLegacyLayout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
tls-handshake-timeout-seconds: 7
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.TLSHandshakeTimeoutSeconds != 7 {
		t.Fatalf("tls-handshake-timeout-seconds = %d, want 7", cfg.TLSHandshakeTimeoutSeconds)
	}
	if got := cfg.TLSHandshakeTimeout(); got != 7*time.Second {
		t.Fatalf("TLSHandshakeTimeout() = %v, want 7s", got)
	}
}

// TestParseConfigBytesRequestsTLSHandshakeTimeoutPrecedence ensures the
// canonical requests value, including explicit zero, wins over the flat
// spelling when a partially migrated document contains both.
func TestParseConfigBytesRequestsTLSHandshakeTimeoutPrecedence(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{
			name: "canonical positive wins",
			yaml: "tls-handshake-timeout-seconds: 7\nrequests:\n  tls-handshake-timeout-seconds: 12\n",
			want: 12,
		},
		{
			name: "canonical explicit zero wins",
			yaml: "tls-handshake-timeout-seconds: 7\nrequests:\n  tls-handshake-timeout-seconds: 0\n",
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(tt.yaml))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if cfg.TLSHandshakeTimeoutSeconds != tt.want {
				t.Fatalf("tls-handshake-timeout-seconds = %d, want %d", cfg.TLSHandshakeTimeoutSeconds, tt.want)
			}
		})
	}
}

// TestParseConfigBytesUpstreamClaudeTLSHandshakeTimeoutIgnored pins the
// relocation: the never-released upstream.claude spelling must not remain
// a second functional entry point after moving to requests.
func TestParseConfigBytesUpstreamClaudeTLSHandshakeTimeoutIgnored(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
upstream:
  claude:
    tls-handshake-timeout-seconds: 99
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.TLSHandshakeTimeoutSeconds != 0 {
		t.Fatalf("requests tls-handshake-timeout-seconds = %d, want 0 from retired upstream.claude spelling", cfg.TLSHandshakeTimeoutSeconds)
	}
}
