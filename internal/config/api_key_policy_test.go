package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const apiKeyPolicyV8YAML = `config-version: 8
access:
    api-keys:
        - "sk-aaa"
        - "sk-bbb"
    api-key-policies:
        - key: " sk-aaa "
          name: " Team A "
          allowed-auths: ["a1b2c3", "a1b2c3", " d4e5f6 ", ""]
        - key: "sk-bbb"
          disabled: true
        - key: ""
          name: "dropped"
`

func TestAPIKeyPoliciesParseAndNormalize(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(apiKeyPolicyV8YAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("api-keys = %v", cfg.APIKeys)
	}
	if len(cfg.APIKeyPolicies) != 2 {
		t.Fatalf("policies = %+v", cfg.APIKeyPolicies)
	}
	a, ok := cfg.FindAPIKeyPolicy("sk-aaa")
	if !ok || a.Name != "Team A" || strings.Join(a.AllowedAuths, ",") != "a1b2c3,d4e5f6" || a.Disabled {
		t.Fatalf("policy sk-aaa = %+v", a)
	}
	b, ok := cfg.FindAPIKeyPolicy("sk-bbb")
	if !ok || !b.Disabled || len(b.AllowedAuths) != 0 {
		t.Fatalf("policy sk-bbb = %+v", b)
	}
	if _, ok = cfg.FindAPIKeyPolicy("sk-unknown"); ok {
		t.Fatal("unknown key must have no policy")
	}
}

func TestAPIKeyPoliciesAcceptedByV8Validation(t *testing.T) {
	if err := ValidateV8Config([]byte(apiKeyPolicyV8YAML)); err != nil {
		t.Fatalf("v8 validation rejected api-key-policies: %v", err)
	}
	normalized, _, err := NormalizeConfigLayout([]byte(apiKeyPolicyV8YAML), true)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(normalized, &doc); err != nil {
		t.Fatal(err)
	}
	access, _ := doc["access"].(map[string]any)
	if access == nil || access["api-key-policies"] == nil {
		t.Fatalf("normalized layout lost access.api-key-policies:\n%s", normalized)
	}
}

func TestAPIKeyPoliciesLegacyRootMigratesToAccess(t *testing.T) {
	legacy := []byte("api-keys: [sk-aaa]\napi-key-policies:\n  - key: sk-aaa\n    allowed-auths: [abc]\n")
	cfg, err := ParseConfigBytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := cfg.FindAPIKeyPolicy("sk-aaa"); !ok || len(p.AllowedAuths) != 1 {
		t.Fatalf("legacy policy not parsed: %+v", cfg.APIKeyPolicies)
	}
	migrated, _, err := NormalizeConfigLayout(legacy, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migrated), "access:") || !strings.Contains(string(migrated), "api-key-policies:") {
		t.Fatalf("legacy policy not migrated under access:\n%s", migrated)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated config invalid: %v", err)
	}
}

func TestAPIKeyPoliciesAbsentKeepsLegacyBehavior(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("config-version: 8\naccess:\n    api-keys: [sk-aaa]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeyPolicies) != 0 {
		t.Fatalf("unexpected policies: %+v", cfg.APIKeyPolicies)
	}
	if _, ok := cfg.FindAPIKeyPolicy("sk-aaa"); ok {
		t.Fatal("key without policy must be unrestricted")
	}
}
