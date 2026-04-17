package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCloakConfigOAuthLeverDefaults verifies that OAuthSanitizeSystemPrompt,
// OAuthRemapToolNames, and OAuthInjectBillingHeader are nil (legacy default)
// when not set, and OAuthDisableHeader defaults to the empty string.
func TestCloakConfigOAuthLeverDefaults(t *testing.T) {
	var c CloakConfig
	if c.OAuthSanitizeSystemPrompt != nil {
		t.Errorf("OAuthSanitizeSystemPrompt: want nil, got %v", c.OAuthSanitizeSystemPrompt)
	}
	if c.OAuthRemapToolNames != nil {
		t.Errorf("OAuthRemapToolNames: want nil, got %v", c.OAuthRemapToolNames)
	}
	if c.OAuthInjectBillingHeader != nil {
		t.Errorf("OAuthInjectBillingHeader: want nil, got %v", c.OAuthInjectBillingHeader)
	}
	if c.OAuthDisableHeader != "" {
		t.Errorf("OAuthDisableHeader: want empty string, got %q", c.OAuthDisableHeader)
	}
}

// TestCloakConfigOAuthLeverYAMLRoundTrip verifies that the four new fields
// survive a YAML marshal/unmarshal round-trip with the correct tag names.
func TestCloakConfigOAuthLeverYAMLRoundTrip(t *testing.T) {
	falseVal := false
	trueVal := true
	orig := CloakConfig{
		Mode:                      "auto",
		OAuthSanitizeSystemPrompt: &trueVal,
		OAuthRemapToolNames:       &falseVal,
		OAuthInjectBillingHeader:  &trueVal,
		OAuthDisableHeader:        "s3cr3t",
	}

	out, err := yaml.Marshal(&orig)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}

	var got CloakConfig
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	if got.OAuthSanitizeSystemPrompt == nil || *got.OAuthSanitizeSystemPrompt != true {
		t.Errorf("OAuthSanitizeSystemPrompt: want true, got %v", got.OAuthSanitizeSystemPrompt)
	}
	if got.OAuthRemapToolNames == nil || *got.OAuthRemapToolNames != false {
		t.Errorf("OAuthRemapToolNames: want false, got %v", got.OAuthRemapToolNames)
	}
	if got.OAuthInjectBillingHeader == nil || *got.OAuthInjectBillingHeader != true {
		t.Errorf("OAuthInjectBillingHeader: want true, got %v", got.OAuthInjectBillingHeader)
	}
	if got.OAuthDisableHeader != "s3cr3t" {
		t.Errorf("OAuthDisableHeader: want s3cr3t, got %q", got.OAuthDisableHeader)
	}

	// Verify that a nil *bool field is correctly omitted from YAML (omitempty)
	emptyCfg := CloakConfig{}
	emptyOut, err := yaml.Marshal(&emptyCfg)
	if err != nil {
		t.Fatalf("yaml.Marshal empty: %v", err)
	}
	yamlStr := string(emptyOut)
	for _, key := range []string{"oauth-sanitize-system-prompt", "oauth-remap-tool-names", "oauth-inject-billing-header", "oauth-disable-header"} {
		if strings.Contains(yamlStr, key) {
			t.Errorf("expected key %q to be omitted from YAML when nil/empty, but found it in: %s", key, yamlStr)
		}
	}
}
