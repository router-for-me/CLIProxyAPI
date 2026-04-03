package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
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

			gotModel := gjson.GetBytes(got, "model").String()
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
	t.Parallel()
	if !useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai"), "gpt-5.4") {
		t.Fatal("expected responses-only registry model to use /responses")
	}
	if !useGitHubCopilotResponsesEndpoint(sdktranslator.FromString("openai"), "gpt-5.4-mini") {
		t.Fatal("expected responses-only registry model to use /responses")
	}
}

func TestUseGitHubCopilotResponsesEndpoint_DynamicRegistryWinsOverStatic(t *testing.T) {
	t.Parallel()

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
	tools := gjson.GetBytes(got, "tools").Array()
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
	if gjson.GetBytes(got, "tool_choice").String() != "auto" {
		t.Fatalf("tool_choice = %s, want auto", gjson.GetBytes(got, "tool_choice").Raw)
	}
}

func TestNormalizeGitHubCopilotResponsesInput_MissingInputExtractedFromSystemAndMessages(t *testing.T) {
	t.Parallel()
	body := []byte(`{"system":"sys text","messages":[{"role":"user","content":"user text"},{"role":"assistant","content":[{"type":"text","text":"assistant text"}]}]}`)
	got := normalizeGitHubCopilotResponsesInput(body)
	in := gjson.GetBytes(got, "input")
	if !in.IsArray() {
		t.Fatalf("input type = %v, want array", in.Type)
	}
	raw := in.Raw
	if !strings.Contains(raw, "sys text") || !strings.Contains(raw, "user text") || !strings.Contains(raw, "assistant text") {
		t.Fatalf("input = %s, want structured array with all texts", raw)
	}
	if gjson.GetBytes(got, "messages").Exists() {
		t.Fatal("messages should be removed after conversion")
	}
	if gjson.GetBytes(got, "system").Exists() {
		t.Fatal("system should be removed after conversion")
	}
}

