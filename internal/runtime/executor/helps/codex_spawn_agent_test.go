package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

func TestIsCodexMultiAgentClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		userAgent string
		want      bool
	}{
		{
			name:      "Codex Desktop",
			userAgent: "Codex Desktop/0.146.0-alpha.3 (Mac OS 26.5.2; arm64) unknown (Codex Desktop; 26.721.30844)",
			want:      true,
		},
		{
			name:      "codex tui",
			userAgent: "codex-tui/0.145.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.145.0)",
			want:      true,
		},
		{
			name:      "other client",
			userAgent: "curl/8.7.1",
			want:      false,
		},
		{
			name:      "embedded token",
			userAgent: "proxy Codex Desktop/0.146.0",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isCodexMultiAgentClient(tt.userAgent); got != tt.want {
				t.Fatalf("isCodexMultiAgentClient(%q) = %v, want %v", tt.userAgent, got, tt.want)
			}
		})
	}
}

func TestCodexSpawnAgentModelsFromSourcesIncludesModelMetadata(t *testing.T) {
	t.Parallel()

	catalog := []byte(`{"models":[
		{"slug":"model-template","display_name":"Template","description":"Template model.","default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"}],"service_tiers":[{"id":"priority"}],"priority":1},
		{"slug":"gpt-5.5","display_name":"Default","description":"Default model.","default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}],"service_tiers":[{"id":"priority"}],"priority":2}
	]}`)
	available := []map[string]any{
		{"id": "custom-model", "display_name": "Custom", "description": "Registry description."},
		{"id": "model-template"},
		{"id": "custom-model", "description": "duplicate"},
	}
	lookup := func(modelID string) *registry.ModelInfo {
		if modelID != "custom-model" {
			return nil
		}
		return &registry.ModelInfo{
			Description: "Dynamic model.",
			Thinking: &registry.ThinkingSupport{
				Levels: []string{"none", "low", "medium", "high"},
			},
		}
	}

	models := codexSpawnAgentModelsFromSources(available, catalog, lookup)
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}
	if got := models[0]; got.id != "model-template" || got.description != "Template model." || got.defaultReasoningEffort != "low" {
		t.Fatalf("template model = %+v", got)
	}
	if got := strings.Join(models[0].serviceTiers, ","); got != "priority" {
		t.Fatalf("template service tiers = %q, want priority", got)
	}
	custom := models[1]
	if custom.id != "custom-model" || custom.description != "Dynamic model." {
		t.Fatalf("custom model = %+v", custom)
	}
	if got := strings.Join(custom.reasoningEfforts, ","); got != "none,low,medium,high" {
		t.Fatalf("custom reasoning efforts = %q", got)
	}
	if custom.defaultReasoningEffort != "medium" {
		t.Fatalf("custom default reasoning effort = %q, want medium", custom.defaultReasoningEffort)
	}
	if len(custom.serviceTiers) != 0 {
		t.Fatalf("custom service tiers = %v, want none", custom.serviceTiers)
	}
}

