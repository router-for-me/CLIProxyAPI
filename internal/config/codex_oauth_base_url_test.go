package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadConfigCodexOAuthBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "absent"},
		{name: "empty", value: `""`},
		{name: "http", value: "http://example.com/", want: "http://example.com"},
		{name: "uppercase scheme", value: "HTTPS://example.com/codex/", want: "HTTPS://example.com/codex"},
		{name: "https path", value: "https://example.com/backend/codex///", want: "https://example.com/backend/codex"},
		{name: "cloud endpoint", value: "https://api.edgee.ai/v1", want: "https://api.edgee.ai/v1"},
		{name: "relative", value: "/backend/codex", wantErr: true},
		{name: "unsupported scheme", value: "ftp://example.com/backend/codex", wantErr: true},
		{name: "empty host", value: "https:///backend/codex", wantErr: true},
		{name: "empty hostname", value: "http://:8080/backend/codex", wantErr: true},
		{name: "userinfo", value: "https://user:pass@example.com/backend/codex", wantErr: true},
		{name: "query", value: "https://example.com/backend/codex?mode=test", wantErr: true},
		{name: "empty query", value: "https://example.com/backend/codex?", wantErr: true},
		{name: "fragment", value: "https://example.com/backend/codex#section", wantErr: true},
		{name: "empty fragment", value: "https://example.com/backend/codex#", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			contents := "host: 127.0.0.1\n"
			if tt.value != "" {
				contents += "codex-oauth-base-url: " + tt.value + "\n"
			}
			if errWrite := os.WriteFile(configPath, []byte(contents), 0o600); errWrite != nil {
				t.Fatalf("write config: %v", errWrite)
			}

			cfg, errLoad := LoadConfig(configPath)
			if tt.wantErr {
				if errLoad == nil {
					t.Fatal("LoadConfig() error = nil, want validation error")
				}
				if !strings.Contains(errLoad.Error(), "codex-oauth-base-url") {
					t.Fatalf("LoadConfig() error = %q, want key name", errLoad)
				}
				return
			}
			if errLoad != nil {
				t.Fatalf("LoadConfig() error = %v", errLoad)
			}
			if cfg.CodexOAuthBaseURL != tt.want {
				t.Fatalf("CodexOAuthBaseURL = %q, want %q", cfg.CodexOAuthBaseURL, tt.want)
			}
			if len(cfg.CodexOAuthHeaders) != 0 {
				t.Fatalf("CodexOAuthHeaders = %#v, want empty", cfg.CodexOAuthHeaders)
			}
		})
	}
}

func TestLoadConfigCodexOAuthHeaders(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantValue string
		wantErr   bool
	}{
		{name: "valid", header: "x-edgee-api-key", wantValue: "edge-key"},
		{name: "all token punctuation", header: "!#$%&'*+-.^_`|~", wantValue: "edge-key"},
		{name: "space", header: "x bad", wantErr: true},
		{name: "colon", header: "x:bad", wantErr: true},
		{name: "at sign", header: "x@bad", wantErr: true},
		{name: "empty", header: "", wantErr: true},
		{name: "authorization", header: "Authorization", wantErr: true},
		{name: "host", header: "HOST", wantErr: true},
		{name: "content length", header: "content-length", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			contents := "codex-oauth-base-url: https://api.edgee.ai/v1\n" +
				"codex-oauth-headers:\n  " + strconv.Quote(tt.header) + ": edge-key\n"
			if errWrite := os.WriteFile(configPath, []byte(contents), 0o600); errWrite != nil {
				t.Fatalf("write config: %v", errWrite)
			}

			cfg, errLoad := LoadConfig(configPath)
			if tt.wantErr {
				if errLoad == nil {
					t.Fatal("LoadConfig() error = nil, want validation error")
				}
				if !strings.Contains(errLoad.Error(), "codex-oauth-headers") || !strings.Contains(errLoad.Error(), strconv.Quote(tt.header)) {
					t.Fatalf("LoadConfig() error = %q, want key and offending header %q", errLoad, tt.header)
				}
				return
			}
			if errLoad != nil {
				t.Fatalf("LoadConfig() error = %v", errLoad)
			}
			if cfg.CodexOAuthHeaders[tt.header] != tt.wantValue {
				t.Fatalf("CodexOAuthHeaders[%q] = %q, want %q", tt.header, cfg.CodexOAuthHeaders[tt.header], tt.wantValue)
			}
		})
	}
}

func TestLoadConfigRejectsCodexOAuthHeadersWithoutBaseURL(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("codex-oauth-headers:\n  x-edgee-api-key: edge-key\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	_, errLoad := LoadConfig(configPath)
	if errLoad == nil {
		t.Fatal("LoadConfig() error = nil, want headers-only validation error")
	}
	if !strings.Contains(errLoad.Error(), "codex-oauth-headers") || !strings.Contains(errLoad.Error(), "codex-oauth-base-url") {
		t.Fatalf("LoadConfig() error = %q, want both config keys", errLoad)
	}
}

func TestParseConfigBytesValidatesCodexOAuthConfig(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("codex-oauth-base-url: https://edge.example.com/codex/\ncodex-oauth-headers:\n  x-edgee-api-key: edge-key\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.CodexOAuthBaseURL != "https://edge.example.com/codex" {
		t.Fatalf("CodexOAuthBaseURL = %q, want trimmed URL", cfg.CodexOAuthBaseURL)
	}
	if cfg.CodexOAuthHeaders["x-edgee-api-key"] != "edge-key" {
		t.Fatalf("CodexOAuthHeaders = %#v, want configured header", cfg.CodexOAuthHeaders)
	}

	_, errParse = ParseConfigBytes([]byte("codex-oauth-base-url: relative/path\n"))
	if errParse == nil || !strings.Contains(errParse.Error(), "codex-oauth-base-url") {
		t.Fatalf("ParseConfigBytes() error = %v, want key-specific validation error", errParse)
	}

	_, errParse = ParseConfigBytes([]byte("codex-oauth-headers:\n  x-edgee-api-key: edge-key\n"))
	if errParse == nil || !strings.Contains(errParse.Error(), "codex-oauth-headers") {
		t.Fatalf("ParseConfigBytes() error = %v, want headers-only validation error", errParse)
	}
}
