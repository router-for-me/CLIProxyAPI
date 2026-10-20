package config

import "testing"

func TestUsageAPIConfigDefaultsDisabled(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.UsageAPI.Enabled {
		t.Fatal("usage-api must default to disabled")
	}
	if len(cfg.UsageAPI.Providers) != 0 || len(cfg.UsageAPI.Credentials) != 0 {
		t.Fatalf("usage-api defaults = %#v, want empty allowlists", cfg.UsageAPI)
	}
}

func TestUsageAPIConfigAcceptsV8Layout(t *testing.T) {
	data := []byte(`config-version: 8
observability:
  usage:
    user-api:
      enabled: true
      providers:
        - Claude
      credentials:
        - auth-index: "abc123"
          name: "team-claude"
`)
	if err := ValidateV8Config(data); err != nil {
		t.Fatalf("ValidateV8Config() error = %v", err)
	}
	cfg, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.UsageAPI.Enabled {
		t.Fatal("usage-api.enabled = false, want true")
	}
	if len(cfg.UsageAPI.Providers) != 1 || cfg.UsageAPI.Providers[0] != "Claude" {
		t.Fatalf("usage-api.providers = %#v, want [Claude]", cfg.UsageAPI.Providers)
	}
	if len(cfg.UsageAPI.Credentials) != 1 {
		t.Fatalf("usage-api.credentials = %#v, want one entry", cfg.UsageAPI.Credentials)
	}
	if cfg.UsageAPI.Credentials[0].AuthIndex != "abc123" || cfg.UsageAPI.Credentials[0].Name != "team-claude" {
		t.Fatalf("usage-api credential = %#v", cfg.UsageAPI.Credentials[0])
	}
}

func TestUsageAPIConfigAcceptsLegacyLayout(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`port: 8317
usage-api:
  enabled: true
  providers:
    - codex
  credentials:
    - auth-index: "def456"
      name: "team-codex"
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.UsageAPI.Enabled {
		t.Fatal("usage-api.enabled = false, want true")
	}
	if len(cfg.UsageAPI.Providers) != 1 || cfg.UsageAPI.Providers[0] != "codex" {
		t.Fatalf("usage-api.providers = %#v, want [codex]", cfg.UsageAPI.Providers)
	}
	if len(cfg.UsageAPI.Credentials) != 1 {
		t.Fatalf("usage-api.credentials = %#v, want one entry", cfg.UsageAPI.Credentials)
	}
	if cfg.UsageAPI.Credentials[0].AuthIndex != "def456" || cfg.UsageAPI.Credentials[0].Name != "team-codex" {
		t.Fatalf("usage-api credential = %#v", cfg.UsageAPI.Credentials[0])
	}
}
