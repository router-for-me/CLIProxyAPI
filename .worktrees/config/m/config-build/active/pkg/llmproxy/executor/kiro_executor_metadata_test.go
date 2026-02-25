package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestGetEffectiveProfileArnWithWarning_UsesCamelCaseIDCMetadata(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"authMethod":   "IDC",
			"clientId":     "cid",
			"clientSecret": "csecret",
		},
	}

	if got := getEffectiveProfileArnWithWarning(auth, "arn:aws:codewhisperer:::profile/default"); got != "" {
		t.Fatalf("expected empty profile ARN for IDC auth metadata, got %q", got)
	}
}

func TestGetMetadataString_PrefersFirstNonEmptyKey(t *testing.T) {
	metadata := map[string]any{
		"client_id": "",
		"clientId":  "cid-camel",
	}

	if got := getMetadataString(metadata, "client_id", "clientId"); got != "cid-camel" {
		t.Fatalf("getMetadataString() = %q, want %q", got, "cid-camel")
	}
}
