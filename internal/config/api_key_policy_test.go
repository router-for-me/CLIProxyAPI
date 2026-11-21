package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAPIKeyPolicyValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		policies string
	}{
		{"blank key", "[{api-key: '  ', name: client}]"},
		{"duplicate key", "[{api-key: secret-client, name: first}, {api-key: ' secret-client ', name: second}]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var cfg Config
			err := yaml.Unmarshal([]byte("access:\n  api-keys: [secret-client]\n  api-key-policies: "+test.policies+"\n"), &cfg)
			if err == nil {
				t.Fatal("expected invalid policy configuration to be rejected")
			}
			if strings.Contains(err.Error(), "secret-client") {
				t.Fatal("validation error exposed the client key")
			}
		})
	}
}

func TestAPIKeyPoliciesRoundTrip(t *testing.T) {
	const source = `access:
  api-keys: [existing-client]
  api-key-policies:
    - api-key: existing-client
      name: client
      allowed-models: ["claude/*", "gpt-*"]
      blocked-models: ["gpt-image-*"]
    - api-key: inactive-client
      name: retired
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(source), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "existing-client" {
		t.Fatal("policies granted an API key")
	}
	if len(cfg.APIKeyPolicies) != 2 || cfg.APIKeyPolicies[0].Name != "client" || !cfg.APIKeyPolicies[0].Allowed("claude/sonnet") || cfg.APIKeyPolicies[0].Allowed("gpt-image-2") {
		t.Fatal("model policies were not loaded")
	}
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var flat Config
	if err := yaml.Unmarshal(encoded, &flat); err != nil || len(flat.APIKeyPolicies) != 2 {
		t.Fatalf("flat YAML round trip failed: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfigPreserveComments(path, &cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var layout yaml.Node
	if err := yaml.Unmarshal(saved, &layout); err != nil {
		t.Fatal(err)
	}
	if yamlPath(layout.Content[0], "access.api-key-policies") == nil {
		t.Fatal("policies were not saved under access")
	}
	var restored Config
	if err := yaml.Unmarshal(saved, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.APIKeyPolicies) != 2 || restored.APIKeyPolicies[0].Name != "client" || restored.APIKeyPolicies[0].Allowed("gpt-image-2") {
		t.Fatal("model policies did not survive YAML round trip")
	}
}

func TestAPIKeyPolicyAllowed(t *testing.T) {
	for _, test := range []struct {
		name    string
		policy  APIKeyPolicy
		model   string
		allowed bool
	}{
		{"unrestricted", APIKeyPolicy{}, "any/model", true},
		{"exact allow", APIKeyPolicy{AllowedModels: []string{"gpt-model"}}, "gpt-model", true},
		{"unknown denied", APIKeyPolicy{AllowedModels: []string{"gpt-model"}}, "other", false},
		{"slash wildcard", APIKeyPolicy{AllowedModels: []string{"claude/*"}}, "claude/sonnet", true},
		{"multiple wildcards", APIKeyPolicy{AllowedModels: []string{"a*b*c"}}, "a/bee/c", true},
		{"ordered wildcards", APIKeyPolicy{AllowedModels: []string{"a*b*c"}}, "a/c/b", false},
		{"nonoverlapping wildcard", APIKeyPolicy{AllowedModels: []string{"a*a"}}, "a", false},
		{"literal question mark", APIKeyPolicy{AllowedModels: []string{"model?"}}, "model1", false},
		{"literal brackets", APIKeyPolicy{AllowedModels: []string{"model[ab]"}}, "modela", false},
		{"case insensitive allow", APIKeyPolicy{AllowedModels: []string{"Model"}}, "model", true},
		{"blocked overrides allowed", APIKeyPolicy{AllowedModels: []string{"*"}, BlockedModels: []string{"paid-*"}}, "paid-sonnet", false},
		{"blocked only", APIKeyPolicy{BlockedModels: []string{"paid-*"}}, "free-model", true},
		{"case insensitive block", APIKeyPolicy{BlockedModels: []string{"paid-*"}}, "PAID-SONNET", false},
		{"case insensitive block pattern", APIKeyPolicy{BlockedModels: []string{"PAID-*"}}, "paid-sonnet", false},
		{"whitespace before suffix", APIKeyPolicy{BlockedModels: []string{"paid-sonnet"}}, "paid-sonnet (high)", false},
		{"exact block suffix", APIKeyPolicy{BlockedModels: []string{"paid-sonnet"}}, "paid-sonnet(high)", false},
		{"exact allow suffix", APIKeyPolicy{AllowedModels: []string{"claude/sonnet"}}, "claude/sonnet(16384)", true},
		{"empty pattern", APIKeyPolicy{AllowedModels: []string{""}}, "any-model", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.Allowed(test.model); got != test.allowed {
				t.Fatalf("Allowed(%q) = %v, want %v", test.model, got, test.allowed)
			}
		})
	}
}
