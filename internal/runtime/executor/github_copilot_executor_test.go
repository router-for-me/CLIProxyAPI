package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	copilotauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	usagestats "github.com/router-for-me/CLIProxyAPI/v8/internal/usage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGitHubCopilotNormalizeModel_StripsSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		wantModel string
	}{
		{
			name:      "suffix stripped",
			model:     "claude-opus-4.6(medium)",
			wantModel: "claude-opus-4.6",
		},
		{
			name:      "no suffix unchanged",
			model:     "claude-opus-4.6",
			wantModel: "claude-opus-4.6",
		},
		{
			name:      "different suffix stripped",
			model:     "gpt-4o(high)",
			wantModel: "gpt-4o",
		},
		{
			name:      "numeric suffix stripped",
			model:     "gemini-2.5-pro(8192)",
			wantModel: "gemini-2.5-pro",
		},
	}

	e := &GitHubCopilotExecutor{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(`{"model":"` + tt.model + `","messages":[]}`)
			got := e.normalizeModel(tt.model, body)

			gotModel := ojson.GetBytes(got, "model").String()
			if gotModel != tt.wantModel {
				t.Fatalf("normalizeModel() model = %q, want %q", gotModel, tt.wantModel)
			}
		})
	}
}

func TestUseGitHubCopilotResponsesEndpoint_OpenAIResponseSource(t *testing.T) {
	t.Parallel()
	if !useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai-response"), "claude-3-5-sonnet") {
		t.Fatal("expected openai-response source to use /responses")
	}
}

func TestUseGitHubCopilotResponsesEndpoint_CodexModel(t *testing.T) {
	t.Parallel()
	if !useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai"), "gpt-5-codex") {
		t.Fatal("expected codex model to use /responses")
	}
}

func TestUseGitHubCopilotResponsesEndpoint_RegistryResponsesOnlyModel(t *testing.T) {
	// Not parallel: shares global model registry with DynamicRegistryWinsOverStatic.
	if !useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai"), "gpt-5.4") {
		t.Fatal("expected responses-only registry model to use /responses")
	}
	if !useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai"), "gpt-5.4-mini") {
		t.Fatal("expected responses-only registry model to use /responses")
	}
}

func TestUseGitHubCopilotResponsesEndpoint_DynamicRegistryWinsOverStatic(t *testing.T) {
	// Not parallel: mutates global model registry, conflicts with RegistryResponsesOnlyModel.

	reg := registry.GetGlobalRegistry()
	clientID := "github-copilot-test-client"
	reg.RegisterClient(clientID, "github-copilot", []*registry.ModelInfo{
		{
			ID:                 "gpt-5.4",
			SupportedEndpoints: []string{"/chat/completions", "/responses"},
		},
		{
			ID:                 "gpt-5.4-mini",
			SupportedEndpoints: []string{"/chat/completions", "/responses"},
		},
	})
	defer reg.UnregisterClient(clientID)

	if useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai"), "gpt-5.4") {
		t.Fatal("expected dynamic registry definition to take precedence over static fallback")
	}

	if useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai"), "gpt-5.4-mini") {
		t.Fatal("expected dynamic registry definition to take precedence over static fallback")
	}
}

func TestUseGitHubCopilotResponsesEndpoint_DefaultChat(t *testing.T) {
	t.Parallel()
	if useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai"), "claude-3-5-sonnet") {
		t.Fatal("expected default openai source with non-codex model to use /chat/completions")
	}
}

