package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func modelIDs(models []map[string]any) map[string]int {
	out := make(map[string]int, len(models))
	for _, model := range models {
		if id, ok := model["id"].(string); ok {
			out[id]++
		}
	}
	return out
}

func TestAppendCodexThinkingModels(t *testing.T) {
	baseModels := []map[string]any{
		{"id": "gpt-5.5", "object": "model", "created": int64(1), "owned_by": "openai"},
		{"id": "claude-sonnet-4-6", "object": "model", "owned_by": "anthropic"},
	}
	codexModels := []*registry.ModelInfo{{
		ID:       "gpt-5.5",
		Object:   "model",
		Created:  1,
		OwnedBy:  "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
	}}

	result := appendCodexThinkingModels(baseModels, codexModels, nil)
	ids := modelIDs(result)

	for _, id := range []string{"gpt-5.5", "gpt-5.5(low)", "gpt-5.5(medium)", "gpt-5.5(high)", "gpt-5.5(xhigh)"} {
		if ids[id] != 1 {
			t.Fatalf("expected %s exactly once, got ids=%v", id, ids)
		}
	}
	if ids["claude-sonnet-4-6(low)"] != 0 {
		t.Fatalf("non-Codex model received thinking suffix: ids=%v", ids)
	}
}

func TestAppendCodexThinkingModelsSkipsUnsupportedAndDuplicateAliases(t *testing.T) {
	baseModels := []map[string]any{
		{"id": "gpt-5.5", "object": "model"},
		{"id": "gpt-5.5(high)", "object": "model"},
		{"id": "gpt-5.5-no-thinking", "object": "model"},
		{"id": "gpt-5.5-suffixed(high)", "object": "model"},
	}
	codexModels := []*registry.ModelInfo{
		{
			ID:       "gpt-5.5",
			OwnedBy:  "openai",
			Thinking: &registry.ThinkingSupport{Levels: []string{"high", "xhigh"}},
		},
		{
			ID:      "gpt-5.5-no-thinking",
			OwnedBy: "openai",
		},
		{
			ID:       "gpt-5.5-suffixed(high)",
			OwnedBy:  "openai",
			Thinking: &registry.ThinkingSupport{Levels: []string{"low"}},
		},
		{
			ID:       "codex-auto-review",
			OwnedBy:  "openai",
			Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
		},
	}

	result := appendCodexThinkingModels(baseModels, codexModels, nil)
	ids := modelIDs(result)

	if ids["gpt-5.5(high)"] != 1 {
		t.Fatalf("expected existing gpt-5.5(high) to remain unduplicated, got ids=%v", ids)
	}
	if ids["gpt-5.5(xhigh)"] != 1 {
		t.Fatalf("expected gpt-5.5(xhigh) alias, got ids=%v", ids)
	}
	if ids["gpt-5.5-no-thinking(low)"] != 0 {
		t.Fatalf("model without levels should not receive suffix alias, got ids=%v", ids)
	}
	if ids["gpt-5.5-suffixed(high)(low)"] != 0 {
		t.Fatalf("suffixed model should not receive derived suffix alias, got ids=%v", ids)
	}
	if ids["codex-auto-review(high)"] != 0 {
		t.Fatalf("internal Codex model should not receive suffix alias, got ids=%v", ids)
	}
}

func TestAppendCodexThinkingModelsSkipsNoneAndAutoAliases(t *testing.T) {
	const modelID = "gpt-5.2"
	baseModels := []map[string]any{
		{"id": modelID, "object": "model"},
	}
	codexModels := []*registry.ModelInfo{{
		ID:       modelID,
		Object:   "model",
		OwnedBy:  "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"none", "auto", "low", "medium", "high", "xhigh"}},
	}}

	result := appendCodexThinkingModels(baseModels, codexModels, nil)
	ids := modelIDs(result)

	if ids[modelID] != 1 {
		t.Fatalf("expected base model to remain listed once, got ids=%v", ids)
	}
	for _, id := range []string{modelID + "(low)", modelID + "(medium)", modelID + "(high)", modelID + "(xhigh)"} {
		if ids[id] != 1 {
			t.Fatalf("expected %s exactly once, got ids=%v", id, ids)
		}
	}
	for _, id := range []string{modelID + "(none)", modelID + "(auto)"} {
		if ids[id] != 0 {
			t.Fatalf("expected %s to be hidden from model listing, got ids=%v", id, ids)
		}
	}
}

func TestOpenAIModelsShowCodexThinkingModelsSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const clientID = "test-openai-models-codex-thinking-switch"
	const modelID = "gpt-test-codex-thinking-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:       modelID,
		Object:   "model",
		OwnedBy:  "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}},
	}})
	defer reg.UnregisterClient(clientID)

	callModels := func(show bool) map[string]int {
		handler := NewOpenAIAPIHandler(&handlers.BaseAPIHandler{
			Cfg: &sdkconfig.SDKConfig{ShowCodexThinkingModels: show},
		})
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

		handler.OpenAIModels(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("OpenAIModels status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var payload struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to decode models response: %v", err)
		}
		return modelIDs(payload.Data)
	}

	ids := callModels(false)
	if ids[modelID+"(high)"] != 0 {
		t.Fatalf("expected switch off to omit thinking suffix aliases, got ids=%v", ids)
	}

	ids = callModels(true)
	if ids[modelID] != 1 {
		t.Fatalf("expected base model to remain listed once, got ids=%v", ids)
	}
	for _, id := range []string{modelID + "(low)", modelID + "(high)"} {
		if ids[id] != 1 {
			t.Fatalf("expected %s exactly once with switch on, got ids=%v", id, ids)
		}
	}
}

