package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCodexConfigRequestCompressionMinBytes(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  *CodexConfig
		want int
	}{
		{name: "nil", want: 1024},
		{name: "zero", cfg: &CodexConfig{}, want: 1024},
		{name: "negative", cfg: &CodexConfig{UpstreamRequestCompressionMinBytes: -1}, want: 1024},
		{name: "explicit", cfg: &CodexConfig{UpstreamRequestCompressionMinBytes: 2048}, want: 2048},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.RequestCompressionMinBytes(); got != tt.want {
				t.Fatalf("RequestCompressionMinBytes()=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestCodexConfigRequestCompressionSerialization(t *testing.T) {
	var cfg CodexConfig
	if cfg.UpstreamRequestCompression {
		t.Fatal("request compression must default to disabled")
	}
	if errUnmarshal := yaml.Unmarshal([]byte("upstream-request-compression: true\nupstream-request-compression-min-bytes: 4096\n"), &cfg); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !cfg.UpstreamRequestCompression || cfg.RequestCompressionMinBytes() != 4096 {
		t.Fatalf("unexpected YAML config: %+v", cfg)
	}
	data, errMarshal := json.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var values map[string]any
	if errUnmarshal := json.Unmarshal(data, &values); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if values["upstream-request-compression"] != true || values["upstream-request-compression-min-bytes"] != float64(4096) {
		t.Fatalf("unexpected JSON: %s", data)
	}
}