func TestNormalizeGitHubCopilotChatTools_KeepFunctionOnly(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tools":[{"type":"function","function":{"name":"ok"}},{"type":"code_interpreter"}],"tool_choice":"auto"}`)
	got := normalizeGitHubCopilotChatTools(body)
	tools := ojson.GetBytes(got, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	if tools[0].Get("type").String() != "function" {
		t.Fatalf("tool type = %q, want function", tools[0].Get("type").String())
	}
}

func TestNormalizeGitHubCopilotChatTools_InvalidToolChoiceDowngradeToAuto(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tools":[],"tool_choice":{"type":"function","function":{"name":"x"}}}`)
	got := normalizeGitHubCopilotChatTools(body)
	if ojson.GetBytes(got, "tool_choice").String() != "auto" {
		t.Fatalf("tool_choice = %s, want auto", ojson.GetBytes(got, "tool_choice").Raw)
	}
}

func TestNormalizeGitHubCopilotResponsesInput_MissingInputExtractedFromSystemAndMessages(t *testing.T) {
	t.Parallel()
	body := []byte(`{"system":"sys text","messages":[{"role":"user","content":"user text"},{"role":"assistant","content":[{"type":"text","text":"assistant text"}]}]}`)
	got := normalizeGitHubCopilotResponsesInput(body)
	in := ojson.GetBytes(got, "input")
	if !in.IsArray() {
		t.Fatalf("input type = %v, want array", in.Type)
	}
	raw := in.Raw
	if !strings.Contains(raw, "sys text") || !strings.Contains(raw, "user text") || !strings.Contains(raw, "assistant text") {
		t.Fatalf("input = %s, want structured array with all texts", raw)
	}
	if ojson.GetBytes(got, "messages").Exists() {
		t.Fatal("messages should be removed after conversion")
	}
	if ojson.GetBytes(got, "system").Exists() {
		t.Fatal("system should be removed after conversion")
	}
}

func TestNormalizeGitHubCopilotResponsesInput_NonStringInputStringified(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":{"foo":"bar"}}`)
	got := normalizeGitHubCopilotResponsesInput(body)
	in := ojson.GetBytes(got, "input")
	if in.Type != ojson.String {
		t.Fatalf("input type = %v, want string", in.Type)
	}
	if !strings.Contains(in.String(), "foo") {
		t.Fatalf("input = %q, want stringified object", in.String())
	}
}

func TestNormalizeGitHubCopilotResponsesInput_StripsServiceTier(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":"user text","service_tier":"default"}`)
	got := normalizeGitHubCopilotResponsesInput(body)

	if ojson.GetBytes(got, "service_tier").Exists() {
		t.Fatalf("service_tier should be removed, got %s", ojson.GetBytes(got, "service_tier").Raw)
	}
	if ojson.GetBytes(got, "input").String() != "user text" {
		t.Fatalf("input = %q, want %q", ojson.GetBytes(got, "input").String(), "user text")
	}
}

func TestNormalizeGitHubCopilotResponsesTools_FlattenFunctionTools(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tools":[{"type":"function","function":{"name":"sum","description":"d","parameters":{"type":"object"}}},{"type":"web_search"}]}`)
	got := normalizeGitHubCopilotResponsesTools(body)
	tools := ojson.GetBytes(got, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	if tools[0].Get("name").String() != "sum" {
		t.Fatalf("tools[0].name = %q, want sum", tools[0].Get("name").String())
	}
	if !tools[0].Get("parameters").Exists() {
		t.Fatal("expected parameters to be preserved")
	}
}

func TestNormalizeGitHubCopilotResponsesTools_ClaudeFormatTools(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tools":[{"name":"Bash","description":"Run commands","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},{"name":"Read","description":"Read files","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
	got := normalizeGitHubCopilotResponsesTools(body)
	tools := ojson.GetBytes(got, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools len = %d, want 2", len(tools))
	}
	if tools[0].Get("type").String() != "function" {
		t.Fatalf("tools[0].type = %q, want function", tools[0].Get("type").String())
	}
	if tools[0].Get("name").String() != "Bash" {
		t.Fatalf("tools[0].name = %q, want Bash", tools[0].Get("name").String())
	}
	if tools[0].Get("description").String() != "Run commands" {
		t.Fatalf("tools[0].description = %q, want 'Run commands'", tools[0].Get("description").String())
	}
	if !tools[0].Get("parameters").Exists() {
		t.Fatal("expected parameters to be set from input_schema")
	}
	if tools[0].Get("parameters.properties.command").Exists() != true {
		t.Fatal("expected parameters.properties.command to exist")
	}
	if tools[1].Get("name").String() != "Read" {
		t.Fatalf("tools[1].name = %q, want Read", tools[1].Get("name").String())
	}
}

func TestNormalizeGitHubCopilotResponsesTools_FlattenToolChoiceFunctionObject(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tool_choice":{"type":"function","function":{"name":"sum"}}}`)
	got := normalizeGitHubCopilotResponsesTools(body)
	if ojson.GetBytes(got, "tool_choice.type").String() != "function" {
		t.Fatalf("tool_choice.type = %q, want function", ojson.GetBytes(got, "tool_choice.type").String())
	}
	if ojson.GetBytes(got, "tool_choice.name").String() != "sum" {
		t.Fatalf("tool_choice.name = %q, want sum", ojson.GetBytes(got, "tool_choice.name").String())
	}
}

func TestNormalizeGitHubCopilotResponsesTools_InvalidToolChoiceDowngradeToAuto(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tool_choice":{"type":"function"}}`)
	got := normalizeGitHubCopilotResponsesTools(body)
	if ojson.GetBytes(got, "tool_choice").String() != "auto" {
		t.Fatalf("tool_choice = %s, want auto", ojson.GetBytes(got, "tool_choice").Raw)
	}
}

func TestNormalizeGitHubCopilotResponsesTools_PreservesNamespaceAndToolSearch(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tools":[{"type":"namespace","name":"mcp__demo","description":"demo","tools":[{"type":"function","name":"status"}]},{"type":"tool_search","execution":"client","description":"Searches deferred tools."},{"type":"function","name":"local_tool"}]}`)
	got := normalizeGitHubCopilotResponsesTools(body)
	tools := ojson.GetBytes(got, "tools").Array()
	if len(tools) != 3 {
		t.Fatalf("tools len = %d, want 3", len(tools))
	}
	if tools[0].Get("type").String() != "namespace" {
		t.Fatalf("tools[0].type = %q, want namespace", tools[0].Get("type").String())
	}
	if tools[0].Get("name").String() != "mcp__demo" {
		t.Fatalf("tools[0].name = %q, want mcp__demo", tools[0].Get("name").String())
	}
	if tools[1].Get("type").String() != "tool_search" {
		t.Fatalf("tools[1].type = %q, want tool_search", tools[1].Get("type").String())
	}
	if tools[2].Get("name").String() != "local_tool" {
		t.Fatalf("tools[2].name = %q, want local_tool", tools[2].Get("name").String())
	}
}

func TestTranslateGitHubCopilotResponsesNonStreamToClaude_TextMapping(t *testing.T) {
	t.Parallel()
	resp := []byte(`{"id":"resp_1","model":"gpt-5-codex","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":5}}`)
	out := translateGitHubCopilotResponsesNonStreamToClaude(resp)
	if ojson.GetBytes(out, "type").String() != "message" {
		t.Fatalf("type = %q, want message", ojson.GetBytes(out, "type").String())
	}
	if ojson.GetBytes(out, "content.0.type").String() != "text" {
		t.Fatalf("content.0.type = %q, want text", ojson.GetBytes(out, "content.0.type").String())
	}
	if ojson.GetBytes(out, "content.0.text").String() != "hello" {
		t.Fatalf("content.0.text = %q, want hello", ojson.GetBytes(out, "content.0.text").String())
	}
}

func TestTranslateGitHubCopilotResponsesNonStreamToClaude_ToolUseMapping(t *testing.T) {
	t.Parallel()
	resp := []byte(`{"id":"resp_2","model":"gpt-5-codex","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sum","arguments":"{\"a\":1}"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	out := translateGitHubCopilotResponsesNonStreamToClaude(resp)
	if ojson.GetBytes(out, "content.0.type").String() != "tool_use" {
		t.Fatalf("content.0.type = %q, want tool_use", ojson.GetBytes(out, "content.0.type").String())
	}
	if ojson.GetBytes(out, "content.0.name").String() != "sum" {
		t.Fatalf("content.0.name = %q, want sum", ojson.GetBytes(out, "content.0.name").String())
	}
	if ojson.GetBytes(out, "stop_reason").String() != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", ojson.GetBytes(out, "stop_reason").String())
	}
}

func TestTranslateGitHubCopilotResponsesStreamToClaude_TextLifecycle(t *testing.T) {
	t.Parallel()
	var param any

	created := translateGitHubCopilotResponsesStreamToClaude([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5-codex"}}`), &param)
	if len(created) == 0 || !strings.Contains(string(created[0]), "message_start") {
		t.Fatalf("created events = %#v, want message_start", created)
	}

	delta := translateGitHubCopilotResponsesStreamToClaude([]byte(`data: {"type":"response.output_text.delta","delta":"he"}`), &param)
	var joinedDelta string
	for _, d := range delta {
		joinedDelta += string(d)
	}
	if !strings.Contains(joinedDelta, "content_block_start") || !strings.Contains(joinedDelta, "text_delta") {
		t.Fatalf("delta events = %#v, want content_block_start + text_delta", delta)
	}

	completed := translateGitHubCopilotResponsesStreamToClaude([]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":7,"output_tokens":9}}}`), &param)
	var joinedCompleted string
	for _, c := range completed {
		joinedCompleted += string(c)
	}
	if !strings.Contains(joinedCompleted, "message_delta") || !strings.Contains(joinedCompleted, "message_stop") {
		t.Fatalf("completed events = %#v, want message_delta + message_stop", completed)
	}
}

func TestApplyHeaders_XInitiator_UserOnly(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	body := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Fatalf("X-Initiator = %q, want user", got)
	}
}

func TestApplyHeaders_XInitiator_AgentWhenLastUserButHistoryHasAssistant(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	body := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"I will read the file"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"file contents..."}]}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "agent" {
		t.Fatalf("X-Initiator = %q, want agent (last user contains tool_result)", got)
	}
}

