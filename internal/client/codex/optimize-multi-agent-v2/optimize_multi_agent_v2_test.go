package multiagentv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
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

func TestOptimizeCodexMultiAgentV2RequestNormalizesAgentMessageContentOnly(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"input":[{"type":"agent_message","id":"amsg_1","author":"/root","recipient":"/root/worker","content":[{"type":"input_text","text":"Payload:\n"},{"type":"encrypted_content","encrypted_content":"delegated task"}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn_1"}}]}`)
	headers := http.Header{"User-Agent": []string{"Codex Desktop/0.146.0-alpha.3"}}
	cfg := &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}}
	got, namespaceOptimized := OptimizeCodexMultiAgentV2Request(context.Background(), headers, payload, cfg)
	if namespaceOptimized {
		t.Fatal("payload without spawn_agent unexpectedly optimized a namespace")
	}
	message := gjson.GetBytes(got, "input.0")
	if message.Get("type").String() != "agent_message" || message.Get("role").Exists() {
		t.Fatalf("outer agent message changed: %s", got)
	}
	if message.Get("content.1.type").String() != "input_text" || message.Get("content.1.text").String() != "delegated task" {
		t.Fatalf("encrypted content was not normalized: %s", got)
	}
	if message.Get("content.1.encrypted_content").Exists() {
		t.Fatalf("encrypted_content was preserved: %s", got)
	}
	if message.Get("author").String() != "/root" || message.Get("recipient").String() != "/root/worker" || message.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn_1" {
		t.Fatalf("agent message metadata changed: %s", got)
	}

	for _, tt := range []struct {
		name    string
		headers http.Header
		cfg     *config.Config
	}{
		{name: "disabled", headers: headers, cfg: &config.Config{}},
		{name: "unrelated client", headers: http.Header{"User-Agent": []string{"curl/8.7.1"}}, cfg: cfg},
	} {
		t.Run(tt.name, func(t *testing.T) {
			unchanged, _ := OptimizeCodexMultiAgentV2Request(context.Background(), tt.headers, payload, tt.cfg)
			if string(unchanged) != string(payload) {
				t.Fatalf("ineligible request changed: %s", unchanged)
			}
		})
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

func TestRewriteCodexMultiAgentV2InputRewritesAgentMessage(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"model":"gpt-5.4","input":[{
		"type":"agent_message",
		"id":"amsg_019f92ae-84fd-76f0-aa66-5a722dee382e",
		"author":"/root",
		"recipient":"/root/arithmetic_problem",
		"content":[
			{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: /root/arithmetic_problem\nSender: /root\nPayload:\n"},
			{"type":"encrypted_content","encrypted_content":"请出一道四则运算题，并给出答案。全程使用简体中文，题目简洁。"}
		],
		"internal_chat_message_metadata_passthrough":{"turn_id":"019f92ae-7eae-7371-957e-8f6f734edddc"}
	}]}`)
	headers := http.Header{"User-Agent": []string{"Codex Desktop/0.146.0-alpha.3"}}
	cfg := &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}}
	got := RewriteCodexMultiAgentV2Input(context.Background(), headers, payload, cfg)

	if messageType := gjson.GetBytes(got, "input.0.type").String(); messageType != "message" {
		t.Fatalf("type = %q, want message; payload=%s", messageType, got)
	}
	if role := gjson.GetBytes(got, "input.0.role").String(); role != "user" {
		t.Fatalf("role = %q, want user; payload=%s", role, got)
	}
	if partType := gjson.GetBytes(got, "input.0.content.1.type").String(); partType != "input_text" {
		t.Fatalf("content[1].type = %q, want input_text; payload=%s", partType, got)
	}
	if text := gjson.GetBytes(got, "input.0.content.1.text").String(); text != "请出一道四则运算题，并给出答案。全程使用简体中文，题目简洁。" {
		t.Fatalf("content[1].text = %q; payload=%s", text, got)
	}
	if encrypted := gjson.GetBytes(got, "input.0.content.1.encrypted_content"); encrypted.Exists() {
		t.Fatalf("content[1].encrypted_content was preserved: %s", got)
	}
	if author := gjson.GetBytes(got, "input.0.author").String(); author != "/root" {
		t.Fatalf("author = %q, want /root", author)
	}
	if turnID := gjson.GetBytes(got, "input.0.internal_chat_message_metadata_passthrough.turn_id").String(); turnID != "019f92ae-7eae-7371-957e-8f6f734edddc" {
		t.Fatalf("turn_id = %q", turnID)
	}
}

func TestRewriteCodexMultiAgentV2InputConditions(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"task"}]}]}`)
	tests := []struct {
		name      string
		cfg       *config.Config
		userAgent string
		want      bool
	}{
		{
			name:      "Codex Desktop enabled",
			cfg:       &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}},
			userAgent: "Codex Desktop/0.146.0-alpha.3",
			want:      true,
		},
		{
			name:      "codex tui enabled",
			cfg:       &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}},
			userAgent: "codex-tui/0.145.0",
			want:      true,
		},
		{
			name:      "optimization disabled",
			cfg:       &config.Config{},
			userAgent: "codex-tui/0.145.0",
		},
		{
			name:      "unrelated client",
			cfg:       &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}},
			userAgent: "curl/8.7.1",
		},
		{
			name:      "nil config",
			userAgent: "Codex Desktop/0.146.0-alpha.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			headers := http.Header{"User-Agent": []string{tt.userAgent}}
			got := RewriteCodexMultiAgentV2Input(context.Background(), headers, payload, tt.cfg)
			if rewritten := gjson.GetBytes(got, "input.0.type").String() == "message"; rewritten != tt.want {
				t.Fatalf("rewritten = %v, want %v; payload=%s", rewritten, tt.want, got)
			}
		})
	}
}

