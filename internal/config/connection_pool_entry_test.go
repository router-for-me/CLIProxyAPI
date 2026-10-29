package config_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestPerEntryConnectionPoolAccepted pins the per-credential opt-in shape: the
// global pool stays off while a single api-keys entry turns pooling on for itself.
func TestPerEntryConnectionPoolAccepted(t *testing.T) {
	raw := []byte(`config-version: 8
api-keys:
  codex:
    - name: ollama
      base-url: "https://ollama.com/v1"
      proxy-url: "direct"
      connection-pool:
        enabled: true
        idle-conn-timeout: "300s"
        max-idle-conns-per-host: 2
      keys:
        - api-key: "ollama-key"
`)
	if err := config.ValidateV8Config(raw); err != nil {
		t.Fatalf("ValidateV8Config: %v", err)
	}
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if len(cfg.CodexKey) != 1 || cfg.CodexKey[0].ConnectionPool == nil {
		t.Fatalf("per-entry connection-pool not parsed: %+v", cfg.CodexKey)
	}
	pool := cfg.CodexKey[0].ConnectionPool
	if pool.Enabled == nil || !*pool.Enabled {
		t.Fatalf("enabled not parsed: %+v", pool)
	}
	if pool.IdleConnTimeout != "300s" {
		t.Fatalf("idle-conn-timeout = %q, want 300s", pool.IdleConnTimeout)
	}
	if pool.MaxIdleConnsPerHost == nil || *pool.MaxIdleConnsPerHost != 2 {
		t.Fatalf("max-idle-conns-per-host not parsed: %+v", pool)
	}
	if errLegacy := config.ValidateV8Config([]byte("config-version: 8\nrequests:\n  proxy-connection-pool:\n    enabled: true\n")); errLegacy == nil {
		t.Fatal("a global pooling switch must not exist")
	}
}

// TestPerEntryConnectionPoolRejectsUnknownKey keeps KnownFields strict inside the
// new nested block.
func TestPerEntryConnectionPoolRejectsUnknownKey(t *testing.T) {
	raw := []byte(`config-version: 8
api-keys:
  codex:
    - name: k
      keys:
        - api-key: "k"
      connection-pool:
        enabled: true
        idle-timeout: "30s"
`)
	if err := config.ValidateV8Config(raw); err == nil {
		t.Fatal("unknown nested key must be rejected")
	}
}

// TestPerKeyConnectionPoolOverridesGroup pins per-key control inside one provider
// group: keys may switch pooling on or off independently, and a partial override keeps
// inheriting the group values it does not mention.
func TestPerKeyConnectionPoolOverridesGroup(t *testing.T) {
	raw := []byte(`config-version: 8
api-keys:
  codex:
    - name: ollama
      base-url: "https://ollama.com/v1"
      connection-pool:
        enabled: true
        idle-conn-timeout: "300s"
        max-idle-conns-per-host: 2
      keys:
        - api-key: "inherits-group"
        - api-key: "opts-out"
          connection-pool:
            enabled: false
        - api-key: "partial"
          connection-pool:
            idle-conn-timeout: "60s"
        - api-key: "null-subkey"
          connection-pool:
            idle-conn-timeout: null
            max-idle-conns-per-host: 4
`)
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	byKey := map[string]*config.ProxyConnectionPoolConfig{}
	for i := range cfg.CodexKey {
		byKey[cfg.CodexKey[i].APIKey] = cfg.CodexKey[i].ConnectionPool
	}

	inherits := byKey["inherits-group"]
	if inherits == nil || inherits.Enabled == nil || !*inherits.Enabled || inherits.IdleConnTimeout != "300s" {
		t.Fatalf("key without an override must inherit the group block, got %+v", inherits)
	}

	optedOut := byKey["opts-out"]
	if optedOut == nil || optedOut.Enabled == nil || *optedOut.Enabled {
		t.Fatalf("per-key opt-out not honoured, got %+v", optedOut)
	}

	partial := byKey["partial"]
	if partial == nil || partial.Enabled == nil || !*partial.Enabled {
		t.Fatalf("partial override must keep the group's enabled value, got %+v", partial)
	}
	if partial.IdleConnTimeout != "60s" {
		t.Fatalf("partial override idle = %q, want 60s", partial.IdleConnTimeout)
	}
	if partial.MaxIdleConnsPerHost == nil || *partial.MaxIdleConnsPerHost != 2 {
		t.Fatalf("partial override must inherit max-idle-conns-per-host, got %+v", partial)
	}

	nullSubkey := byKey["null-subkey"]
	if nullSubkey == nil || nullSubkey.Enabled == nil || !*nullSubkey.Enabled {
		t.Fatalf("null-subkey override must inherit group enabled, got %+v", nullSubkey)
	}
	if nullSubkey.IdleConnTimeout != "300s" {
		t.Fatalf("null sub-key must inherit group idle-conn-timeout 300s, got %q", nullSubkey.IdleConnTimeout)
	}
	if nullSubkey.MaxIdleConnsPerHost == nil || *nullSubkey.MaxIdleConnsPerHost != 4 {
		t.Fatalf("null-subkey override max-idle-conns-per-host = %v, want 4", nullSubkey.MaxIdleConnsPerHost)
	}
}