func TestApplyHeaders_XInitiator_AgentWithToolRole(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	body := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"tool","content":"result"}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "agent" {
		t.Fatalf("X-Initiator = %q, want agent (last role is tool)", got)
	}
}

func TestApplyHeaders_XInitiator_InputArrayLastAssistantMessage(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hi"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "agent" {
		t.Fatalf("X-Initiator = %q, want agent (last role is assistant)", got)
	}
}

func TestApplyHeaders_XInitiator_InputArrayAgentWhenLastUserButHistoryHasAssistant(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	body := []byte(`{"input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I can help"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"Do X"}]}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "agent" {
		t.Fatalf("X-Initiator = %q, want agent (history has assistant)", got)
	}
}

func TestApplyHeaders_XInitiator_InputArrayLastFunctionCallOutput(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Use tool"}]},{"type":"function_call","call_id":"c1","name":"Read","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "agent" {
		t.Fatalf("X-Initiator = %q, want agent (last item maps to tool role)", got)
	}
}

func TestApplyHeaders_XInitiator_UserInMultiTurnNoTools(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	body := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"Hi there!"},{"role":"user","content":"what is 2+2?"}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Fatalf("X-Initiator = %q, want user (genuine multi-turn, no tools)", got)
	}
}

func TestApplyHeaders_XInitiator_UserFollowUpAfterToolHistory(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	body := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Read","input":{}}]},{"role":"tool","tool_call_id":"tu1","content":"file data"},{"role":"assistant","content":"I read the file."},{"role":"user","content":"What did we do so far?"}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Fatalf("X-Initiator = %q, want user (genuine follow-up after tool history)", got)
	}
}

func TestApplyHeaders_GitHubAPIVersion(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	e.applyHeaders(req, "token", nil)
	if got := req.Header.Get("X-Github-Api-Version"); got != "2025-04-01" {
		t.Fatalf("X-Github-Api-Version = %q, want 2025-04-01", got)
	}
}

func TestDetectVisionContent_WithImageURL(t *testing.T) {
	t.Parallel()
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]}`)
	if !detectVisionContent(body) {
		t.Fatal("expected vision content to be detected")
	}
}