func TestDecodeCodexHomeAvailableModels(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"codex":[{"id":"model-b","display_name":"Model B"},{"id":"model-a"}],
		"other":[{"name":"models/model-c","displayName":"Model C"},{"id":"model-a","display_name":"duplicate"}]
	}`)
	models := decodeCodexHomeAvailableModels(raw)
	if len(models) != 3 {
		t.Fatalf("model count = %d, want 3", len(models))
	}
	if got := mapString(models[0], "id"); got != "model-a" {
		t.Fatalf("first model ID = %q, want model-a", got)
	}
	if got := mapString(models[1], "description"); got != "Model B" {
		t.Fatalf("model-b description = %q, want Model B", got)
	}
	if got := mapString(models[2], "id"); got != "model-c" {
		t.Fatalf("last model ID = %q, want model-c", got)
	}
	if got := decodeCodexHomeAvailableModels([]byte(`{"error":{"type":"no_credentials"}}`)); got != nil {
		t.Fatalf("error envelope decoded as models: %#v", got)
	}
}

func TestRewriteCodexSpawnAgentDescriptionNormalizesModelList(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"input":[{
			"type":"additional_tools",
			"role":"developer",
			"tools":[{
				"type":"namespace",
				"name":"collaboration",
				"tools":[
					{"type":"function","name":"send_message","description":"unchanged"},
					{"type":"function","name":"spawn_agent","description":"\n        Available model overrides (optional; inherited parent model is preferred):\n- old duplicate\n- old duplicate\n        Spawns an agent to work on a task.","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}}}}
				]
			}]
		}]
	}`)
	models := []codexSpawnAgentModel{
		{
			id:                     "model-alpha",
			description:            "Alpha model.",
			reasoningEfforts:       []string{"low", "medium", "high"},
			defaultReasoningEffort: "medium",
			serviceTiers:           []string{"priority"},
		},
		{
			id:                     "model-beta",
			description:            "Beta model",
			reasoningEfforts:       []string{"low", "high"},
			defaultReasoningEffort: "low",
		},
	}

	got := rewriteCodexSpawnAgentDescription(payload, models)
	description := gjson.GetBytes(got, "input.0.tools.0.tools.1.description").String()
	wantAlpha := "- `model-alpha`: Alpha model. Reasoning efforts: low, medium (default), high. Service tiers: priority."
	wantBeta := "- `model-beta`: Beta model. Reasoning efforts: low (default), high."
	if !strings.Contains(description, wantAlpha) || !strings.Contains(description, wantBeta) {
		t.Fatalf("description does not contain model metadata:\n%s", description)
	}
	if strings.Contains(description, "old duplicate") {
		t.Fatalf("stale model list was not replaced: %q", description)
	}
	for _, modelID := range []string{"model-alpha", "model-beta"} {
		if count := strings.Count(description, "`"+modelID+"`"); count != 1 {
			t.Fatalf("model %q reference count = %d, want 1", modelID, count)
		}
	}
	if strings.Index(description, "`model-beta`") > strings.Index(description, codexSpawnAgentDescriptionMarker) {
		t.Fatalf("model list was not inserted before spawn instructions: %q", description)
	}
	if gotDescription := gjson.GetBytes(got, "input.0.tools.0.tools.0.description").String(); gotDescription != "unchanged" {
		t.Fatalf("non-spawn tool description = %q, want unchanged", gotDescription)
	}
	if encrypted := gjson.GetBytes(got, "input.0.tools.0.tools.1.parameters.properties.message.encrypted"); encrypted.Exists() {
		t.Fatalf("spawn_agent message encrypted was not removed: %s", encrypted.Raw)
	}
}

func TestRewriteCodexSpawnAgentDescriptionTopLevelWithoutMarker(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Create a worker."}]}]}`)
	models := []codexSpawnAgentModel{{
		id:                     "model-a",
		description:            "Model A.",
		reasoningEfforts:       []string{"medium"},
		defaultReasoningEffort: "medium",
	}}
	got := rewriteCodexSpawnAgentDescription(payload, models)
	description := gjson.GetBytes(got, "tools.0.tools.0.description").String()

	wantSuffix := codexSpawnAgentModelsHeading + "\n- `model-a`: Model A. Reasoning efforts: medium (default)."
	if !strings.HasPrefix(description, "Create a worker.\n\n") || !strings.HasSuffix(description, wantSuffix) {
		t.Fatalf("description = %q, want original text followed by model list", description)
	}
}

func TestCodexSpawnAgentToolPathsIgnoreInvalidContainers(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"input":[{"type":"message","tools":[{"type":"function","name":"spawn_agent","description":"message"}]}],
		"tools":[
			{"type":"function","name":"wrapper","tools":[{"type":"function","name":"spawn_agent","description":"child"}]},
			{"type":"custom","name":"spawn_agent","description":"custom"},
			{"type":"namespace","name":"spawn_agent","description":"namespace"}
		]
	}`)
	if paths := codexSpawnAgentToolPaths(payload); len(paths) != 0 {
		t.Fatalf("invalid container paths = %v, want none", paths)
	}
}

func TestOptimizeCodexMultiAgentV2RequestSkipsNamespaceConflict(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]},{"type":"namespace","name":"collaboration-optimize","tools":[]}]}`)
	headers := http.Header{"User-Agent": []string{"codex-tui/0.145.0"}}
	cfg := &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}}
	got, optimized := OptimizeCodexMultiAgentV2Request(context.Background(), headers, payload, cfg)
	if optimized {
		t.Fatal("namespace conflict unexpectedly enabled optimization")
	}
	if string(got) != string(payload) {
		t.Fatalf("namespace conflict changed payload: %s", got)
	}
}

