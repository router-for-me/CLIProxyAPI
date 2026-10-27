package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProxyPoolsAcceptsValidConfiguration(t *testing.T) {
	t.Parallel()

	pools := []ProxyPoolConfig{
		{
			ID:       "pool-us",
			Strategy: "sticky",
			HealthCheck: ProxyPoolHealthCheckConfig{
				Enabled:         true,
				TestURL:         "https://api.anthropic.com",
				IntervalSeconds: 60,
				TimeoutSeconds:  5,
			},
			Nodes: []string{
				"http://user:pass@proxy-a.example.com:8080",
				"https://proxy-b.example.com:8443",
				"socks5://user:pass@proxy-c.example.com:1080",
				"socks5h://proxy-d.example.com:1080",
			},
		},
		{ID: "pool-eu", Strategy: "round-robin", Nodes: []string{"socks5://proxy-e.example.com:1080"}},
	}
	if errValidate := ValidateProxyPools(pools); errValidate != nil {
		t.Fatalf("ValidateProxyPools() error = %v", errValidate)
	}
}

func TestValidateProxyPoolsRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pools   []ProxyPoolConfig
		wantErr string
	}{
		{
			name:    "empty id",
			pools:   []ProxyPoolConfig{{ID: " ", Nodes: []string{"http://proxy.example.com:8080"}}},
			wantErr: "id is required",
		},
		{
			name: "duplicate id",
			pools: []ProxyPoolConfig{
				{ID: "pool-a", Nodes: []string{"http://proxy.example.com:8080"}},
				{ID: "pool-a", Nodes: []string{"http://proxy.example.com:8081"}},
			},
			wantErr: "duplicate id",
		},
		{
			name:    "empty nodes",
			pools:   []ProxyPoolConfig{{ID: "pool-a"}},
			wantErr: "nodes must not be empty",
		},
		{
			name:    "blank node",
			pools:   []ProxyPoolConfig{{ID: "pool-a", Nodes: []string{" "}}},
			wantErr: "is empty",
		},
		{
			name:    "invalid strategy",
			pools:   []ProxyPoolConfig{{ID: "pool-a", Strategy: "random", Nodes: []string{"http://proxy.example.com:8080"}}},
			wantErr: "unsupported strategy",
		},
		{
			name:    "invalid scheme",
			pools:   []ProxyPoolConfig{{ID: "pool-a", Nodes: []string{"ftp://proxy.example.com:21"}}},
			wantErr: "invalid proxy URL",
		},
		{
			name:    "direct pseudo proxy",
			pools:   []ProxyPoolConfig{{ID: "pool-a", Nodes: []string{"direct"}}},
			wantErr: "unsupported proxy URL",
		},
		{
			name: "invalid health check url",
			pools: []ProxyPoolConfig{{
				ID:          "pool-a",
				Nodes:       []string{"http://proxy.example.com:8080"},
				HealthCheck: ProxyPoolHealthCheckConfig{Enabled: true, TestURL: "ftp://example.com"},
			}},
			wantErr: "test-url must use http or https",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errValidate := ValidateProxyPools(tc.pools)
			if errValidate == nil {
				t.Fatalf("ValidateProxyPools() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(errValidate.Error(), tc.wantErr) {
				t.Fatalf("ValidateProxyPools() error = %v, want containing %q", errValidate, tc.wantErr)
			}
		})
	}
}

func TestParseConfigBytesProxyPools(t *testing.T) {
	t.Parallel()

	cfg, errParse := ParseConfigBytes([]byte(`
proxy-pools:
  - id: "pool-us"
    strategy: "sticky"
    health-check:
      enabled: true
      test-url: "https://api.anthropic.com"
      interval-seconds: 60
      timeout-seconds: 5
    nodes:
      - "socks5://user:pass@proxy-a.example.com:1080"
  - id: "pool-eu"
    strategy: "round-robin"
    nodes:
      - "http://proxy-b.example.com:8080"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.ProxyPools) != 2 {
		t.Fatalf("len(ProxyPools) = %d, want 2", len(cfg.ProxyPools))
	}
	pool := cfg.ProxyPools[0]
	if pool.ID != "pool-us" || pool.Strategy != "sticky" || len(pool.Nodes) != 1 {
		t.Fatalf("pool[0] = %+v", pool)
	}
	if !pool.HealthCheck.Enabled || pool.HealthCheck.TestURL != "https://api.anthropic.com" || pool.HealthCheck.IntervalSeconds != 60 || pool.HealthCheck.TimeoutSeconds != 5 {
		t.Fatalf("pool[0].HealthCheck = %+v", pool.HealthCheck)
	}
}

func TestLoadConfigOptionalKeepsProxyPools(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
proxy-pools:
  - id: "pool-us"
    strategy: "sticky"
    health-check:
      enabled: true
      test-url: "https://api.anthropic.com"
      interval-seconds: 60
      timeout-seconds: 5
    nodes:
      - "socks5://user:pass@proxy-a.example.com:1080"
`
	if errWrite := os.WriteFile(path, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	cfg, errLoad := LoadConfigOptional(path, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if len(cfg.ProxyPools) != 1 || cfg.ProxyPools[0].ID != "pool-us" {
		t.Fatalf("ProxyPools = %+v, want pool-us", cfg.ProxyPools)
	}
	onDisk, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if !bytes.Contains(onDisk, []byte("proxy-pools")) {
		t.Fatalf("LoadConfigOptional() rewrote away proxy-pools:\n%s", onDisk)
	}
}

func TestValidateV8ConfigAcceptsProxyPools(t *testing.T) {
	t.Parallel()

	data := []byte(`
config-version: 8
proxy-pools:
  - id: "pool-us"
    strategy: "sticky"
    health-check:
      enabled: true
      test-url: "https://api.anthropic.com"
      interval-seconds: 60
      timeout-seconds: 5
    nodes:
      - "socks5://proxy-a.example.com:1080"
`)
	if errValidate := ValidateV8Config(data); errValidate != nil {
		t.Fatalf("ValidateV8Config() error = %v", errValidate)
	}
}

func TestNormalizeConfigLayoutPreservesProxyPools(t *testing.T) {
	t.Parallel()

	data := []byte(`
config-version: 8
proxy-pools:
  - id: "pool-us"
    nodes:
      - "http://proxy-a.example.com:8080"
`)
	normalized, _, errNormalize := NormalizeConfigLayout(data, true)
	if errNormalize != nil {
		t.Fatalf("NormalizeConfigLayout() error = %v", errNormalize)
	}
	if !bytes.Contains(normalized, []byte("proxy-pools")) {
		t.Fatalf("NormalizeConfigLayout() dropped proxy-pools:\n%s", normalized)
	}
}

func TestParseConfigBytesRejectsInvalidProxyPools(t *testing.T) {
	t.Parallel()

	_, errParse := ParseConfigBytes([]byte(`
proxy-pools:
  - id: "pool-us"
    strategy: "sticky"
    nodes:
      - "socks5://proxy-a.example.com"
  - id: "pool-us"
    nodes:
      - "http://proxy-b.example.com:8080"
`))
	if errParse == nil {
		t.Fatal("ParseConfigBytes() = nil error, want duplicate id rejection")
	}
	if !strings.Contains(errParse.Error(), "duplicate id") {
		t.Fatalf("ParseConfigBytes() error = %v, want duplicate id", errParse)
	}
}
