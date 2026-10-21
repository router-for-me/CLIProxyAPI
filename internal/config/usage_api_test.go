package config

import "testing"

func TestParseConfigUsageAPILegacyLayout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
usage-api:
  enabled: true
  providers: [" Claude ", "codex", "claude"]
  credentials: [" auth-1 ", "auth-1", ""]
  cache-seconds: 0
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.UsageAPI.Enabled {
		t.Fatal("UsageAPI.Enabled = false, want true")
	}
	wantProviders := []string{"claude", "codex"}
	if len(cfg.UsageAPI.Providers) != len(wantProviders) {
		t.Fatalf("UsageAPI.Providers = %#v, want %#v", cfg.UsageAPI.Providers, wantProviders)
	}
	for i, want := range wantProviders {
		if cfg.UsageAPI.Providers[i] != want {
			t.Fatalf("UsageAPI.Providers[%d] = %q, want %q", i, cfg.UsageAPI.Providers[i], want)
		}
	}
	if len(cfg.UsageAPI.Credentials) != 1 || cfg.UsageAPI.Credentials[0] != "auth-1" {
		t.Fatalf("UsageAPI.Credentials = %#v, want [auth-1]", cfg.UsageAPI.Credentials)
	}
	if cfg.UsageAPI.CacheSeconds != DefaultUsageAPICacheSeconds {
		t.Fatalf("UsageAPI.CacheSeconds = %d, want %d", cfg.UsageAPI.CacheSeconds, DefaultUsageAPICacheSeconds)
	}
}

func TestParseConfigUsageAPIV8Layout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
observability:
  usage:
    api:
      enabled: true
      providers: [codex]
      cache-seconds: 9999
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.UsageAPI.Enabled {
		t.Fatal("UsageAPI.Enabled = false, want true")
	}
	if len(cfg.UsageAPI.Providers) != 1 || cfg.UsageAPI.Providers[0] != "codex" {
		t.Fatalf("UsageAPI.Providers = %#v, want [codex]", cfg.UsageAPI.Providers)
	}
	if cfg.UsageAPI.CacheSeconds != MaxUsageAPICacheSeconds {
		t.Fatalf("UsageAPI.CacheSeconds = %d, want %d", cfg.UsageAPI.CacheSeconds, MaxUsageAPICacheSeconds)
	}
}

func TestUsageAPIConfigDefaultsToDisabled(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.UsageAPI.Enabled {
		t.Fatal("UsageAPI.Enabled = true, want false")
	}
	if cfg.UsageAPI.CacheSeconds != DefaultUsageAPICacheSeconds {
		t.Fatalf("UsageAPI.CacheSeconds = %d, want %d", cfg.UsageAPI.CacheSeconds, DefaultUsageAPICacheSeconds)
	}
}