func TestOptimizeCodexCollaborationNamespaceWithoutModels(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}]}`)
	toolPaths := codexSpawnAgentToolPaths(payload)
	got, optimized := optimizeCodexCollaborationNamespace(payload, toolPaths)
	if !optimized {
		t.Fatal("collaboration namespace was not optimized")
	}
	if namespace := gjson.GetBytes(got, "tools.0.name").String(); namespace != codexOptimizedCollaborationNamespace {
		t.Fatalf("namespace = %q, want collaboration-optimize", namespace)
	}
}

func TestRewriteCodexSpawnAgentDescriptionWithoutModelsStillRemovesEncrypted(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"type":"function","name":"spawn_agent","description":"unchanged","parameters":{"properties":{"message":{"encrypted":true}}}}]}`)
	got := rewriteCodexSpawnAgentDescription(payload, nil)
	if description := gjson.GetBytes(got, "tools.0.description").String(); description != "unchanged" {
		t.Fatalf("description = %q, want unchanged", description)
	}
	if encrypted := gjson.GetBytes(got, "tools.0.parameters.properties.message.encrypted"); encrypted.Exists() {
		t.Fatalf("message encrypted was not removed: %s", encrypted.Raw)
	}
}

func TestRewriteCodexSpawnAgentDescriptionLeavesPayloadWithoutToolUnchanged(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"type":"function","name":"other","description":"unchanged"}]}`)
	models := []codexSpawnAgentModel{{id: "model-a", description: "Model A."}}
	got := rewriteCodexSpawnAgentDescription(payload, models)
	if string(got) != string(payload) {
		t.Fatalf("payload changed without spawn_agent tool: %s", got)
	}
}

func TestRewriteCodexSpawnAgentDescriptionEnabledOptimizesTool(t *testing.T) {
	modelID := "codex-spawn-agent-test-model"
	clientID := "codex-spawn-agent-test-client"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:          modelID,
		Description: "Test agent model.",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}})
	defer modelRegistry.UnregisterClient(clientID)

	payload := []byte(`{"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}]}]}`)
	headers := http.Header{"User-Agent": []string{"Codex Desktop/0.146.0-alpha.3"}}
	cfg := &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}}
	got, optimized := OptimizeCodexMultiAgentV2Request(context.Background(), headers, payload, cfg)
	if !optimized {
		t.Fatal("collaboration namespace was not marked optimized")
	}
	if namespace := gjson.GetBytes(got, "tools.0.name").String(); namespace != codexOptimizedCollaborationNamespace {
		t.Fatalf("namespace = %q, want %q", namespace, codexOptimizedCollaborationNamespace)
	}
	description := gjson.GetBytes(got, "tools.0.tools.0.description").String()
	want := "- `" + modelID + "`: Test agent model. Reasoning efforts: low, medium (default), high."
	if !strings.Contains(description, want) {
		t.Fatalf("description does not contain dynamic model metadata: %q", description)
	}
	if encrypted := gjson.GetBytes(got, "tools.0.tools.0.parameters.properties.message.encrypted"); encrypted.Exists() {
		t.Fatalf("spawn_agent message encrypted was not removed: %s", encrypted.Raw)
	}
}

