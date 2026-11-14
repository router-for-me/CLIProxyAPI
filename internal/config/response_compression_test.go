package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResponseCompressionConfig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		enabled  bool
		minBytes int
	}{
		{name: "defaults", input: "{}", enabled: true, minBytes: 1024},
		{name: "disabled", input: "server:\n  response-compression:\n    enabled: false\n    min-bytes: 64", minBytes: 64},
		{name: "enabled", input: "server:\n  response-compression:\n    enabled: true\n    min-bytes: -1", enabled: true, minBytes: 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if errUnmarshal := yaml.Unmarshal([]byte(tc.input), &cfg); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if got := cfg.ServerResponseCompression.IsEnabled(); got != tc.enabled {
				t.Fatalf("IsEnabled() = %v, want %v", got, tc.enabled)
			}
			if got := cfg.ServerResponseCompression.EffectiveMinBytes(); got != tc.minBytes {
				t.Fatalf("EffectiveMinBytes() = %d, want %d", got, tc.minBytes)
			}
			encoded, errMarshal := json.Marshal(cfg)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			var roundTrip Config
			if errUnmarshal := json.Unmarshal(encoded, &roundTrip); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if roundTrip.ServerResponseCompression.IsEnabled() != tc.enabled || roundTrip.ServerResponseCompression.EffectiveMinBytes() != tc.minBytes {
				t.Fatal("JSON round trip changed compression configuration")
			}
		})
	}
}

func TestCodexWebsocketCompressionConfig(t *testing.T) {
	var nilConfig *CodexConfig
	if !nilConfig.WebsocketCompressionEnabled() {
		t.Fatal("nil configuration must enable WebSocket compression")
	}
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{input: "{}", want: true},
		{input: "codex:\n  websocket-compression: true", want: true},
		{input: "codex:\n  websocket-compression: false", want: false},
	} {
		var cfg Config
		if errUnmarshal := yaml.Unmarshal([]byte(tc.input), &cfg); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		if got := cfg.Codex.WebsocketCompressionEnabled(); got != tc.want {
			t.Fatalf("WebsocketCompressionEnabled() = %v, want %v", got, tc.want)
		}
	}
}