func TestTranslateRequestWithCodexMultiAgentV2Conditions(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"task"}]}]}`)
	enabledCfg := &config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}}
	eligibleHeaders := http.Header{"User-Agent": []string{"Codex Desktop/0.146.0-alpha.3"}}

	translations := []struct {
		name  string
		to    sdktranslator.Format
		path  string
		want  string
		model string
	}{
		{name: "Claude", to: sdktranslator.FormatClaude, path: "messages.0.content", want: "task", model: "claude-sonnet-4-5"},
		{name: "Gemini", to: sdktranslator.FormatGemini, path: "contents.0.parts.0.text", want: "task", model: "gemini-2.5-pro"},
		{name: "Antigravity", to: sdktranslator.FormatAntigravity, path: "request.contents.0.parts.0.text", want: "task", model: "gemini-2.5-pro"},
		{name: "OpenAI", to: sdktranslator.FormatOpenAI, path: "messages.0.content.0.text", want: "task", model: "chat-model"},
		{name: "Interactions", to: sdktranslator.FormatInteractions, path: "input.0.content.0.text", want: "task", model: "interaction-model"},
	}
	for _, tt := range translations {
		t.Run(tt.name, func(t *testing.T) {
			got := TranslateRequestWithCodexMultiAgentV2(context.Background(), eligibleHeaders, enabledCfg, sdktranslator.FormatOpenAIResponse, tt.to, tt.model, payload, false)
			if value := gjson.GetBytes(got, tt.path).String(); value != tt.want {
				t.Fatalf("%s = %q, want %q; output=%s", tt.path, value, tt.want, got)
			}
		})
	}

	t.Run("disabled optimization", func(t *testing.T) {
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), eligibleHeaders, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "chat-model", payload, false)
		if count := gjson.GetBytes(got, "messages.#").Int(); count != 0 {
			t.Fatalf("disabled optimization translated agent_message; output=%s", got)
		}
	})
	t.Run("unrelated client", func(t *testing.T) {
		headers := http.Header{"User-Agent": []string{"curl/8.7.1"}}
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), headers, enabledCfg, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "chat-model", payload, false)
		if count := gjson.GetBytes(got, "messages.#").Int(); count != 0 {
			t.Fatalf("unrelated client agent_message was translated; output=%s", got)
		}
	})
	t.Run("non-Responses source", func(t *testing.T) {
		got := TranslateRequestWithCodexMultiAgentV2(context.Background(), eligibleHeaders, enabledCfg, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI, "test-model", payload, false)
		if messageType := gjson.GetBytes(got, "input.0.type").String(); messageType != "agent_message" {
			t.Fatalf("non-Responses source changed agent_message; output=%s", got)
		}
	})
	for _, target := range []sdktranslator.Format{sdktranslator.FormatCodex, sdktranslator.FormatOpenAIResponse} {
		t.Run("excluded target "+target.String(), func(t *testing.T) {
			got := TranslateRequestWithCodexMultiAgentV2(context.Background(), eligibleHeaders, enabledCfg, sdktranslator.FormatOpenAIResponse, target, "test-model", payload, false)
			if messageType := gjson.GetBytes(got, "input.0.type").String(); messageType != "agent_message" {
				t.Fatalf("target %s changed agent_message; output=%s", target, got)
			}
		})
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
