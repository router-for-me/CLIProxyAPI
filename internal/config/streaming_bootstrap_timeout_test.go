package config

import (
	"testing"
	"time"
)

func TestStreamingConfig_BootstrapTimeoutDuration(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "empty", raw: "", want: 0},
		{name: "zero", raw: "0", want: 0},
		{name: "off", raw: "off", want: 0},
		{name: "none", raw: "none", want: 0},
		{name: "never", raw: "never", want: 0},
		{name: "unlimited", raw: "unlimited", want: 0},
		{name: "disabled", raw: "disabled", want: 0},
		{name: "unparsable", raw: "soon", want: 0},
		{name: "seconds", raw: "90", want: 90 * time.Second},
		{name: "duration", raw: "90s", want: 90 * time.Second},
		{name: "compound duration", raw: "1m30s", want: 90 * time.Second},
		{name: "sub-second duration", raw: "500ms", want: 500 * time.Millisecond},
		{name: "padded duration", raw: " 45s ", want: 45 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &StreamingConfig{BootstrapTimeout: tt.raw}
			if got := cfg.BootstrapTimeoutDuration(); got != tt.want {
				t.Errorf("BootstrapTimeoutDuration(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}

	var nilCfg *StreamingConfig
	if got := nilCfg.BootstrapTimeoutDuration(); got != DefaultStreamingBootstrapTimeout {
		t.Errorf("nil BootstrapTimeoutDuration() = %v, want %v", got, DefaultStreamingBootstrapTimeout)
	}
}
