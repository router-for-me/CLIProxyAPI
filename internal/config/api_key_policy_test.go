package config

import (
	"strings"
	"testing"
	"time"

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

func TestMatchModelPattern(t *testing.T) {
	cases := []struct {
		pattern, model string
		want           bool
	}{
		{"gpt-5*", "gpt-5.1-codex", true},
		{"gpt-5*", "GPT-5", true},
		{"gpt-5*", "gpt-4o", false},
		{"claude-*-4*", "claude-sonnet-4-5", true},
		{"*mini", "gpt-5-mini", true},
		{"exact", "exact", true},
		{"exact", "exact-2", false},
		{"*", "anything", true},
	}
	for _, c := range cases {
		if got := MatchModelPattern(c.pattern, c.model); got != c.want {
			t.Errorf("MatchModelPattern(%q, %q) = %v, want %v", c.pattern, c.model, got, c.want)
		}
	}
}

func TestAPIKeyPolicyExpiry(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if (APIKeyPolicy{}).ExpiredAt(now) {
		t.Fatal("no expiry must not expire")
	}
	if !(APIKeyPolicy{ExpiresAt: "2026-11-15T11:59:59Z"}).ExpiredAt(now) {
		t.Fatal("past timestamp must expire")
	}
	if (APIKeyPolicy{ExpiresAt: "2027-01-01T00:00:00Z"}).ExpiredAt(now) {
		t.Fatal("future timestamp must not expire")
	}
	if !(APIKeyPolicy{ExpiresAt: "not-a-date"}).ExpiredAt(now) {
		t.Fatal("invalid expiry must fail closed")
	}
	day := APIKeyPolicy{ExpiresAt: "2026-11-15"}
	end, ok := day.ExpiresAtTime()
	if !ok || end.Day() != 16 || end.Hour() != 0 {
		t.Fatalf("date-only expiry = %v", end)
	}
}

func TestPhase2FieldsParse(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`config-version: 8
access:
    api-keys: [sk-aaa]
    api-key-policies:
        - key: sk-aaa
          allowed-models: ["gpt-5*", " claude-* "]
          expires-at: "2027-01-01T00:00:00Z"
          limits: {tokens-per-day: 2000000, cost-per-month: 50}
    model-prices:
        - {model: "gpt-5*", input: 1.25, output: 10}
        - {model: "", input: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cfg.FindAPIKeyPolicy("sk-aaa")
	if len(p.AllowedModels) != 2 || p.AllowedModels[1] != "claude-*" || p.Limits.TokensPerDay != 2000000 || p.Limits.CostPerMonth != 50 || p.ExpiresAt == "" {
		t.Fatalf("policy = %+v", p)
	}
	if !p.AllowsModel("gpt-5.1") || p.AllowsModel("gemini-3-pro") {
		t.Fatal("allowed-models matching wrong")
	}
	if len(cfg.ModelPrices) != 1 {
		t.Fatalf("model prices = %+v", cfg.ModelPrices)
	}
	if price, ok := cfg.FindModelPrice("gpt-5.1"); !ok || price.Output != 10 {
		t.Fatalf("price lookup = %+v %v", price, ok)
	}
	if err = ValidateV8Config([]byte("config-version: 8\naccess:\n    model-prices: [{model: x, input: 1}]\n")); err != nil {
		t.Fatalf("v8 rejected model-prices: %v", err)
	}
}