func TestDetectVisionContent_WithImageType(t *testing.T) {
	t.Parallel()
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"data":"abc","media_type":"image/png"}}]}]}`)
	if !detectVisionContent(body) {
		t.Fatal("expected image type to be detected")
	}
}

func TestDetectVisionContent_NoVision(t *testing.T) {
	t.Parallel()
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	if detectVisionContent(body) {
		t.Fatal("expected no vision content")
	}
}

func TestDetectVisionContent_NoMessages(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	if detectVisionContent(body) {
		t.Fatal("expected no vision content when messages field is absent")
	}
}

func TestApplyGitHubCopilotResponsesDefaults_SetsAllDefaults(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":"hello","reasoning":{"effort":"medium"}}`)
	got := applyGitHubCopilotResponsesDefaults(body)

	if ojson.GetBytes(got, "store").Bool() != false {
		t.Fatalf("store = %v, want false", ojson.GetBytes(got, "store").Raw)
	}
	inc := ojson.GetBytes(got, "include")
	if !inc.IsArray() || inc.Array()[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want [\"reasoning.encrypted_content\"]", inc.Raw)
	}
	if ojson.GetBytes(got, "reasoning.summary").String() != "auto" {
		t.Fatalf("reasoning.summary = %q, want auto", ojson.GetBytes(got, "reasoning.summary").String())
	}
}

func TestApplyGitHubCopilotResponsesDefaults_DoesNotOverrideExisting(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":"hello","store":true,"include":["other"],"reasoning":{"effort":"high","summary":"concise"}}`)
	got := applyGitHubCopilotResponsesDefaults(body)

	if ojson.GetBytes(got, "store").Bool() != true {
		t.Fatalf("store should not be overridden, got %s", ojson.GetBytes(got, "store").Raw)
	}
	if ojson.GetBytes(got, "include").Array()[0].String() != "other" {
		t.Fatalf("include should not be overridden, got %s", ojson.GetBytes(got, "include").Raw)
	}
	if ojson.GetBytes(got, "reasoning.summary").String() != "concise" {
		t.Fatalf("reasoning.summary should not be overridden, got %q", ojson.GetBytes(got, "reasoning.summary").String())
	}
}

func TestApplyGitHubCopilotResponsesDefaults_NoReasoningEffort(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":"hello"}`)
	got := applyGitHubCopilotResponsesDefaults(body)

	if ojson.GetBytes(got, "store").Bool() != false {
		t.Fatalf("store = %v, want false", ojson.GetBytes(got, "store").Raw)
	}
	if ojson.GetBytes(got, "reasoning.summary").Exists() {
		t.Fatalf("reasoning.summary should not be set when reasoning.effort is absent, got %q", ojson.GetBytes(got, "reasoning.summary").String())
	}
}

func TestNormalizeReasoningField_NonStreaming(t *testing.T) {
	t.Parallel()
	data := []byte(`{"choices":[{"message":{"content":"hello","reasoning_text":"I think..."}}]}`)
	got := normalizeGitHubCopilotReasoningField(data)
	rc := ojson.GetBytes(got, "choices.0.message.reasoning_content").String()
	if rc != "I think..." {
		t.Fatalf("reasoning_content = %q, want %q", rc, "I think...")
	}
}

func TestNormalizeReasoningField_Streaming(t *testing.T) {
	t.Parallel()
	data := []byte(`{"choices":[{"delta":{"reasoning_text":"thinking delta"}}]}`)
	got := normalizeGitHubCopilotReasoningField(data)
	rc := ojson.GetBytes(got, "choices.0.delta.reasoning_content").String()
	if rc != "thinking delta" {
		t.Fatalf("reasoning_content = %q, want %q", rc, "thinking delta")
	}
}

