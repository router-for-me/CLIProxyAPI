package executor

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestApplyCodexCapacityClaimsUpdatesPlanType(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"plan_type": "free",
		},
		Metadata: map[string]any{
			"chatgpt_plan_type": "free",
		},
	}

	applyCodexCapacityClaims(auth, codexCapacityTestJWT(t, map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":                "acct-123",
			"chatgpt_plan_type":                 "plus",
			"chatgpt_subscription_active_start": "2026-06-12T00:00:00Z",
			"chatgpt_subscription_active_until": "2026-07-12T00:00:00Z",
			"chatgpt_subscription_last_checked": "2026-06-12T00:01:00Z",
		},
	}))

	if got := auth.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("plan_type attribute = %q, want plus", got)
	}
	if got := auth.Metadata["chatgpt_plan_type"]; got != "plus" {
		t.Fatalf("chatgpt_plan_type metadata = %q, want plus", got)
	}
	if got := auth.Metadata["chatgpt_account_id"]; got != "acct-123" {
		t.Fatalf("chatgpt_account_id metadata = %q, want acct-123", got)
	}
	if got := auth.Metadata["chatgpt_subscription_active_until"]; got != "2026-07-12T00:00:00Z" {
		t.Fatalf("chatgpt_subscription_active_until metadata = %q, want 2026-07-12T00:00:00Z", got)
	}
}

func TestApplyCodexCapacityClaimsPreservesMissingPlanAndClearsMissingValues(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"plan_type": "plus",
		},
		Metadata: map[string]any{
			"chatgpt_account_id":                "acct-123",
			"chatgpt_plan_type":                 "plus",
			"chatgpt_subscription_active_start": "2026-06-12T00:00:00Z",
			"chatgpt_subscription_active_until": "2026-07-12T00:00:00Z",
		},
	}

	applyCodexCapacityClaims(auth, codexCapacityTestJWT(t, map[string]any{
		"email":                       "user@example.com",
		"https://api.openai.com/auth": map[string]any{},
	}))

	if got := auth.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("expected missing plan claim to preserve plan_type attribute, got %q", got)
	}
	if got := auth.Metadata["chatgpt_plan_type"]; got != "plus" {
		t.Fatalf("expected missing plan claim to preserve chatgpt_plan_type metadata, got %q", got)
	}
	for _, key := range []string{
		"chatgpt_account_id",
		"chatgpt_subscription_active_start",
		"chatgpt_subscription_active_until",
	} {
		if _, ok := auth.Metadata[key]; ok {
			t.Fatalf("expected missing claim to clear metadata %q", key)
		}
	}
}

func codexCapacityTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, errMarshalHeader := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if errMarshalHeader != nil {
		t.Fatalf("marshal jwt header: %v", errMarshalHeader)
	}
	payload, errMarshalPayload := json.Marshal(claims)
	if errMarshalPayload != nil {
		t.Fatalf("marshal jwt claims: %v", errMarshalPayload)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "."
}
