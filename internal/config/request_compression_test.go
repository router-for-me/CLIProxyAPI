package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigBytesNormalizesRequestCompression(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("request-compression: ' AUTO '\nrequest-compression-min-size: '16K'\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if cfg.RequestCompression != RequestCompressionAuto {
		t.Fatalf("mode: got %q, want %q", cfg.RequestCompression, RequestCompressionAuto)
	}
	if cfg.RequestCompressionMinSize != "16k" {
		t.Fatalf("minimum size: got %q, want 16k", cfg.RequestCompressionMinSize)
	}
	if cfg.EffectiveRequestCompressionMinBytes() != 16<<10 {
		t.Fatalf("effective threshold: got %d, want %d", cfg.EffectiveRequestCompressionMinBytes(), 16<<10)
	}
}

func TestRequestCompressionDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("debug: false\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if cfg.RequestCompression != "" {
		t.Fatalf("serialized mode: got %q, want empty", cfg.RequestCompression)
	}
	if cfg.EffectiveRequestCompressionMode() != RequestCompressionOff {
		t.Fatalf("effective mode: got %q, want %q", cfg.EffectiveRequestCompressionMode(), RequestCompressionOff)
	}
	if cfg.EffectiveRequestCompressionMinBytes() != DefaultRequestCompressionMinBytes {
		t.Fatalf("effective threshold: got %d, want %d", cfg.EffectiveRequestCompressionMinBytes(), DefaultRequestCompressionMinBytes)
	}
}

func TestRequestCompressionRejectsInvalidValues(t *testing.T) {
	for _, data := range []string{
		"request-compression: gzip\n",
		"request-compression: zstd\n",
		"request-compression: br\n",
		"request-compression-min-size: 0k\n",
		"request-compression-min-size: 16\n",
		"request-compression-min-size: 16kb\n",
		"request-compression-min-size: 1.5k\n",
		"request-compression-min-size: -1k\n",
		"request-compression-min-size: 999999999999999999999999999999999999999k\n",
	} {
		if _, err := ParseConfigBytes([]byte(data)); err == nil {
			t.Fatalf("ParseConfigBytes(%q): expected error", data)
		}
	}
}

func TestLoadConfigNormalizesRequestCompression(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("request-compression: OFF\nrequest-compression-min-size: 32K\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.RequestCompression != RequestCompressionOff {
		t.Fatalf("mode: got %q, want %q", cfg.RequestCompression, RequestCompressionOff)
	}
	if cfg.RequestCompressionMinSize != "32k" {
		t.Fatalf("minimum size: got %q, want 32k", cfg.RequestCompressionMinSize)
	}
	if cfg.EffectiveRequestCompressionMinBytes() != 32<<10 {
		t.Fatalf("effective threshold: got %d, want %d", cfg.EffectiveRequestCompressionMinBytes(), 32<<10)
	}
}
