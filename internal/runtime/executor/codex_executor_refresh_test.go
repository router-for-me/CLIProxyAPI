package executor

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSyncCodexPlanTypeUpdatesRuntimeAndPersistedMetadata(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
		Metadata:   map[string]any{"plan_type": "free"},
	}

	syncCodexPlanType(auth, codexPlanJWT(t, "plus"))

	if got := auth.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("Attributes plan_type = %q, want plus", got)
	}
	if got, _ := auth.Metadata["plan_type"].(string); got != "plus" {
		t.Fatalf("Metadata plan_type = %q, want plus", got)
	}
}

func TestSyncCodexPlanTypeKeepsExistingPlanForInvalidToken(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
		Metadata:   map[string]any{"plan_type": "free"},
	}

	syncCodexPlanType(auth, "invalid-token")

	if got := auth.Attributes["plan_type"]; got != "free" {
		t.Fatalf("Attributes plan_type = %q, want free", got)
	}
	if got, _ := auth.Metadata["plan_type"].(string); got != "free" {
		t.Fatalf("Metadata plan_type = %q, want free", got)
	}
}

func TestSyncCodexPlanTypeFromMetadataUpdatesHomeRefreshResult(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
		Metadata: map[string]any{
			"id_token": codexPlanJWT(t, "plus"),
		},
	}

	syncCodexPlanTypeFromMetadata(auth)

	if got := auth.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("Attributes plan_type = %q, want plus", got)
	}
	if got, _ := auth.Metadata["plan_type"].(string); got != "plus" {
		t.Fatalf("Metadata plan_type = %q, want plus", got)
	}
}

func codexPlanJWT(t *testing.T, planType string) string {
	t.Helper()
	payload, errMarshal := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": planType,
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal JWT payload: %v", errMarshal)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