func TestNormalizeReasoningField_PreservesExistingReasoningContent(t *testing.T) {
	t.Parallel()
	data := []byte(`{"choices":[{"message":{"reasoning_text":"old","reasoning_content":"existing"}}]}`)
	got := normalizeGitHubCopilotReasoningField(data)
	rc := ojson.GetBytes(got, "choices.0.message.reasoning_content").String()
	if rc != "existing" {
		t.Fatalf("reasoning_content = %q, want %q (should not overwrite)", rc, "existing")
	}
}

func TestNormalizeReasoningField_MultiChoice(t *testing.T) {
	t.Parallel()
	data := []byte(`{"choices":[{"message":{"reasoning_text":"thought-0"}},{"message":{"reasoning_text":"thought-1"}}]}`)
	got := normalizeGitHubCopilotReasoningField(data)
	rc0 := ojson.GetBytes(got, "choices.0.message.reasoning_content").String()
	rc1 := ojson.GetBytes(got, "choices.1.message.reasoning_content").String()
	if rc0 != "thought-0" {
		t.Fatalf("choices[0].reasoning_content = %q, want %q", rc0, "thought-0")
	}
	if rc1 != "thought-1" {
		t.Fatalf("choices[1].reasoning_content = %q, want %q", rc1, "thought-1")
	}
}

func TestNormalizeReasoningField_NoChoices(t *testing.T) {
	t.Parallel()
	data := []byte(`{"id":"chatcmpl-123"}`)
	got := normalizeGitHubCopilotReasoningField(data)
	if string(got) != string(data) {
		t.Fatalf("expected no change, got %s", string(got))
	}
}

func TestApplyHeaders_OpenAIIntentValue(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	e.applyHeaders(req, "token", nil)
	if got := req.Header.Get("Openai-Intent"); got != "conversation-edits" {
		t.Fatalf("Openai-Intent = %q, want conversation-edits", got)
	}
}

func TestCountTokens_ReturnsPositiveCount(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hello, world!"}]}`)
	resp, err := e.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("CountTokens() error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("CountTokens() returned empty payload")
	}
	tokens := ojson.GetBytes(resp.Payload, "usage.prompt_tokens").Int()
	if tokens <= 0 {
		t.Fatalf("expected positive token count, got %d", tokens)
	}
}

func TestCountTokens_ClaudeSourceFormatTranslates(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"Tell me a joke"}],"max_tokens":1024}`)
	resp, err := e.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("CountTokens() error: %v", err)
	}
	inputTokens := ojson.GetBytes(resp.Payload, "input_tokens").Int()
	if inputTokens <= 0 {
		// Fallback: check usage.prompt_tokens (depends on translator registration)
		promptTokens := ojson.GetBytes(resp.Payload, "usage.prompt_tokens").Int()
		if promptTokens <= 0 {
			t.Fatalf("expected positive token count, got payload: %s", resp.Payload)
		}
	}
}

func TestGitHubCopilotExecute_ClaudeModelUsesNativeGateway(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotQuery string
	var gotAuth string
	var gotAPIVersion string
	var gotEditorVersion string
	var gotIntent string
	var gotInitiator string
	var gotBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotAPIVersion = r.Header.Get("X-Github-Api-Version")
		gotEditorVersion = r.Header.Get("Editor-Version")
		gotIntent = r.Header.Get("Openai-Intent")
		gotInitiator = r.Header.Get("X-Initiator")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-sonnet-4.6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	e := NewGitHubCopilotExecutor(&config.Config{})
	e.cache["gh-access-token"] = &cachedAPIToken{
		token:       "copilot-api-token",
		apiEndpoint: server.URL,
		expiresAt:   time.Now().Add(time.Hour),
	}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "gh-access-token"}}
	payload := []byte(`{"model":"claude-sonnet-4.6","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}`)

	resp, err := e.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4.6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/messages")
	}
	if gotQuery != "beta=true" {
		t.Fatalf("query = %q, want %q", gotQuery, "beta=true")
	}
	if gotAuth != "Bearer copilot-api-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer copilot-api-token")
	}
	if gotAPIVersion != copilotGitHubAPIVer {
		t.Fatalf("X-Github-Api-Version = %q, want %q", gotAPIVersion, copilotGitHubAPIVer)
	}
	if gotEditorVersion != copilotEditorVersion {
		t.Fatalf("Editor-Version = %q, want %q", gotEditorVersion, copilotEditorVersion)
	}
	if gotIntent != copilotOpenAIIntent {
		t.Fatalf("Openai-Intent = %q, want %q", gotIntent, copilotOpenAIIntent)
	}
	if gotInitiator != "user" {
		t.Fatalf("X-Initiator = %q, want %q", gotInitiator, "user")
	}
	if ojson.GetBytes(gotBody, "model").String() != "claude-sonnet-4.6" {
		t.Fatalf("upstream model = %q, want %q", ojson.GetBytes(gotBody, "model").String(), "claude-sonnet-4.6")
	}
	if ojson.GetBytes(resp.Payload, "content.0.text").String() != "ok" {
		t.Fatalf("response text = %q, want %q", ojson.GetBytes(resp.Payload, "content.0.text").String(), "ok")
	}
}

