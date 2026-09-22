package management

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func mustAccountDoc(t *testing.T, body string) *yaml.Node {
	t.Helper()
	doc, err := decodeAccountConfigYAML([]byte(body))
	if err != nil {
		t.Fatalf("decodeAccountConfigYAML() error = %v", err)
	}
	return doc
}

func mustEncodeAccountDoc(t *testing.T, doc *yaml.Node) string {
	t.Helper()
	body, err := encodeAccountConfigYAML(doc)
	if err != nil {
		t.Fatalf("encodeAccountConfigYAML() error = %v", err)
	}
	return string(body)
}

func TestAccountConfigPolicyRejectsProtectedBillingAndQuotaChanges(t *testing.T) {
	original := mustAccountDoc(t, `debug: false
billing:
  api-key: original-secret
routing:
  strategy: quota-aware
  quota-aware:
    weekly-remaining-min-percent: 25
`)
	requested := mustAccountDoc(t, `debug: true
billing:
  api-key: attacker-secret
routing:
  strategy: quota-aware
`)

	_, err := mergeAccountConfigYAMLWithProtectedOriginal(original, requested)
	if err == nil {
		t.Fatal("mergeAccountConfigYAMLWithProtectedOriginal() error = nil, want protected rejection")
	}
	if strings.Contains(err.Error(), "original-secret") || strings.Contains(err.Error(), "attacker-secret") {
		t.Fatalf("protected rejection leaked secret in error: %v", err)
	}
}

func TestAccountConfigPolicyOmissionRestoresProtectedOriginalNodes(t *testing.T) {
	original := mustAccountDoc(t, `debug: false
billing:
  api-key: original-secret
remote-management:
  enabled: true
routing:
  strategy: quota-aware
  quota-aware:
    weekly-remaining-min-percent: 25
nested:
  custom-quota-token: keep-me
`)
	requested := mustAccountDoc(t, `debug: true
plugins:
  enabled: true
`)

	merged, err := mergeAccountConfigYAMLWithProtectedOriginal(original, requested)
	if err != nil {
		t.Fatalf("mergeAccountConfigYAMLWithProtectedOriginal() error = %v", err)
	}
	body := mustEncodeAccountDoc(t, merged)
	for _, want := range []string{
		"debug: true",
		"billing:",
		"api-key: original-secret",
		"remote-management:",
		"strategy: quota-aware",
		"quota-aware:",
		"custom-quota-token: keep-me",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("merged YAML missing %q:\n%s", want, body)
		}
	}
}

func TestAccountConfigPolicyAllowedEditsPersistInMergedBody(t *testing.T) {
	original := mustAccountDoc(t, `debug: false
billing:
  api-key: original-secret
routing:
  strategy: round-robin
`)
	requested := mustAccountDoc(t, `debug: true
plugins:
  enabled: true
routing:
  strategy: fill-first
`)

	merged, err := mergeAccountConfigYAMLWithProtectedOriginal(original, requested)
	if err != nil {
		t.Fatalf("mergeAccountConfigYAMLWithProtectedOriginal() error = %v", err)
	}
	body := mustEncodeAccountDoc(t, merged)
	for _, want := range []string{"debug: true", "enabled: true", "strategy: fill-first", "api-key: original-secret"} {
		if !strings.Contains(body, want) {
			t.Fatalf("merged YAML missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "strategy: round-robin") {
		t.Fatalf("unprotected routing strategy was unexpectedly restored:\n%s", body)
	}
}

func TestAccountConfigPolicySanitizeYAMLRedactsWithoutMutatingOriginal(t *testing.T) {
	original := mustAccountDoc(t, `debug: true
billing:
  api-key: original-secret
remote-management:
  password: admin-secret
routing:
  strategy: quota-aware
  quota-aware:
    weekly-remaining-min-percent: 25
plugins:
  configs:
    safe:
      enabled: true
nested:
  quota-secret: hidden
  safe: visible
`)
	before := mustEncodeAccountDoc(t, original)
	sanitized := sanitizeAccountConfigYAMLDocument(original)
	after := mustEncodeAccountDoc(t, original)
	if before != after {
		t.Fatalf("sanitizeAccountConfigYAMLDocument mutated original\nbefore:\n%s\nafter:\n%s", before, after)
	}
	body := mustEncodeAccountDoc(t, sanitized)
	for _, forbidden := range []string{"billing:", "original-secret", "remote-management", "admin-secret", "quota-aware", "quota-secret", "hidden", "strategy: quota-aware"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("sanitized YAML contains protected %q:\n%s", forbidden, body)
		}
	}
	for _, want := range []string{"debug: true", "plugins:", "safe: visible"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sanitized YAML missing %q:\n%s", want, body)
		}
	}
}

func TestAccountConfigPolicySanitizeJSONRedactsRecursively(t *testing.T) {
	input := map[string]any{
		"debug":   true,
		"billing": map[string]any{"api-key": "secret"},
		"routing": map[string]any{
			"strategy":    "quota-aware",
			"quota-aware": map[string]any{"threshold": 10},
		},
		"nested": map[string]any{
			"safe":          "visible",
			"billing-token": "hidden",
		},
	}
	out, ok := accountSanitizeJSONConfig(input).(map[string]any)
	if !ok {
		t.Fatalf("accountSanitizeJSONConfig() type = %T", out)
	}
	if _, exists := out["billing"]; exists {
		t.Fatalf("sanitized JSON retained billing: %#v", out)
	}
	routing, _ := out["routing"].(map[string]any)
	if _, exists := routing["strategy"]; exists {
		t.Fatalf("sanitized JSON retained quota-aware strategy: %#v", routing)
	}
	if _, exists := routing["quota-aware"]; exists {
		t.Fatalf("sanitized JSON retained quota-aware subtree: %#v", routing)
	}
	nested, _ := out["nested"].(map[string]any)
	if nested["safe"] != "visible" {
		t.Fatalf("sanitized JSON dropped safe nested value: %#v", nested)
	}
	if _, exists := nested["billing-token"]; exists {
		t.Fatalf("sanitized JSON retained nested billing-token: %#v", nested)
	}
	if _, exists := input["billing"]; !exists {
		t.Fatalf("accountSanitizeJSONConfig mutated input: %#v", input)
	}
}

func TestAccountConfigPolicyRejectsDuplicateKeysAndAliases(t *testing.T) {
	if _, err := decodeAccountConfigYAML([]byte("debug: true\ndebug: false\n")); err == nil {
		t.Fatal("decodeAccountConfigYAML() duplicate key error = nil")
	}
	if _, err := decodeAccountConfigYAML([]byte("secret: &secret hidden\ncopy: *secret\n")); err == nil {
		t.Fatal("decodeAccountConfigYAML() alias error = nil")
	}
}