func TestRestoreCodexMultiAgentV2Response(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.completed",
		"response":{
			"output":[
				{"type":"function_call","name":"spawn_agent","namespace":"collaboration-optimize","arguments":{"namespace":"collaboration-optimize","name":"collaboration-optimize__opaque"}},
				{"type":"function_call","name":"collaboration-optimize__send_message"},
				{"type":"message","namespace":"collaboration-optimize","name":"collaboration-optimize__plain"}
			],
			"tools":[{"type":"namespace","name":"collaboration-optimize"}]
		}
	}`)
	got := RestoreCodexMultiAgentV2Response(payload, true)
	if namespace := gjson.GetBytes(got, "response.output.0.namespace").String(); namespace != codexCollaborationNamespace {
		t.Fatalf("function namespace = %q, want collaboration", namespace)
	}
	if name := gjson.GetBytes(got, "response.output.1.name").String(); name != "collaboration__send_message" {
		t.Fatalf("qualified function name = %q, want collaboration__send_message", name)
	}
	if name := gjson.GetBytes(got, "response.tools.0.name").String(); name != codexCollaborationNamespace {
		t.Fatalf("namespace tool name = %q, want collaboration", name)
	}
	if namespace := gjson.GetBytes(got, "response.output.0.arguments.namespace").String(); namespace != codexOptimizedCollaborationNamespace {
		t.Fatalf("opaque arguments namespace was unexpectedly rewritten: %q", namespace)
	}
	if namespace := gjson.GetBytes(got, "response.output.2.namespace").String(); namespace != codexOptimizedCollaborationNamespace {
		t.Fatalf("ordinary namespace field was unexpectedly rewritten: %q", namespace)
	}
	if name := gjson.GetBytes(got, "response.output.2.name").String(); name != "collaboration-optimize__plain" {
		t.Fatalf("ordinary name field was unexpectedly rewritten: %q", name)
	}
	if unchanged := RestoreCodexMultiAgentV2Response(payload, false); string(unchanged) != string(payload) {
		t.Fatalf("inactive restore changed payload: %s", unchanged)
	}
}

func TestRewriteCodexSpawnAgentDescriptionDisabledLeavesPayloadUnchanged(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"type":"function","name":"spawn_agent","description":"unchanged","parameters":{"properties":{"message":{"encrypted":true}}}}]}`)
	headers := http.Header{"User-Agent": []string{"codex-tui/0.145.0"}}
	got := RewriteCodexSpawnAgentDescription(context.Background(), headers, payload, &config.Config{})
	if string(got) != string(payload) {
		t.Fatalf("disabled optimization changed payload: %s", got)
	}
}

func TestRewriteCodexSpawnAgentDescriptionIgnoresOtherUserAgent(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"tools":[{"type":"function","name":"spawn_agent","description":"unchanged"}]}`)
	headers := http.Header{"User-Agent": []string{"curl/8.7.1"}}
	cfg := &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}}
	got := RewriteCodexSpawnAgentDescription(context.Background(), headers, payload, cfg)
	if string(got) != string(payload) {
		t.Fatalf("payload changed for unrelated User-Agent: %s", got)
	}
}

func TestReplaceCodexSpawnAgentModelsNormalizesSectionsAndPreservesInstructions(t *testing.T) {
	t.Parallel()

	description := codexSpawnAgentModelsHeading + "\n- `old-model`: old\nKeep this multi-agent instruction.\nSpawns an agent.\n" + codexSpawnAgentModelsHeading
	got := replaceCodexSpawnAgentModels(description, "- `new-model`: New model.")
	if strings.Contains(got, "old-model") {
		t.Fatalf("old model list was preserved: %q", got)
	}
	if count := strings.Count(got, codexSpawnAgentModelsHeading); count != 1 {
		t.Fatalf("model heading count = %d, want 1: %q", count, got)
	}
	if !strings.Contains(got, "Keep this multi-agent instruction.") {
		t.Fatalf("following instruction was removed: %q", got)
	}
}

func TestCodexClientUserAgentPrefersGinRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "codex-tui/0.145.0")
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = request
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	headers := http.Header{"User-Agent": []string{"overridden-client/1.0"}}

	if got := codexClientUserAgent(ctx, headers); got != "codex-tui/0.145.0" {
		t.Fatalf("codexClientUserAgent() = %q, want gin request User-Agent", got)
	}
}
