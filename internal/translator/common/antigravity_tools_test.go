package common

import (
	"testing"
)

func TestAntigravityToolNameToUpstream(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"read_file", "external_read_file"},
		{"write_file", "external_write_file"},
		{"execute_code", "external_execute_code"},
		{"web_search", "web_search"},
		{"get_weather", "get_weather"},
	}

	for _, tt := range tests {
		if got := AntigravityToolNameToUpstream(tt.input); got != tt.want {
			t.Errorf("AntigravityToolNameToUpstream(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAntigravityUpstreamToolNameToClient(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"external_read_file", "read_file"},
		{"external_write_file", "write_file"},
		{"external_execute_code", "execute_code"},
		{"external_weather", "external_weather"},
		{"external_lookup", "external_lookup"},
		{"web_search", "web_search"},
		{"get_weather", "get_weather"},
	}

	for _, tt := range tests {
		if got := AntigravityUpstreamToolNameToClient(tt.input); got != tt.want {
			t.Errorf("AntigravityUpstreamToolNameToClient(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
