package config

import (
	"testing"
)

func TestParseConfigBytesRemoteManagementBaseURL(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`remote-management:
  allow-remote: true
  base-url: "https://proxy.example.com"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if cfg.RemoteManagement.BaseURL != "https://proxy.example.com" {
		t.Fatalf("RemoteManagement.BaseURL = %q, want https://proxy.example.com", cfg.RemoteManagement.BaseURL)
	}
}