func TestGitHubCopilotExecuteStream_ClaudeModelUsesNativeGateway(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotInitiator string
	var gotAPIVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotInitiator = r.Header.Get("X-Initiator")
		gotAPIVersion = r.Header.Get("X-Github-Api-Version")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4.6\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	e := NewGitHubCopilotExecutor(&config.Config{})
	e.cache["gh-access-token"] = &cachedAPIToken{
		token:       "copilot-api-token",
		apiEndpoint: server.URL,
		expiresAt:   time.Now().Add(time.Hour),
	}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "gh-access-token"}}
	payload := []byte(`{"model":"claude-sonnet-4.6","stream":true,"max_tokens":256,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"notes.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file contents"}]}]}`)

	result, err := e.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4.6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error: %v", err)
	}

	var joined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		joined.Write(chunk.Payload)
	}

	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/messages")
	}
	if gotInitiator != "agent" {
		t.Fatalf("X-Initiator = %q, want %q", gotInitiator, "agent")
	}
	if gotAPIVersion != copilotGitHubAPIVer {
		t.Fatalf("X-Github-Api-Version = %q, want %q", gotAPIVersion, copilotGitHubAPIVer)
	}
	if !strings.Contains(joined.String(), "message_start") || !strings.Contains(joined.String(), "text_delta") {
		t.Fatalf("stream = %q, want Claude SSE payload", joined.String())
	}
}

func TestCountTokens_EmptyPayload(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	resp, err := e.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"model":"gpt-4o","messages":[]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("CountTokens() error: %v", err)
	}
	tokens := ojson.GetBytes(resp.Payload, "usage.prompt_tokens").Int()
	if tokens != 0 {
		t.Fatalf("expected 0 tokens for empty messages, got %d", tokens)
	}
}

func TestStripUnsupportedBetas_RemovesContext1M(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"claude-opus-4.6","betas":["interleaved-thinking-2025-05-14","context-1m-2025-08-07","claude-code-20250219"],"messages":[]}`)
	result := stripUnsupportedBetas(body)

	betas := ojson.GetBytes(result, "betas")
	if !betas.Exists() {
		t.Fatal("betas field should still exist after stripping")
	}
	for _, item := range betas.Array() {
		if item.String() == "context-1m-2025-08-07" {
			t.Fatal("context-1m-2025-08-07 should have been stripped")
		}
	}
	found := false
	for _, item := range betas.Array() {
		if item.String() == "interleaved-thinking-2025-05-14" {
			found = true
		}
	}
	if !found {
		t.Fatal("other betas should be preserved")
	}
}

func TestStripUnsupportedBetas_NoBetasField(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	result := stripUnsupportedBetas(body)

	if string(result) != string(body) {
		t.Fatalf("body should be unchanged when no betas field exists, got %s", string(result))
	}
}

func TestStripUnsupportedBetas_MetadataBetas(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"claude-opus-4.6","metadata":{"betas":["context-1m-2025-08-07","other-beta"]},"messages":[]}`)
	result := stripUnsupportedBetas(body)

	betas := ojson.GetBytes(result, "metadata.betas")
	if !betas.Exists() {
		t.Fatal("metadata.betas field should still exist after stripping")
	}
	for _, item := range betas.Array() {
		if item.String() == "context-1m-2025-08-07" {
			t.Fatal("context-1m-2025-08-07 should have been stripped from metadata.betas")
		}
	}
	if betas.Array()[0].String() != "other-beta" {
		t.Fatal("other betas in metadata.betas should be preserved")
	}
}