func TestAppendCodexThinkingModelsWithLevelFiltering(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		CodexThinkingDisplay: sdkconfig.CodexThinkingDisplayConfig{
			Levels: []string{"high", "xhigh"},
		},
	}
	baseModels := []map[string]any{
		{"id": "gpt-5.5", "object": "model", "created": int64(1), "owned_by": "openai"},
	}
	codexModels := []*registry.ModelInfo{{
		ID:       "gpt-5.5",
		Object:   "model",
		Created:  1,
		OwnedBy:  "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
	}}

	result := appendCodexThinkingModels(baseModels, codexModels, cfg)
	ids := modelIDs(result)

	// Should have base model + only high and xhigh aliases
	if ids["gpt-5.5"] != 1 {
		t.Fatalf("expected base model once, got ids=%v", ids)
	}
	for _, id := range []string{"gpt-5.5(high)", "gpt-5.5(xhigh)"} {
		if ids[id] != 1 {
			t.Fatalf("expected %s exactly once, got ids=%v", id, ids)
		}
	}
	for _, id := range []string{"gpt-5.5(low)", "gpt-5.5(medium)"} {
		if ids[id] != 0 {
			t.Fatalf("expected %s to be hidden, got ids=%v", id, ids)
		}
	}
}

func TestAppendCodexThinkingModelsWithModelOverrides(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		CodexThinkingDisplay: sdkconfig.CodexThinkingDisplayConfig{
			Levels: []string{"high", "xhigh"},
			ModelOverrides: map[string][]string{
				"gpt-5.2-codex": {"low"},
			},
		},
	}
	baseModels := []map[string]any{
		{"id": "gpt-5.2-codex", "object": "model", "created": int64(1), "owned_by": "openai"},
		{"id": "gpt-5.3-codex", "object": "model", "created": int64(2), "owned_by": "openai"},
	}
	codexModels := []*registry.ModelInfo{
		{
			ID:       "gpt-5.2-codex",
			Object:   "model",
			Created:  1,
			OwnedBy:  "openai",
			Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
		},
		{
			ID:       "gpt-5.3-codex",
			Object:   "model",
			Created:  2,
			OwnedBy:  "openai",
			Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
		},
	}

	result := appendCodexThinkingModels(baseModels, codexModels, cfg)
	ids := modelIDs(result)

	// gpt-5.2-codex: global (high, xhigh) + override (low) = low, high, xhigh
	if ids["gpt-5.2-codex"] != 1 {
		t.Fatalf("expected base model once, got ids=%v", ids)
	}
	for _, id := range []string{"gpt-5.2-codex(low)", "gpt-5.2-codex(high)", "gpt-5.2-codex(xhigh)"} {
		if ids[id] != 1 {
			t.Fatalf("expected %s exactly once (override additive), got ids=%v", id, ids)
		}
	}
	if ids["gpt-5.2-codex(medium)"] != 0 {
		t.Fatalf("expected gpt-5.2-codex(medium) to be hidden, got ids=%v", ids)
	}

	// gpt-5.3-codex: only global (high, xhigh)
	if ids["gpt-5.3-codex"] != 1 {
		t.Fatalf("expected base model once, got ids=%v", ids)
	}
	for _, id := range []string{"gpt-5.3-codex(high)", "gpt-5.3-codex(xhigh)"} {
		if ids[id] != 1 {
			t.Fatalf("expected %s exactly once (global only), got ids=%v", id, ids)
		}
	}
	for _, id := range []string{"gpt-5.3-codex(low)", "gpt-5.3-codex(medium)"} {
		if ids[id] != 0 {
			t.Fatalf("expected %s to be hidden, got ids=%v", id, ids)
		}
	}
}

func TestAppendCodexThinkingModelsDefaultAllLevels(t *testing.T) {
	baseModels := []map[string]any{
		{"id": "gpt-5.5", "object": "model", "created": int64(1), "owned_by": "openai"},
	}
	codexModels := []*registry.ModelInfo{{
		ID:       "gpt-5.5",
		Object:   "model",
		Created:  1,
		OwnedBy:  "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
	}}

	// Test with empty SDKConfig (Levels empty -> default all levels)
	result := appendCodexThinkingModels(baseModels, codexModels, &sdkconfig.SDKConfig{})
	ids := modelIDs(result)

	for _, id := range []string{"gpt-5.5(low)", "gpt-5.5(medium)", "gpt-5.5(high)", "gpt-5.5(xhigh)"} {
		if ids[id] != 1 {
			t.Fatalf("expected %s exactly once with empty config, got ids=%v", id, ids)
		}
	}

	// Test with nil cfg
	baseModels2 := []map[string]any{
		{"id": "gpt-5.5", "object": "model", "created": int64(1), "owned_by": "openai"},
	}
	result2 := appendCodexThinkingModels(baseModels2, codexModels, nil)
	ids2 := modelIDs(result2)

	for _, id := range []string{"gpt-5.5(low)", "gpt-5.5(medium)", "gpt-5.5(high)", "gpt-5.5(xhigh)"} {
		if ids2[id] != 1 {
			t.Fatalf("expected %s exactly once with nil config, got ids=%v", id, ids2)
		}
	}
}
