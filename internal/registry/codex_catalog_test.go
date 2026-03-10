package registry

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnsureCodexPlanTypeMetadataExtractsFromJWT(t *testing.T) {
	metadata := map[string]any{
		"type":     "codex",
		"id_token": testCodexJWT(t, "team"),
	}
	plan, changed := EnsureCodexPlanTypeMetadata(metadata)
	if !changed {
		t.Fatal("expected metadata to be updated from id_token")
	}
	if plan != "team" {
		t.Fatalf("expected team plan, got %q", plan)
	}
	if got, _ := metadata["plan_type"].(string); got != "team" {
		t.Fatalf("expected metadata plan_type team, got %#v", metadata["plan_type"])
	}
}

func TestGetCodexModelsForPlanUsesSafeFallback(t *testing.T) {
	models := GetCodexModelsForPlan("unknown")
	if len(models) == 0 {
		t.Fatal("expected codex models for unknown plan fallback")
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == "gpt-5.4" || model.ID == "gpt-5.3-codex-spark" {
			t.Fatalf("expected unknown plan to avoid higher-tier model %q", model.ID)
		}
	}
}

func TestGetStaticModelDefinitionsByChannelCodexReturnsTierUnion(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("codex")
	if len(models) == 0 {
		t.Fatal("expected codex static model definitions")
	}
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if _, ok := seen[model.ID]; ok {
			t.Fatalf("duplicate codex model %q in union", model.ID)
		}
		seen[model.ID] = struct{}{}
	}
	for _, required := range []string{"gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.4"} {
		if _, ok := seen[required]; !ok {
			t.Fatalf("expected codex union to include %q", required)
		}
	}
}

func TestLookupStaticModelInfoFindsCodexTierModel(t *testing.T) {
	model := LookupStaticModelInfo("gpt-5.3-codex-spark")
	if model == nil {
		t.Fatal("expected spark model lookup to succeed")
	}
	if !strings.EqualFold(model.DisplayName, "GPT 5.3 Codex Spark") {
		t.Fatalf("unexpected display name: %+v", model)
	}
}

func testCodexJWT(t *testing.T, planType string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadRaw, err := json.Marshal(map[string]any{
		"email": "tester@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_123",
			"chatgpt_plan_type":  planType,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadRaw)
	return header + "." + payload + ".signature"
}