func TestStripUnsupportedBetas_AllBetasStripped(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"claude-opus-4.6","betas":["context-1m-2025-08-07"],"messages":[]}`)
	result := stripUnsupportedBetas(body)

	betas := ojson.GetBytes(result, "betas")
	if betas.Exists() {
		t.Fatal("betas field should be deleted when all betas are stripped")
	}
}

func TestCopilotModelEntry_Limits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		capabilities map[string]any
		wantNil      bool
		wantPrompt   int
		wantOutput   int
		wantContext  int
	}{
		{
			name:         "nil capabilities",
			capabilities: nil,
			wantNil:      true,
		},
		{
			name:         "no limits key",
			capabilities: map[string]any{"family": "claude-opus-4.6"},
			wantNil:      true,
		},
		{
			name:         "limits is not a map",
			capabilities: map[string]any{"limits": "invalid"},
			wantNil:      true,
		},
		{
			name: "all zero values",
			capabilities: map[string]any{
				"limits": map[string]any{
					"max_context_window_tokens": float64(0),
					"max_prompt_tokens":         float64(0),
					"max_output_tokens":         float64(0),
				},
			},
			wantNil: true,
		},
		{
			name: "individual account limits (128K prompt)",
			capabilities: map[string]any{
				"limits": map[string]any{
					"max_context_window_tokens": float64(144000),
					"max_prompt_tokens":         float64(128000),
					"max_output_tokens":         float64(64000),
				},
			},
			wantNil:     false,
			wantPrompt:  128000,
			wantOutput:  64000,
			wantContext: 144000,
		},
		{
			name: "business account limits (168K prompt)",
			capabilities: map[string]any{
				"limits": map[string]any{
					"max_context_window_tokens": float64(200000),
					"max_prompt_tokens":         float64(168000),
					"max_output_tokens":         float64(32000),
				},
			},
			wantNil:     false,
			wantPrompt:  168000,
			wantOutput:  32000,
			wantContext: 200000,
		},
		{
			name: "partial limits (only prompt)",
			capabilities: map[string]any{
				"limits": map[string]any{
					"max_prompt_tokens": float64(128000),
				},
			},
			wantNil:    false,
			wantPrompt: 128000,
			wantOutput: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entry := copilotauth.CopilotModelEntry{
				ID:           "claude-opus-4.6",
				Capabilities: tt.capabilities,
			}
			limits := entry.Limits()
			if tt.wantNil {
				if limits != nil {
					t.Fatalf("expected nil limits, got %+v", limits)
				}
				return
			}
			if limits == nil {
				t.Fatal("expected non-nil limits, got nil")
			}
			if limits.MaxPromptTokens != tt.wantPrompt {
				t.Errorf("MaxPromptTokens = %d, want %d", limits.MaxPromptTokens, tt.wantPrompt)
			}
			if limits.MaxOutputTokens != tt.wantOutput {
				t.Errorf("MaxOutputTokens = %d, want %d", limits.MaxOutputTokens, tt.wantOutput)
			}
			if tt.wantContext > 0 && limits.MaxContextWindowTokens != tt.wantContext {
				t.Errorf("MaxContextWindowTokens = %d, want %d", limits.MaxContextWindowTokens, tt.wantContext)
			}
		})
	}
}

func TestGitHubCopilotResponsesStreamPreservesSSEFraming(t *testing.T) {
	wire := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"text\":{\"format\":{\"type\":\"text\"}},\"output\":[]}}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, wire)
	}))
	defer server.Close()
	e := NewGitHubCopilotExecutor(&config.Config{})
	e.cache["test-token"] = &cachedAPIToken{token: "test-api-token", apiEndpoint: server.URL, expiresAt: time.Now().Add(time.Hour)}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "test-token"}}
	payload := []byte(`{"model":"gpt-5.6-luna","input":[{"role":"user","content":"Hello"}],"stream":true}`)
	result, err := e.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "gpt-5.6-luna", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: payload})
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		joined.Write(chunk.Payload)
	}
	if joined.String() != wire {
		t.Fatalf("SSE framing changed: got %q, want %q", joined.String(), wire)
	}
}

type copilotUsageCaptureKey struct{}
type copilotUsageCapturePlugin struct{}

func (copilotUsageCapturePlugin) HandleUsage(ctx context.Context, record usage.Record) {
	if records, ok := ctx.Value(copilotUsageCaptureKey{}).(chan usage.Record); ok {
		records <- record
	}
}

func TestGitHubCopilotResponsesStreamAccounting(t *testing.T) {
	// The named plugin retains no test state; capture is scoped to each request context.
	usage.RegisterNamedPlugin("test:copilot-responses-accounting", copilotUsageCapturePlugin{})
	const tokenUsage = `{"input_tokens":9,"output_tokens":5,"total_tokens":14,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}`
	const delta = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"
	completed := func(usageJSON string) string {
		return `{"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[],"usage":` + usageJSON + `}}`
	}
	for _, tc := range []struct {
		name         string
		event        string
		prefix       string
		source       string
		model        string
		truncated    bool
		wantFailure  bool
		wantReadErr  bool
		wantTokens   int64
		wantTerminal string
	}{
		{name: "nested usage", event: completed(tokenUsage), wantTokens: 14},
		{name: "completion before transport error", event: completed(tokenUsage), truncated: true, wantTokens: 14},
		{name: "completion without usage", event: completed("null")},
		{name: "completion without usage before transport error", event: completed("null"), truncated: true},
		{name: "early service tier does not publish", prefix: "data: {\"type\":\"response.created\",\"response\":{\"service_tier\":\"default\",\"usage\":null}}\n\n", event: completed(tokenUsage), wantTokens: 14},
		{name: "early null usage does not publish", prefix: "data: {\"type\":\"response.created\",\"usage\":null}\n\n", event: completed(tokenUsage), wantTokens: 14},
		{name: "preterminal transport error", truncated: true, wantFailure: true, wantReadErr: true},
		{name: "metadata before transport error", prefix: "data: {\"type\":\"response.created\",\"service_tier\":\"default\",\"usage\":null}\n\n", truncated: true, wantFailure: true, wantReadErr: true},
		{name: "explicit failure", event: `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"Failed"}}}`, wantFailure: true},
		{name: "error event", event: `{"type":"error","error":{"type":"server_error","message":"Failed"}}`, wantFailure: true},
		{name: "incomplete is successful", event: `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":` + tokenUsage + `}}`, truncated: true, wantTokens: 14},
		{name: "Claude terminal conversion", source: "claude", event: completed(tokenUsage), truncated: true, wantTokens: 14, wantTerminal: "event: message_stop\n"},
		{name: "Chat usage control", source: "openai", model: "gpt-4o", event: `{"choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":` + tokenUsage + `}`, truncated: true, wantReadErr: true, wantTokens: 14},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.prefix + delta
			if tc.event != "" {
				wire += "event: " + ojson.Get(tc.event, "type").String() + "\ndata: " + tc.event + "\n\n"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/responses"
				if tc.model == "gpt-4o" {
					wantPath = "/chat/completions"
				}
				if r.URL.Path != wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.truncated {
					w.Header().Set("Content-Length", fmt.Sprint(len(wire)+100))
				}
				_, _ = io.WriteString(w, wire)
			}))
			defer server.Close()
			e := NewGitHubCopilotExecutor(&config.Config{})
			e.cache["test-token"] = &cachedAPIToken{token: "test-api-token", apiEndpoint: server.URL, expiresAt: time.Now().Add(time.Hour)}
			auth := &cliproxyauth.Auth{ID: t.Name(), Metadata: map[string]any{"access_token": "test-token"}}
			records := make(chan usage.Record, 16)
			ctx := context.WithValue(context.Background(), copilotUsageCaptureKey{}, records)
			source := tc.source
			if source == "" {
				source = "openai-response"
			}
			model := tc.model
			if model == "" {
				model = "gpt-5-codex"
			}
			payload := []byte(fmt.Sprintf(`{"model":%q,"input":"Hello","messages":[{"role":"user","content":"Hello"}],"stream":true}`, model))
			result, err := e.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(source), OriginalRequest: payload,
			})
			if err != nil {
				t.Fatal(err)
			}
			var joined strings.Builder
			var streamErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
				joined.Write(chunk.Payload)
			}
			if tc.wantReadErr {
				if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
					t.Errorf("stream error = %v, want unexpected EOF", streamErr)
				}
			} else if streamErr != nil {
				t.Errorf("stream error = %v, want nil", streamErr)
			}
			if source == "openai-response" && joined.String() != wire {
				t.Errorf("SSE framing changed: got %q, want %q", joined.String(), wire)
			}
			if tc.wantTerminal != "" && !strings.Contains(joined.String(), tc.wantTerminal) {
				t.Errorf("terminal %q missing from %q", tc.wantTerminal, joined.String())
			}

			// A queue barrier verifies all records, including duplicates, without sleeping.
			usage.PublishRecord(ctx, usage.Record{Model: "accounting-barrier"})
			stats := usagestats.NewRequestStatistics()
			var captured []usage.Record
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
		drain:
			for {
				select {
				case record := <-records:
					if record.Model == "accounting-barrier" {
						break drain
					}
					captured = append(captured, record)
					stats.Record(ctx, record)
				case <-timer.C:
					t.Fatal("timed out waiting for usage queue barrier")
				}
			}
			if len(captured) != 1 {
				t.Fatalf("usage records = %d, want exactly one", len(captured))
			}
			record := captured[0]
			if record.Failed != tc.wantFailure || record.Detail.TotalTokens != tc.wantTokens {
				t.Errorf("usage = %+v, want failed=%v total=%d", record, tc.wantFailure, tc.wantTokens)
			}
			if tc.wantTokens != 0 && (record.Detail.InputTokens != 9 || record.Detail.OutputTokens != 5 || record.Detail.CachedTokens != 3 || record.Detail.ReasoningTokens != 2) {
				t.Errorf("token breakdown = %+v, want input=9 output=5 cached=3 reasoning=2", record.Detail)
			}
			snapshot := stats.Snapshot()
			wantFailures := int64(0)
			if tc.wantFailure {
				wantFailures = 1
			}
			if snapshot.TotalRequests != 1 || snapshot.FailureCount != wantFailures || snapshot.SuccessCount != 1-wantFailures || snapshot.TotalTokens != tc.wantTokens {
				t.Errorf("management counts = %+v, want requests=1 failures=%d total=%d", snapshot, wantFailures, tc.wantTokens)
			}
		})
	}
}