func TestNormalizeGitHubCopilotResponsesInput_NonStringInputStringified(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":{"foo":"bar"}}`)
	got := normalizeGitHubCopilotResponsesInput(body)
	in := gjson.GetBytes(got, "input")
	if in.Type != gjson.String {
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

	if gjson.GetBytes(got, "service_tier").Exists() {
		t.Fatalf("service_tier should be removed, got %s", gjson.GetBytes(got, "service_tier").Raw)
	}
	if gjson.GetBytes(got, "input").String() != "user text" {
		t.Fatalf("input = %q, want %q", gjson.GetBytes(got, "input").String(), "user text")
	}
}

func TestNormalizeGitHubCopilotResponsesTools_FlattenFunctionTools(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tools":[{"type":"function","function":{"name":"sum","description":"d","parameters":{"type":"object"}}},{"type":"web_search"}]}`)
	got := normalizeGitHubCopilotResponsesTools(body)
	tools := gjson.GetBytes(got, "tools").Array()
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
	tools := gjson.GetBytes(got, "tools").Array()
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
	if gjson.GetBytes(got, "tool_choice.type").String() != "function" {
		t.Fatalf("tool_choice.type = %q, want function", gjson.GetBytes(got, "tool_choice.type").String())
	}
	if gjson.GetBytes(got, "tool_choice.name").String() != "sum" {
		t.Fatalf("tool_choice.name = %q, want sum", gjson.GetBytes(got, "tool_choice.name").String())
	}
}

func TestNormalizeGitHubCopilotResponsesTools_InvalidToolChoiceDowngradeToAuto(t *testing.T) {
	t.Parallel()
	body := []byte(`{"tool_choice":{"type":"function"}}`)
	got := normalizeGitHubCopilotResponsesTools(body)
	if gjson.GetBytes(got, "tool_choice").String() != "auto" {
		t.Fatalf("tool_choice = %s, want auto", gjson.GetBytes(got, "tool_choice").Raw)
	}
}

func TestTranslateGitHubCopilotResponsesNonStreamToClaude_TextMapping(t *testing.T) {
	t.Parallel()
	resp := []byte(`{"id":"resp_1","model":"gpt-5-codex","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":5}}`)
	out := translateGitHubCopilotResponsesNonStreamToClaude(resp)
	if gjson.GetBytes(out, "type").String() != "message" {
		t.Fatalf("type = %q, want message", gjson.GetBytes(out, "type").String())
	}
	if gjson.GetBytes(out, "content.0.type").String() != "text" {
		t.Fatalf("content.0.type = %q, want text", gjson.GetBytes(out, "content.0.type").String())
	}
	if gjson.GetBytes(out, "content.0.text").String() != "hello" {
		t.Fatalf("content.0.text = %q, want hello", gjson.GetBytes(out, "content.0.text").String())
	}
}

func TestTranslateGitHubCopilotResponsesNonStreamToClaude_ToolUseMapping(t *testing.T) {
	t.Parallel()
	resp := []byte(`{"id":"resp_2","model":"gpt-5-codex","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sum","arguments":"{\"a\":1}"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	out := translateGitHubCopilotResponsesNonStreamToClaude(resp)
	if gjson.GetBytes(out, "content.0.type").String() != "tool_use" {
		t.Fatalf("content.0.type = %q, want tool_use", gjson.GetBytes(out, "content.0.type").String())
	}
	if gjson.GetBytes(out, "content.0.name").String() != "sum" {
		t.Fatalf("content.0.name = %q, want sum", gjson.GetBytes(out, "content.0.name").String())
	}
	if gjson.GetBytes(out, "stop_reason").String() != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", gjson.GetBytes(out, "stop_reason").String())
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

// --- Tests for X-Initiator detection logic (Problem L) ---

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
	// When the last role is "user" and the message contains tool_result content,
	// the request is a continuation (e.g. Claude tool result translated to a
	// synthetic user message). Should be "agent".
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
	// When the last message has role "tool", it's clearly agent-initiated.
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
	// Responses API: last item is user-role but history contains assistant → agent.
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
	// Genuine multi-turn: user → assistant (plain text) → user follow-up.
	// No tool messages → should be "user" (not a false-positive).
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
	// User follow-up after a completed tool-use conversation.
	// The last message is a genuine user question — should be "user", not "agent".
	// This aligns with opencode's behavior: only active tool loops are agent-initiated.
	body := []byte(`{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Read","input":{}}]},{"role":"tool","tool_call_id":"tu1","content":"file data"},{"role":"assistant","content":"I read the file."},{"role":"user","content":"What did we do so far?"}]}`)
	e.applyHeaders(req, "token", body)
	if got := req.Header.Get("X-Initiator"); got != "user" {
		t.Fatalf("X-Initiator = %q, want user (genuine follow-up after tool history)", got)
	}
}

// --- Tests for x-github-api-version header (Problem M) ---

func TestApplyHeaders_GitHubAPIVersion(t *testing.T) {
	t.Parallel()
	e := &GitHubCopilotExecutor{}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	e.applyHeaders(req, "token", nil)
	if got := req.Header.Get("X-Github-Api-Version"); got != "2025-04-01" {
		t.Fatalf("X-Github-Api-Version = %q, want 2025-04-01", got)
	}
}

// --- Tests for vision detection (Problem P) ---

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
	// After Responses API normalization, messages is removed — detection should return false
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	if detectVisionContent(body) {
		t.Fatal("expected no vision content when messages field is absent")
	}
}

// --- Tests for applyGitHubCopilotResponsesDefaults ---

func TestApplyGitHubCopilotResponsesDefaults_SetsAllDefaults(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":"hello","reasoning":{"effort":"medium"}}`)
	got := applyGitHubCopilotResponsesDefaults(body)

	if gjson.GetBytes(got, "store").Bool() != false {
		t.Fatalf("store = %v, want false", gjson.GetBytes(got, "store").Raw)
	}
	inc := gjson.GetBytes(got, "include")
	if !inc.IsArray() || inc.Array()[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want [\"reasoning.encrypted_content\"]", inc.Raw)
	}
	if gjson.GetBytes(got, "reasoning.summary").String() != "auto" {
		t.Fatalf("reasoning.summary = %q, want auto", gjson.GetBytes(got, "reasoning.summary").String())
	}
}

func TestApplyGitHubCopilotResponsesDefaults_DoesNotOverrideExisting(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":"hello","store":true,"include":["other"],"reasoning":{"effort":"high","summary":"concise"}}`)
	got := applyGitHubCopilotResponsesDefaults(body)

	if gjson.GetBytes(got, "store").Bool() != true {
		t.Fatalf("store should not be overridden, got %s", gjson.GetBytes(got, "store").Raw)
	}
	if gjson.GetBytes(got, "include").Array()[0].String() != "other" {
		t.Fatalf("include should not be overridden, got %s", gjson.GetBytes(got, "include").Raw)
	}
	if gjson.GetBytes(got, "reasoning.summary").String() != "concise" {
		t.Fatalf("reasoning.summary should not be overridden, got %q", gjson.GetBytes(got, "reasoning.summary").String())
	}
}

func TestApplyGitHubCopilotResponsesDefaults_NoReasoningEffort(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":"hello"}`)
	got := applyGitHubCopilotResponsesDefaults(body)

	if gjson.GetBytes(got, "store").Bool() != false {
		t.Fatalf("store = %v, want false", gjson.GetBytes(got, "store").Raw)
	}
	// reasoning.summary should NOT be set when reasoning.effort is absent
	if gjson.GetBytes(got, "reasoning.summary").Exists() {
		t.Fatalf("reasoning.summary should not be set when reasoning.effort is absent, got %q", gjson.GetBytes(got, "reasoning.summary").String())
	}
}

// --- Tests for normalizeGitHubCopilotReasoningField ---

func TestNormalizeReasoningField_NonStreaming(t *testing.T) {
	t.Parallel()
	data := []byte(`{"choices":[{"message":{"content":"hello","reasoning_text":"I think..."}}]}`)
	got := normalizeGitHubCopilotReasoningField(data)
	rc := gjson.GetBytes(got, "choices.0.message.reasoning_content").String()
	if rc != "I think..." {
		t.Fatalf("reasoning_content = %q, want %q", rc, "I think...")
	}
}

func TestNormalizeReasoningField_Streaming(t *testing.T) {
	t.Parallel()
	data := []byte(`{"choices":[{"delta":{"reasoning_text":"thinking delta"}}]}`)
	got := normalizeGitHubCopilotReasoningField(data)
	rc := gjson.GetBytes(got, "choices.0.delta.reasoning_content").String()
	if rc != "thinking delta" {
		t.Fatalf("reasoning_content = %q, want %q", rc, "thinking delta")
	}
}

func TestNormalizeReasoningField_PreservesExistingReasoningContent(t *testing.T) {
	t.Parallel()
	data := []byte(`{"choices":[{"message":{"reasoning_text":"old","reasoning_content":"existing"}}]}`)
	got := normalizeGitHubCopilotReasoningField(data)
	rc := gjson.GetBytes(got, "choices.0.message.reasoning_content").String()
	if rc != "existing" {
		t.Fatalf("reasoning_content = %q, want %q (should not overwrite)", rc, "existing")
	}
}

func TestNormalizeReasoningField_MultiChoice(t *testing.T) {
	t.Parallel()
	data := []byte(`{"choices":[{"message":{"reasoning_text":"thought-0"}},{"message":{"reasoning_text":"thought-1"}}]}`)
	got := normalizeGitHubCopilotReasoningField(data)
	rc0 := gjson.GetBytes(got, "choices.0.message.reasoning_content").String()
	rc1 := gjson.GetBytes(got, "choices.1.message.reasoning_content").String()
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

// --- Tests for CountTokens (local tiktoken estimation) ---

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
	// The response should contain a positive token count.
	tokens := gjson.GetBytes(resp.Payload, "usage.prompt_tokens").Int()
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
	// Claude source format → should get input_tokens in response
	inputTokens := gjson.GetBytes(resp.Payload, "input_tokens").Int()
	if inputTokens <= 0 {
		// Fallback: check usage.prompt_tokens (depends on translator registration)
		promptTokens := gjson.GetBytes(resp.Payload, "usage.prompt_tokens").Int()
		if promptTokens <= 0 {
			t.Fatalf("expected positive token count, got payload: %s", resp.Payload)
		}
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
	tokens := gjson.GetBytes(resp.Payload, "usage.prompt_tokens").Int()
	// Empty messages should return 0 tokens.
	if tokens != 0 {
		t.Fatalf("expected 0 tokens for empty messages, got %d", tokens)
	}
}

func TestStripUnsupportedBetas_RemovesContext1M(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"claude-opus-4.6","betas":["interleaved-thinking-2025-05-14","context-1m-2025-08-07","claude-code-20250219"],"messages":[]}`)
	result := stripUnsupportedBetas(body)

	betas := gjson.GetBytes(result, "betas")
	if !betas.Exists() {
		t.Fatal("betas field should still exist after stripping")
	}
	for _, item := range betas.Array() {
		if item.String() == "context-1m-2025-08-07" {
			t.Fatal("context-1m-2025-08-07 should have been stripped")
		}
	}
	// Other betas should be preserved
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

	// Should be unchanged
	if string(result) != string(body) {
		t.Fatalf("body should be unchanged when no betas field exists, got %s", string(result))
	}
}

func TestStripUnsupportedBetas_MetadataBetas(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"claude-opus-4.6","metadata":{"betas":["context-1m-2025-08-07","other-beta"]},"messages":[]}`)
	result := stripUnsupportedBetas(body)

	betas := gjson.GetBytes(result, "metadata.betas")
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

	betas := gjson.GetBytes(result, "betas")
	if betas.Exists() {
		t.Fatal("betas field should be deleted when all betas are stripped")
	}
}
