package executor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tiktoken-go/tokenizer"
)

type keyPayloadRequestFixture struct {
	family      string
	destination string
	model       string
	source      sdktranslator.Format
	payload     string
	reply       string
	streamReply string
	alt         string
	executor    cliproxyauth.ProviderExecutor
	collection  string
	counter     func(tokenizer.Codec, []byte) (int64, error)
}

type keyPayloadInvocation func(context.Context, *cliproxyauth.Manager, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) error

type keyPayloadMatcherCase struct {
	name          string
	model         string
	rule          config.PayloadModelRule
	params        []string
	systemDeletes int
	expectedPatch map[string]any
}

type keyPayloadAttemptFixture struct {
	retry int
	roles []string
}

type keyPayloadTransportProvider struct {
	http.RoundTripper
}

// RoundTripperFor binds each synthetic credential to the same in-process capture.
func (provider *keyPayloadTransportProvider) RoundTripperFor(_auth *cliproxyauth.Auth) http.RoundTripper {
	return provider
}

// keyPayloadTestContext verifies transport selection before any synthetic request.
func keyPayloadTestContext(t *testing.T, ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, transport http.RoundTripper) (context.Context, *keyPayloadTransportProvider) {
	t.Helper()
	provider := &keyPayloadTransportProvider{RoundTripper: transport}
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(provider))
	if helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Transport != provider {
		t.Fatal("synthetic capture was overridden by request, credential or global proxy selection")
	}
	return ctx, provider
}

// keyPayloadRuntimeConfig supplies parsed target/control definitions and supported same-secret cases.
func keyPayloadRuntimeConfig(t *testing.T) *config.Config {
	t.Helper()
	var raw strings.Builder
	raw.WriteString("api-keys:\n")
	for _, family := range []struct{ name, fields, controlGroup string }{
		{name: "gemini", fields: "headers: {X-Role: target},"},
		{name: "interactions", fields: "headers: {X-Role: target},"},
		{name: "vertex", fields: "headers: {X-Role: target},", controlGroup: "    - base-url: https://synthetic-control.invalid\n      keys:\n"},
		{name: "codex", fields: "headers: {X-Role: target},"},
		{name: "claude", fields: "cloak: {mode: uncloak}, headers: {X-Role: target},"},
		{name: "xai", fields: "headers: {X-Role: target},"},
		{name: "meta", fields: "headers: {X-Role: target},"},
		{name: "openai-compatibility"},
	} {
		fmt.Fprintf(&raw, "  %s:\n    - name: custom-fixture\n      base-url: https://synthetic.invalid\n      keys:\n        - {api-key: syntheticSharedKey123, %s payload: {filter: [{models: [{name: '*'}], params: [local_delete]}]}}\n%s        - {api-key: syntheticSharedKey123}\n", family.name, family.fields, family.controlGroup)
	}
	raw.WriteString(`requests:
  payload:
    default:
      - {models: [{name: '*'}], params: {global_default: true}}
    default-raw:
      - {models: [{name: '*'}], params: {global_default_raw: 'true'}}
    override:
      - {models: [{name: '*'}], params: {global_keep: true, local_delete: delete-marker, global_delete: delete-marker}}
    override-raw:
      - {models: [{name: '*'}], params: {global_override_raw: 'true'}}
    filter:
      - {models: [{name: '*'}], params: [global_delete]}
`)
	cfg, err := config.ParseConfigBytes([]byte(raw.String()))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// runKeyPayloadMessage adapts the manager message contract to the capture harness.
func runKeyPayloadMessage(ctx context.Context, manager *cliproxyauth.Manager, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	_, err := manager.Execute(ctx, providers, req, opts)
	return err
}

// runKeyPayloadStream drains the actual manager stream and propagates chunk errors.
func runKeyPayloadStream(ctx context.Context, manager *cliproxyauth.Manager, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	result, err := manager.ExecuteStream(ctx, providers, req, opts)
	if err != nil {
		return err
	}
	chunks := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return chunk.Err
		}
		chunks++
	}
	if chunks == 0 {
		return fmt.Errorf("stream produced no captured chunks")
	}
	return nil
}

// captureKeyPayloadFinal drives selection and captures the final business request locally.
func captureKeyPayloadFinal(t *testing.T, cfg *config.Config, fixture keyPayloadRequestFixture, auth *cliproxyauth.Auth, invoke keyPayloadInvocation) []byte {
	t.Helper()
	expectedURL, err := url.Parse(auth.Attributes["base_url"])
	if err != nil || expectedURL.Host == "" {
		t.Fatalf("invalid synthetic capture authority: %v", err)
	}
	baseModel := thinking.ParseSuffix(fixture.model).ModelName
	expectedPathsMap := map[string][]string{
		"claude":               {"/v1/messages", "/v1/messages/count_tokens"},
		"gemini":               {"/v1beta/models/" + baseModel + ":generateContent", "/v1beta/models/" + baseModel + ":streamGenerateContent", "/v1beta/models/" + baseModel + ":countTokens"},
		"interactions":         {"/v1beta/interactions", "/v1beta/models/" + baseModel + ":countTokens"},
		"vertex":               {"/v1/publishers/google/models/" + baseModel + ":generateContent", "/v1/publishers/google/models/" + baseModel + ":streamGenerateContent", "/v1/publishers/google/models/" + baseModel + ":countTokens", "/v1beta1/interactions"},
		"codex":                {"/responses", "/responses/compact"},
		"xai":                  {"/responses", "/responses/compact", "/images/generations", "/videos/generations", "/tts"},
		"meta":                 {"/responses"},
		"openai-compatibility": {"/chat/completions", "/responses/compact"},
	}
	var captured []byte
	requests := 0
	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != expectedURL.Scheme || req.URL.Host != expectedURL.Host || !slices.Contains(expectedPathsMap[fixture.family], req.URL.Path) {
			return nil, fmt.Errorf("unexpected synthetic endpoint %s%s; expected %s paths %v", req.URL.Host, req.URL.Path, fixture.family, expectedPathsMap[fixture.family])
		}
		var err error
		captured, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		requests++
		stream := ojson.GetBytes(captured, "stream").Bool() || req.URL.Query().Get("alt") == "sse" || req.Header.Get("Accept") == "text/event-stream"
		reply, contentType := fixture.reply, "application/json"
		if stream {
			reply, contentType = fixture.streamReply, "text/event-stream"
		}
		t.Logf("selected=%s requested=%s outgoing=%s digest=%x path=%s local=%t", auth.Label, fixture.model, ojson.GetBytes(captured, "model").String(), sha256.Sum256([]byte(ojson.GetBytes(captured, "messages").Raw+ojson.GetBytes(captured, "input").Raw)), req.URL.Path, !ojson.GetBytes(captured, "local_delete").Exists())
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(reply))}, nil
	})
	ctx, provider := keyPayloadTestContext(t, context.Background(), cfg, auth, transport)
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRoundTripperProvider(provider)
	manager.RegisterExecutor(fixture.executor)
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, fixture.executor.Identifier(), []*registry.ModelInfo{{ID: thinking.ParseSuffix(fixture.model).ModelName, Thinking: &registry.ThinkingSupport{Min: 1, Max: 8192, ZeroAllowed: true, DynamicAllowed: true}}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	opts := cliproxyexecutor.Options{SourceFormat: fixture.source, OriginalRequest: []byte(fixture.payload), Alt: fixture.alt, Headers: http.Header{"X-Filter": {"yes"}}}
	if err := invoke(ctx, manager, []string{fixture.executor.Identifier()}, cliproxyexecutor.Request{Model: fixture.model, Payload: []byte(fixture.payload)}, opts); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || len(captured) == 0 {
		t.Fatalf("capture completeness: got %d requests", requests)
	}
	return captured
}

func TestKeyPayloadProviderFinalRequests(t *testing.T) {
	cfg := keyPayloadRuntimeConfig(t)
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	const claudeRequest = `{"max_tokens":64,"system":[{"type":"text","text":"delete-marker"},{"type":"text","text":"keep-marker"}],"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"fixture","input_schema":{"type":"object"}}]}`
	const geminiRequest = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"systemInstruction":{"parts":[{"text":"keep-marker"}]}}`
	const responsesRequest = `{"instructions":"keep-marker","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[{"type":"function","name":"fixture","parameters":{"type":"object"}}]}`
	const chatRequest = `{"messages":[{"role":"system","content":"keep-marker"},{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"fixture","parameters":{"type":"object"}}}]}`
	const responsesReply = `{"id":"synthetic_response","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	responsesStream := "data: {\"type\":\"response.completed\",\"response\":" + responsesReply + "}\n\n"
	const geminiReply = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	geminiStream := "data: " + geminiReply + "\n\n"
	const interactionReply = `{"id":"synthetic_interaction","status":"completed","outputs":[{"type":"text","text":"ok"}],"usage":{"total_input_tokens":1,"total_output_tokens":1}}`
	interactionStream := "event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":" + interactionReply + "}\n\n"
	claudeReply := `{"id":"synthetic_message","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	claudeStream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic_message\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	chatReply := `{"id":"synthetic_chat","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	chatStream := "data: {\"id\":\"synthetic_chat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic_chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	fixtures := []keyPayloadRequestFixture{
		{family: "claude", destination: "claude", model: "claude-sonnet-4-5", source: sdktranslator.FormatClaude, payload: claudeRequest, reply: claudeReply, streamReply: claudeStream, executor: NewClaudeExecutor(cfg)},
		{family: "gemini", destination: "gemini", model: "gemini-2.5-pro", source: sdktranslator.FormatGemini, payload: geminiRequest, reply: geminiReply, streamReply: geminiStream, executor: NewGeminiExecutor(cfg)},
		{family: "interactions", destination: "interactions", model: "gemini-3.1-flash-lite", source: sdktranslator.FormatInteractions, payload: `{"model":"gemini-3.1-flash-lite","input":"hello"}`, reply: interactionReply, streamReply: interactionStream, executor: NewGeminiInteractionsExecutor(cfg)},
		{family: "vertex", destination: "gemini", model: "gemini-2.5-pro", source: sdktranslator.FormatGemini, payload: geminiRequest, reply: geminiReply, streamReply: geminiStream, executor: NewGeminiVertexExecutor(cfg)},
		{family: "codex", destination: "codex", model: "gpt-5.4", source: sdktranslator.FormatOpenAIResponse, payload: responsesRequest, reply: responsesStream, streamReply: responsesStream, executor: NewCodexExecutor(cfg)},
		{family: "xai", destination: "codex", model: "grok-4", source: sdktranslator.FormatOpenAIResponse, payload: responsesRequest, reply: responsesStream, streamReply: responsesStream, executor: NewXAIExecutor(cfg)},
		{family: "meta", destination: "meta", model: "muse-spark", source: sdktranslator.FormatOpenAIResponse, payload: responsesRequest, reply: responsesStream, streamReply: responsesStream, executor: NewMetaExecutor(cfg)},
		{family: "openai-compatibility", destination: "openai", model: "gpt-5.4", source: sdktranslator.FormatOpenAI, payload: chatRequest, reply: chatReply, streamReply: chatStream, executor: NewOpenAICompatExecutor(util.OpenAICompatibleProviderKey("custom-fixture"), cfg)},
		{family: "codex", model: "gpt-5.4", source: sdktranslator.FormatOpenAIResponse, payload: responsesRequest, reply: responsesReply, alt: "responses/compact", executor: NewCodexExecutor(cfg)},
		{family: "xai", model: "grok-4", source: sdktranslator.FormatOpenAIResponse, payload: responsesRequest, reply: responsesReply, alt: "responses/compact", executor: NewXAIExecutor(cfg)},
		{family: "openai-compatibility", model: "gpt-5.4", source: sdktranslator.FormatOpenAIResponse, payload: responsesRequest, reply: responsesReply, alt: "responses/compact", executor: NewOpenAICompatExecutor(util.OpenAICompatibleProviderKey("custom-fixture"), cfg)},
	}
	for _, fixture := range fixtures {
		if fixture.alt != "" {
			fixture.destination = "openai-response"
		}
		var selected []*cliproxyauth.Auth
		for _, auth := range auths {
			if auth.Provider == fixture.executor.Identifier() {
				selected = append(selected, auth)
			}
		}
		if len(selected) != 2 || selected[0].ConfigKey == selected[1].ConfigKey {
			t.Fatalf("%s: exact target/control ownership missing", fixture.family)
		}
		config.PayloadFiltersFor(selected[0].ConfigKey)[0].Models[0].Protocol = " " + strings.ToUpper(fixture.destination) + " "
		before := cfg.CloneForRuntime()
		for _, operation := range []struct {
			name   string
			invoke keyPayloadInvocation
		}{{name: "message", invoke: runKeyPayloadMessage}, {name: "stream", invoke: runKeyPayloadStream}} {
			if operation.name == "stream" && fixture.alt != "" {
				continue
			}
			roles := []string{"target", "other", "file"}
			if fixture.source == sdktranslator.FormatClaude || fixture.source == sdktranslator.FormatOpenAIResponse || fixture.source == sdktranslator.FormatOpenAI {
				roles = append(roles, "oauth")
			}
			for _, role := range roles {
				t.Run(fixture.family+"/"+fixture.alt+"/"+operation.name+"/"+role, func(t *testing.T) {
					auth := selected[0].Clone()
					if role == "other" {
						auth = selected[1].Clone()
					}
					auth.Label = role
					if role == "file" {
						auth.Attributes["source"] = "file:synthetic-fixture"
					}
					if role == "oauth" {
						delete(auth.Attributes, "api_key")
						auth.Attributes["auth_kind"] = cliproxyauth.AuthKindOAuth
						auth.Metadata = map[string]any{"access_token": "syntheticOAuthToken123", "cloak_mode": "uncloak"}
					}
					baselineAuth := auth.Clone()
					baselineAuth.ConfigKey = nil
					baseline := captureKeyPayloadFinal(t, cfg, fixture, baselineAuth, operation.invoke)
					actual := captureKeyPayloadFinal(t, cfg, fixture, auth, operation.invoke)
					var expected, received map[string]any
					if err := json.Unmarshal(baseline, &expected); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(actual, &received); err != nil {
						t.Fatal(err)
					}
					if expected["local_delete"] != "delete-marker" || expected["global_default"] != true || expected["global_default_raw"] != true || expected["global_keep"] != true || expected["global_override_raw"] != true || ojson.GetBytes(baseline, "global_delete").Exists() {
						t.Fatal("baseline did not exercise all global rule sections")
					}
					if role == "target" {
						delete(expected, "local_delete")
					}
					if !reflect.DeepEqual(expected, received) {
						t.Fatalf("%s final body diverged outside the declared local deletion", role)
					}
				})
			}
		}
		if !reflect.DeepEqual(cfg, before) {
			t.Fatal("request matrix changed authoritative configuration")
		}
	}
}

// TestKeyPayloadCodexToolSearchPool captures the issue 5266 rule on two gateways serving one alias.
func TestKeyPayloadCodexToolSearchPool(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte(`config-version: 8
api-keys:
  codex:
    - base-url: https://strict.invalid
      keys:
        - api-key: syntheticStrictKey123
          payload:
            filter:
              - models:
                  - name: "gpt-5.6-*-codex"
                    protocol: "codex"
                params:
                  - "tools.#(type==\"tool_search\")"
    - base-url: https://compatible.invalid
      keys:
        - api-key: syntheticCompatibleKey123
`))
	if err != nil {
		t.Fatal(err)
	}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil || len(auths) != 2 {
		t.Fatalf("expected two Codex credentials, got %d: %v", len(auths), err)
	}

	// Fail the first attempt so both gateways receive the same client request regardless of selection order.
	const model = "gpt-5.6-terra-codex"
	captured := make(map[string][]byte)
	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		host := req.URL.Host
		if (host != "strict.invalid" && host != "compatible.invalid") || req.URL.Path != "/responses" || captured[host] != nil {
			return nil, fmt.Errorf("unexpected capture at %s", req.URL.Redacted())
		}
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		captured[host] = body
		if len(captured) == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"try next gateway"}}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + `{"type":"response.completed","response":{"id":"fixture","object":"response","status":"completed","output":[]}}` + "\n\n"))}, nil
	})
	ctx, provider := keyPayloadTestContext(t, context.Background(), cfg, nil, transport)
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRoundTripperProvider(provider)
	manager.SetRetryConfig(0, 0, len(auths))
	manager.RegisterExecutor(NewCodexExecutor(cfg))
	for _, auth := range auths {
		if _, err := manager.Register(ctx, auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	}
	const payload = `{"input":[{"role":"user","content":"hello"}],"tools":[{"type":"tool_search","description":"Find tools","parameters":{"type":"object","properties":{}}},{"type":"function","name":"lookup","defer_loading":true,"parameters":{"type":"object","properties":{}}}]}`
	if err := runKeyPayloadMessage(ctx, manager, []string{"codex"}, cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex}); err != nil {
		t.Fatal(err)
	}

	// Compare complete tool declarations, without using the deletion query as the test oracle.
	strict, compatible := captured["strict.invalid"], captured["compatible.invalid"]
	if ojson.GetBytes(strict, "model").String() != model || ojson.GetBytes(compatible, "model").String() != model {
		t.Fatalf("expected both upstream requests to carry %s", model)
	}
	var retained []ojson.Result
	searchCount := 0
	for _, tool := range ojson.GetBytes(compatible, "tools").Array() {
		if tool.Get("type").String() == "tool_search" {
			searchCount++
			if tool.Get("description").String() != "Find tools" || !tool.Get("parameters").IsObject() {
				t.Fatal("compatible gateway lost the tool_search declaration")
			}
		} else {
			retained = append(retained, tool)
		}
	}
	strictTools := ojson.GetBytes(strict, "tools").Array()
	if searchCount != 1 || !ojson.GetBytes(compatible, `tools.#(name=="lookup").defer_loading`).Bool() || !slices.EqualFunc(strictTools, retained, func(a, b ojson.Result) bool { return a.Raw == b.Raw }) {
		t.Fatalf("expected only tool_search to differ: strict=%s compatible=%s", ojson.GetBytes(strict, "tools").Raw, ojson.GetBytes(compatible, "tools").Raw)
	}
}

// runKeyPayloadCount adapts the manager count contract to the capture harness.
func runKeyPayloadCount(ctx context.Context, manager *cliproxyauth.Manager, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	_, err := manager.ExecuteCount(ctx, providers, req, opts)
	return err
}

func TestKeyPayloadCounts(t *testing.T) {
	cfg := keyPayloadRuntimeConfig(t)
	system := []any{
		map[string]any{"type": "text", "text": "global first block"},
		map[string]any{"type": "text", "text": strings.Repeat("discarded alpha sentence ", 40)},
		map[string]any{"type": "text", "text": strings.Repeat("discarded beta sentence ", 60)},
		map[string]any{"type": "text", "text": strings.Repeat("retained system instructions ", 20)},
	}
	cfg.Payload.Override = append(cfg.Payload.Override, config.PayloadRule{Models: []config.PayloadModelRule{{Name: "claude-*"}}, Params: map[string]any{"system": system}})
	cfg.Payload.Filter[0].Params = []string{"global_delete", "system.0"}
	local := config.PayloadFilterRule{Models: []config.PayloadModelRule{{Name: "claude-*"}}, Params: []string{"system.0"}}
	cfg.ClaudeKey[0].Payload.Filter = []config.PayloadFilterRule{local, local}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"system":[{"type":"text","text":"original caller instructions"}],"messages":[{"role":"user","content":"hello from the retained user message"}]}`
	var original map[string]any
	if err = json.Unmarshal([]byte(payload), &original); err != nil {
		t.Fatal(err)
	}
	originalCount, err := helps.CountClaudeInputTokens([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range auths {
		if auth.Provider != "claude" {
			continue
		}
		t.Run("local/"+auth.Attributes["config_index"], func(t *testing.T) {
			expected := map[string]any{"messages": original["messages"], "system": system[1:]}
			if len(config.PayloadFiltersFor(auth.ConfigKey)) != 0 {
				expected["system"] = system[3:]
			}
			prepared, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			want, err := helps.CountClaudeInputTokens(prepared)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return nil, fmt.Errorf("local estimator attempted upstream %s", req.URL.Redacted())
			})
			ctx, _ := keyPayloadTestContext(t, context.Background(), cfg, auth, transport)
			executor := NewClaudeExecutor(cfg).ForAPIKey(auth.ConfigKey)
			response, err := executor.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if err != nil {
				t.Fatal(err)
			}
			got := ojson.GetBytes(response.Payload, "input_tokens").Int()
			if got != want || calls != 0 || want == originalCount {
				t.Fatalf("independent local count: got %d expected %d original %d upstream %d", got, want, originalCount, calls)
			}
			t.Logf("local-count selected-index=%s actual=%d independent=%d original=%d upstream=%d", auth.Attributes["config_index"], got, want, originalCount, calls)
		})
	}
	native := cfg.CloneForRuntime()
	for index := range native.ClaudeKey {
		native.ClaudeKey[index].BaseURL = "https://api.anthropic.com"
	}
	nativeAuths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: native, IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	fixture := keyPayloadRequestFixture{family: "claude", model: "claude-sonnet-4-5", source: sdktranslator.FormatClaude, payload: payload, reply: `{"input_tokens":7}`, executor: NewClaudeExecutor(native)}
	for _, auth := range nativeAuths {
		if auth.Provider != "claude" {
			continue
		}
		t.Run("native/"+auth.Attributes["config_index"], func(t *testing.T) {
			baselineAuth := auth.Clone()
			baselineAuth.ConfigKey = nil
			baseline := captureKeyPayloadFinal(t, native, fixture, baselineAuth, runKeyPayloadCount)
			actual := captureKeyPayloadFinal(t, native, fixture, auth, runKeyPayloadCount)
			var expected, received map[string]any
			if err := json.Unmarshal(baseline, &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(actual, &received); err != nil {
				t.Fatal(err)
			}
			if blocks := expected["system"].([]any); len(blocks) != len(system)-1 {
				t.Fatal("native count lost global policies")
			}
			if len(config.PayloadFiltersFor(auth.ConfigKey)) != 0 {
				expected["system"] = expected["system"].([]any)[2:]
			}
			if !reflect.DeepEqual(expected, received) {
				t.Fatal("native count changed outside sequential selected-key deletion")
			}
		})
	}
}

func TestKeyPayloadConcurrentIsolation(t *testing.T) {
	cfg := keyPayloadRuntimeConfig(t)
	cfg.Payload.Filter = append(make([]config.PayloadFilterRule, 0, 4), cfg.Payload.Filter...)
	cfg.Payload.Filter[:cap(cfg.Payload.Filter)][1] = config.PayloadFilterRule{Params: []string{"spare-sentinel"}}
	cfg.ClaudeKey[0].Payload.Filter[0].Models[0].Headers = map[string]string{"X-Filter": "yes"}
	before := cfg.CloneForRuntime()
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	var selected []*cliproxyauth.Auth
	for _, auth := range auths {
		if auth.Provider == "claude" {
			selected = append(selected, auth)
		}
	}
	if len(selected) != 2 {
		t.Fatal("target/control synthesis incomplete")
	}
	selected[0].Attributes["header:X-Role"] = "target"
	selected[1].Attributes["header:X-Role"] = "other"
	started, release := make(chan struct{}), make(chan struct{})
	bodies := make(chan []byte, 2)
	errors := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "synthetic.invalid" || req.URL.Path != "/v1/messages" {
			return nil, fmt.Errorf("unexpected destination %s", req.URL.Redacted())
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if req.Header.Get("X-Role") == "target" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		bodies <- body
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
	})
	ctx, _ = keyPayloadTestContext(t, ctx, cfg, selected[0], transport)
	registered := NewClaudeExecutor(cfg)
	req := cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: []byte(`{"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"X-Filter": {"yes"}}}
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		_, err := registered.ForAPIKey(selected[0].ConfigKey).Execute(ctx, selected[0], req, opts)
		errors <- err
	}()
	select {
	case <-started:
	case err := <-errors:
		t.Fatalf("target did not reach its interleave gate: %v", err)
	}
	if _, err = registered.ForAPIKey(selected[1].ConfigKey).Execute(ctx, selected[1], req, opts); err != nil {
		t.Fatal(err)
	}
	control := <-bodies
	close(release)
	group.Wait()
	if err = <-errors; err != nil {
		t.Fatal(err)
	}
	target := <-bodies
	if !ojson.GetBytes(control, "local_delete").Exists() || ojson.GetBytes(target, "local_delete").Exists() || !reflect.DeepEqual(cfg, before) || cfg.Payload.Filter[:cap(cfg.Payload.Filter)][1].Params[0] != "spare-sentinel" {
		t.Fatal("deterministic interleaving changed policy scope or shared configuration")
	}
}

func TestKeyPayloadNativeCountsAndMedia(t *testing.T) {
	cfg := keyPayloadRuntimeConfig(t)
	for index := range cfg.VertexCompatAPIKey {
		enabled := true
		cfg.VertexCompatAPIKey[index].Interactions = &enabled
	}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	gemini := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	for _, test := range []struct {
		name    string
		fixture keyPayloadRequestFixture
		invoke  keyPayloadInvocation
	}{
		{name: "gemini-count", fixture: keyPayloadRequestFixture{family: "gemini", model: "gemini-2.5-pro", source: sdktranslator.FormatGemini, payload: gemini, reply: `{"totalTokens":7}`, executor: NewGeminiExecutor(cfg)}, invoke: runKeyPayloadCount},
		{name: "interactions-count", fixture: keyPayloadRequestFixture{family: "interactions", model: "gemini-2.5-pro", source: sdktranslator.FormatGemini, payload: gemini, reply: `{"totalTokens":7}`, executor: NewGeminiInteractionsExecutor(cfg)}, invoke: runKeyPayloadCount},
		{name: "vertex-count", fixture: keyPayloadRequestFixture{family: "vertex", model: "gemini-2.5-pro", source: sdktranslator.FormatGemini, payload: gemini, reply: `{"totalTokens":7}`, executor: NewGeminiVertexExecutor(cfg)}, invoke: runKeyPayloadCount},
		{name: "vertex-interactions", fixture: keyPayloadRequestFixture{family: "vertex", model: "gemini-3.1-flash-lite", source: sdktranslator.FormatInteractions, payload: `{"model":"gemini-3.1-flash-lite","input":"hello"}`, reply: `{"id":"synthetic_interaction","status":"completed","outputs":[{"type":"text","text":"ok"}]}`, executor: NewGeminiVertexExecutor(cfg)}, invoke: runKeyPayloadMessage},
		{name: "xai-image", fixture: keyPayloadRequestFixture{family: "xai", model: "grok-imagine-image", source: sdktranslator.FromString(xaiImageHandlerType), payload: `{"prompt":"keep-marker","n":1}`, reply: `{"data":[{"b64_json":"synthetic"}]}`, executor: NewXAIExecutor(cfg)}, invoke: runKeyPayloadMessage},
		{name: "xai-video", fixture: keyPayloadRequestFixture{family: "xai", model: "grok-imagine-video", source: sdktranslator.FromString(xaiVideoHandlerType), payload: `{"prompt":"keep-marker"}`, reply: `{"request_id":"synthetic_video"}`, executor: NewXAIExecutor(cfg)}, invoke: runKeyPayloadMessage},
		{name: "xai-speech", fixture: keyPayloadRequestFixture{family: "xai", model: "grok-tts", source: sdktranslator.FromString(xaiSpeechHandlerType), payload: `{"input":"keep-marker","voice_id":"synthetic_voice"}`, reply: "synthetic-audio", executor: NewXAIExecutor(cfg)}, invoke: runKeyPayloadMessage},
	} {
		t.Run(test.name, func(t *testing.T) {
			var auth *cliproxyauth.Auth
			for _, candidate := range auths {
				if candidate.Provider == test.fixture.executor.Identifier() && len(config.PayloadFiltersFor(candidate.ConfigKey)) != 0 {
					auth = candidate
				}
			}
			if auth == nil {
				t.Fatal("selected policy owner missing")
			}
			baselineAuth := auth.Clone()
			baselineAuth.ConfigKey = nil
			baseline := captureKeyPayloadFinal(t, cfg, test.fixture, baselineAuth, test.invoke)
			actual := captureKeyPayloadFinal(t, cfg, test.fixture, auth, test.invoke)
			var expected, received map[string]any
			if err := json.Unmarshal(baseline, &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(actual, &received); err != nil {
				t.Fatal(err)
			}
			if expected["local_delete"] != "delete-marker" {
				t.Fatal("baseline did not apply global policy")
			}
			delete(expected, "local_delete")
			if !reflect.DeepEqual(expected, received) {
				t.Fatal("specialized path changed outside its local deletion")
			}
		})
	}
}

func TestKeyPayloadWebsocketRequests(t *testing.T) {
	for _, family := range []string{"codex", "xai"} {
		for index := range 2 {
			t.Run(fmt.Sprintf("%s/%d", family, index), func(t *testing.T) {
				frames := make(chan cliproxyexecutor.StreamChunk, 1)
				upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if req.URL.Path != "/responses" {
						http.Error(w, "unexpected synthetic WebSocket path", http.StatusBadRequest)
						t.Errorf("unexpected synthetic WebSocket path %s", req.URL.Path)
						return
					}
					conn, err := upgrader.Upgrade(w, req, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() {
						if errClose := conn.Close(); errClose != nil {
							t.Error(errClose)
						}
					}()
					_, body, errRead := conn.ReadMessage()
					frames <- cliproxyexecutor.StreamChunk{Payload: body, Err: errRead}
					if errRead != nil {
						return
					}
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"synthetic_ws","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)); errWrite != nil {
						t.Error(errWrite)
					}
				}))
				defer server.Close()
				cfg := keyPayloadRuntimeConfig(t)
				for keyIndex := range cfg.CodexKey {
					cfg.CodexKey[keyIndex].BaseURL = server.URL
				}
				for keyIndex := range cfg.XAIKey {
					cfg.XAIKey[keyIndex].BaseURL = server.URL
				}
				auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
				if err != nil {
					t.Fatal(err)
				}
				var selected *cliproxyauth.Auth
				for _, auth := range auths {
					if auth.Provider == family && auth.Attributes["config_index"] == fmt.Sprint(index) {
						selected = auth
					}
				}
				if selected == nil {
					t.Fatal("WebSocket selected owner missing")
				}
				selected.ProxyURL = "direct"
				var registered cliproxyauth.ProviderExecutor = NewCodexWebsocketsExecutor(cfg)
				if family == "xai" {
					registered = NewXAIWebsocketsExecutor(cfg)
				}
				bound := registered.(cliproxyauth.APIKeyConfigExecutor).ForAPIKey(selected.ConfigKey)
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"input":[{"role":"user","content":"keep-marker"}],"instructions":"retained"}`)}
				result, err := bound.ExecuteStream(context.Background(), selected, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex})
				if err != nil {
					t.Fatal(err)
				}
				chunks := 0
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					chunks++
				}
				var frame cliproxyexecutor.StreamChunk
				select {
				case frame = <-frames:
				default:
					t.Fatal("WebSocket stream completed without a captured frame")
				}
				if frame.Err != nil || chunks == 0 {
					t.Fatalf("WebSocket stream incomplete: %d chunks, %v", chunks, frame.Err)
				}
				expectedLocal := len(config.PayloadFiltersFor(selected.ConfigKey)) != 0
				if ojson.GetBytes(frame.Payload, "type").String() != "response.create" || ojson.GetBytes(frame.Payload, "local_delete").Exists() == expectedLocal || !ojson.GetBytes(frame.Payload, "global_keep").Bool() || !strings.Contains(ojson.GetBytes(frame.Payload, "input").Raw, "keep-marker") {
					t.Fatal("WebSocket framing lost selected policy or retained business content")
				}
			})
		}
	}
}

func TestKeyPayloadMatchersAndSequentialDeletion(t *testing.T) {
	for _, test := range []keyPayloadMatcherCase{
		{name: "exact", rule: config.PayloadModelRule{Name: "claude-sonnet-4-5"}, systemDeletes: 2},
		{name: "glob", rule: config.PayloadModelRule{Name: "claude-*"}, systemDeletes: 2},
		{name: "nonmatching", rule: config.PayloadModelRule{Name: "gemini-*"}},
		{name: "source", rule: config.PayloadModelRule{Name: "*", FromProtocol: "claude"}, systemDeletes: 2},
		{name: "other-source", rule: config.PayloadModelRule{Name: "*", FromProtocol: "openai"}},
		{name: "headers", rule: config.PayloadModelRule{Name: "*", Headers: map[string]string{"X-Filter": "ye*"}}, systemDeletes: 2},
		{name: "other-headers", rule: config.PayloadModelRule{Name: "*", Headers: map[string]string{"X-Filter": "no"}}},
		{name: "conditions", rule: config.PayloadModelRule{Name: "*", Match: []map[string]any{{"messages.0.role": "user"}}, NotMatch: []map[string]any{{"messages.0.role": "assistant"}}, Exist: []string{"system"}, NotExist: []string{"absent"}}, systemDeletes: 2},
		{name: "missing-condition", rule: config.PayloadModelRule{Name: "*", Exist: []string{"absent"}}},
		{name: "query", rule: config.PayloadModelRule{Name: "*"}, params: []string{`system.#(text%"delete-*")#`}, systemDeletes: 2},
		{name: "literal", rule: config.PayloadModelRule{Name: "claude-*"}, params: []string{"literal.$1"}, expectedPatch: map[string]any{"literal": map[string]any{"other": "keep-marker"}}},
		{name: "future-fixture", model: "claude-future-fixture", rule: config.PayloadModelRule{Name: "claude-*"}, systemDeletes: 2},
		{name: "disabled-suffix", model: "claude-sonnet-4-5(none)", rule: config.PayloadModelRule{Name: "claude-*"}, systemDeletes: 2},
		{name: "numeric-suffix", model: "claude-sonnet-4-5(0)", rule: config.PayloadModelRule{Name: "claude-*"}, systemDeletes: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := keyPayloadRuntimeConfig(t)
			params := test.params
			if params == nil {
				params = []string{"system.0"}
			}
			rule := config.PayloadFilterRule{Models: []config.PayloadModelRule{test.rule}, Params: params}
			cfg.ClaudeKey[0].Payload.Filter = []config.PayloadFilterRule{rule, rule}
			cfg.Payload.Override = append(cfg.Payload.Override, config.PayloadRule{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"literal": map[string]any{"$1": "delete-marker", "other": "keep-marker"}}})
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil {
				t.Fatal(err)
			}
			var auth *cliproxyauth.Auth
			for _, candidate := range auths {
				if candidate.Provider == "claude" && candidate.Attributes["config_index"] == "0" {
					auth = candidate
				}
			}
			if auth == nil {
				t.Fatal("selected target missing")
			}
			model := test.model
			if model == "" {
				model = "claude-sonnet-4-5"
			}
			fixture := keyPayloadRequestFixture{family: "claude", model: model, source: sdktranslator.FormatClaude, payload: `{"max_tokens":64,"system":[{"type":"text","text":"delete-one"},{"type":"text","text":"delete-two"},{"type":"text","text":"keep-marker"}],"messages":[{"role":"user","content":"retained message"}],"tools":[{"name":"fixture","input_schema":{"type":"object"}}]}`, reply: `{"id":"fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, executor: NewClaudeExecutor(cfg)}
			baselineAuth := auth.Clone()
			baselineAuth.ConfigKey = nil
			baseline := captureKeyPayloadFinal(t, cfg, fixture, baselineAuth, runKeyPayloadMessage)
			actual := captureKeyPayloadFinal(t, cfg, fixture, auth, runKeyPayloadMessage)
			var expected, received map[string]any
			if err := json.Unmarshal(baseline, &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(actual, &received); err != nil {
				t.Fatal(err)
			}
			if test.systemDeletes != 0 {
				expected["system"] = expected["system"].([]any)[test.systemDeletes:]
			}
			for field, value := range test.expectedPatch {
				expected[field] = value
			}
			if !reflect.DeepEqual(expected, received) {
				t.Fatal("matcher or sequential deletion changed an unrelated final field")
			}
		})
	}
	for _, system := range []string{"", `,"system":null`, `,"system":[]`, `,"system":"keep-marker"`} {
		t.Run("system-shape/"+system, func(t *testing.T) {
			cfg := keyPayloadRuntimeConfig(t)
			cfg.ClaudeKey[0].Payload.Filter[0].Params = []string{"system.0"}
			auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil {
				t.Fatal(err)
			}
			var auth *cliproxyauth.Auth
			for _, candidate := range auths {
				if candidate.Provider == "claude" && candidate.Attributes["config_index"] == "0" {
					auth = candidate
				}
			}
			fixture := keyPayloadRequestFixture{family: "claude", model: "claude-sonnet-4-5", source: sdktranslator.FormatClaude, payload: `{"max_tokens":64,"messages":[{"role":"user","content":"hello"}]` + system + `}`, reply: `{"id":"fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, executor: NewClaudeExecutor(cfg)}
			baselineAuth := auth.Clone()
			baselineAuth.ConfigKey = nil
			baseline := captureKeyPayloadFinal(t, cfg, fixture, baselineAuth, runKeyPayloadMessage)
			actual := captureKeyPayloadFinal(t, cfg, fixture, auth, runKeyPayloadMessage)
			var expected, received map[string]any
			if err := json.Unmarshal(baseline, &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(actual, &received); err != nil {
				t.Fatal(err)
			}
			if blocks, ok := expected["system"].([]any); ok && len(blocks) != 0 {
				expected["system"] = blocks[1:]
			}
			if !reflect.DeepEqual(expected, received) {
				t.Fatal("system-shape deletion differs from its prepared baseline")
			}
		})
	}
}

func TestKeyPayloadCredentialFallback(t *testing.T) {
	for _, scenario := range []keyPayloadAttemptFixture{
		{roles: []string{"target", "other"}},
		{roles: []string{"target", "oauth"}},
		{retry: 1, roles: []string{"target"}},
		{retry: 1, roles: []string{"other"}},
		{retry: 1, roles: []string{"oauth"}},
	} {
		for name, invoke := range map[string]keyPayloadInvocation{"message": runKeyPayloadMessage, "stream": runKeyPayloadStream, "count": runKeyPayloadCount} {
			t.Run(fmt.Sprintf("%s/%s/retry=%d", name, strings.Join(scenario.roles, "-"), scenario.retry), func(t *testing.T) {
				// Prepare distinguishable deletions so repeated binding cannot hide behind idempotence.
				cfg := keyPayloadRuntimeConfig(t)
				cfg.DisableCooling = true
				cfg.ClaudeKey[0].Priority = 100
				cfg.Payload.Override = append(cfg.Payload.Override, config.PayloadRule{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"system": []any{map[string]any{"type": "text", "text": "delete-first"}, map[string]any{"type": "text", "text": "keep-second"}, map[string]any{"type": "text", "text": "keep-third"}}}})
				cfg.ClaudeKey[0].Payload.Filter = append(cfg.ClaudeKey[0].Payload.Filter, config.PayloadFilterRule{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"system.0"}})
				for index := range cfg.ClaudeKey {
					cfg.ClaudeKey[index].BaseURL = "https://api.anthropic.com"
				}
				auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
				if err != nil {
					t.Fatal(err)
				}
				selected := make(map[string]*cliproxyauth.Auth)
				for _, auth := range auths {
					if auth.Provider != "claude" {
						continue
					}
					role := "other"
					if auth.Attributes["config_index"] == "0" {
						role = "target"
					}
					auth.Attributes["header:X-Role"] = role
					selected[role] = auth
				}
				oauth := selected["target"].Clone()
				oauth.ID = "synthetic-oauth-control"
				delete(oauth.Attributes, "api_key")
				oauth.Attributes["auth_kind"] = cliproxyauth.AuthKindOAuth
				oauth.Attributes["priority"] = "0"
				oauth.Attributes["header:X-Role"] = "oauth"
				oauth.Metadata = map[string]any{"access_token": "sk-ant-oat-syntheticToken123", "account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "cloak_mode": "uncloak"}
				selected["oauth"] = oauth

				// Capture every selected attempt before returning a retryable synthetic failure.
				expectedPath := "/v1/messages"
				if name == "count" {
					expectedPath = "/v1/messages/count_tokens"
				}
				var attempts [][]byte
				transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Host != "api.anthropic.com" || len(attempts) >= 2 || req.URL.Path != expectedPath {
						return nil, fmt.Errorf("unexpected attempt destination %s after %d captures", req.URL.Redacted(), len(attempts))
					}
					role := scenario.roles[min(len(attempts), len(scenario.roles)-1)]
					if actual := req.Header.Get("X-Role"); actual != role {
						return nil, fmt.Errorf("attempt %d selected %s; expected %s", len(attempts)+1, actual, role)
					}
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					attempts = append(attempts, body)
					t.Logf("attempt=%d selected=%s requested=claude-sonnet-4-5 outgoing=%s system=%d messages=%x path=%s", len(attempts), role, ojson.GetBytes(body, "model").String(), ojson.GetBytes(body, "system.#").Int(), sha256.Sum256([]byte(ojson.GetBytes(body, "messages").Raw)), req.URL.Path)
					status, reply, contentType := http.StatusOK, `{"id":"fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`, "application/json"
					if len(attempts) == 1 {
						status, reply = http.StatusServiceUnavailable, `{"error":{"type":"overloaded_error","message":"synthetic selected attempt"}}`
					} else if req.URL.Path == "/v1/messages/count_tokens" {
						reply = `{"input_tokens":7}`
					} else if name == "stream" {
						reply, contentType = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"fixture\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "text/event-stream"
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(reply))}, nil
				})
				ctx, provider := keyPayloadTestContext(t, context.Background(), cfg, nil, transport)
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(scenario.retry, 0, len(scenario.roles))
				manager.SetRoundTripperProvider(provider)
				manager.RegisterExecutor(NewClaudeExecutor(cfg))
				for _, role := range scenario.roles {
					auth := selected[role]
					if _, err := manager.Register(ctx, auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-sonnet-4-5"}})
					defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
				}
				if err := invoke(ctx, manager, []string{"claude"}, cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: []byte(`{"max_tokens":64,"messages":[{"role":"user","content":"keep-marker"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
					t.Fatal(err)
				}

				// Verify fresh preparation and one selected policy on both attempts.
				if len(attempts) != 2 {
					t.Fatalf("attempt capture incomplete: %d requests", len(attempts))
				}
				for index, body := range attempts {
					role := scenario.roles[min(index, len(scenario.roles)-1)]
					blocks := int64(3)
					if role == "target" {
						blocks--
					}
					if ojson.GetBytes(body, "local_delete").Exists() == (role == "target") || ojson.GetBytes(body, "system.#").Int() != blocks || !ojson.GetBytes(body, "global_keep").Bool() || !strings.Contains(ojson.GetBytes(body, "messages").Raw, "keep-marker") {
						t.Fatal("retry or fallback reused a filtered body or another key's policy")
					}
				}
			})
		}
	}
}

func TestKeyPayloadOtherLocalCounters(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	cfg := keyPayloadRuntimeConfig(t)
	items := make([]any, 0, 3)
	for _, text := range []string{"global first message", strings.Repeat("discarded sentence ", 80), "keep-marker"} {
		items = append(items, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}})
	}
	instructions := strings.Repeat("global retained instructions ", 30)
	cfg.Payload.Override = append(cfg.Payload.Override, config.PayloadRule{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"input": items, "messages": items, "instructions": instructions}})
	cfg.Payload.Filter = append(cfg.Payload.Filter, config.PayloadFilterRule{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"input.0", "messages.0"}})
	cfg.CodexKey[0].Payload.Filter[0].Params = []string{"input.0"}
	cfg.XAIKey[0].Payload.Filter[0].Params = []string{"input.0"}
	cfg.MetaKey[0].Payload.Filter[0].Params = []string{"input.0"}
	cfg.OpenAICompatibility[0].APIKeyEntries[0].Payload.Filter[0].Params = []string{"messages.0"}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []keyPayloadRequestFixture{
		{family: "codex", model: "gpt-5.4", source: sdktranslator.FormatCodex, collection: "input", counter: countCodexInputTokens, executor: NewCodexExecutor(cfg)},
		{family: "xai", model: "grok-4", source: sdktranslator.FormatCodex, collection: "input", counter: countXAIInputTokens, executor: NewXAIExecutor(cfg)},
		{family: "meta", model: "muse-spark", source: sdktranslator.FormatCodex, collection: "input", counter: countCodexInputTokens, executor: NewMetaExecutor(cfg)},
		{family: "openai-compatibility", model: "gpt-5.4", source: sdktranslator.FormatOpenAI, collection: "messages", counter: helps.CountOpenAIChatTokens, executor: NewOpenAICompatExecutor(util.OpenAICompatibleProviderKey("custom-fixture"), cfg)},
	} {
		t.Run(fixture.family, func(t *testing.T) {
			globalBody, err := json.Marshal(map[string]any{"input": items[1:], "messages": items[1:], "instructions": instructions})
			if err != nil {
				t.Fatal(err)
			}
			globalCount, err := fixture.counter(enc, globalBody)
			if err != nil {
				t.Fatal(err)
			}
			for _, auth := range auths {
				if auth.Provider != fixture.executor.Identifier() {
					continue
				}
				expected := map[string]any{"input": items[1:], "messages": items[1:], "instructions": instructions}
				if len(config.PayloadFiltersFor(auth.ConfigKey)) != 0 {
					expected[fixture.collection] = items[2:]
				}
				body, err := json.Marshal(expected)
				if err != nil {
					t.Fatal(err)
				}
				want, err := fixture.counter(enc, body)
				if err != nil {
					t.Fatal(err)
				}
				payload, err := json.Marshal(map[string]any{fixture.collection: []any{map[string]any{"role": "user", "content": "original caller message"}}, "instructions": "original caller instructions"})
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				transport := claudeFingerprintRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					return nil, fmt.Errorf("local counter attempted upstream %s", req.URL.Redacted())
				})
				ctx, _ := keyPayloadTestContext(t, context.Background(), cfg, auth, transport)
				result, err := fixture.executor.(cliproxyauth.APIKeyConfigExecutor).ForAPIKey(auth.ConfigKey).CountTokens(ctx, auth, cliproxyexecutor.Request{Model: fixture.model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: fixture.source, ResponseFormat: sdktranslator.FormatClaude})
				if err != nil {
					t.Fatal(err)
				}
				got := ojson.GetBytes(result.Payload, "input_tokens").Int()
				if got != want || calls != 0 || (len(config.PayloadFiltersFor(auth.ConfigKey)) != 0 && want == globalCount) {
					t.Fatalf("local counter got %d expected %d global-only %d upstream %d", got, want, globalCount, calls)
				}
				t.Logf("local-count provider=%s selected-index=%s local-rules=%d actual=%d independent=%d global-only=%d upstream=omitted", fixture.family, auth.Attributes["config_index"], len(config.PayloadFiltersFor(auth.ConfigKey)), got, want, globalCount)
			}
		})
	}
}

func TestV8SharedUpstreamRequestSettingsAffectBothAuthKinds(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		for _, kind := range []string{cliproxyauth.AuthKindAPIKey, cliproxyauth.AuthKindOAuth} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", provider, kind, stream), func(t *testing.T) {
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						captured <- body
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"message":"captured request"}}`))
					}))
					defer server.Close()
					cfg, err := config.ParseConfigBytes([]byte(`proxy-url: direct
disable-image-generation: true
client: {codex: {optimize-multi-agent-v2: true}}
upstream:
  codex: {orphan-delegation-compatibility: true}
  xai: {inject-x-search: true}
`))
					if err != nil {
						t.Fatal(err)
					}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Status: cliproxyauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"auth_kind": kind, "base_url": server.URL}}
					if kind == cliproxyauth.AuthKindAPIKey {
						auth.Attributes["api_key"] = "test-key"
					} else {
						auth.Metadata = map[string]any{"access_token": "test-token"}
					}
					var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if provider == "xai" {
						executor = NewXAIExecutor(cfg)
					}
					manager := cliproxyauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.RegisterExecutor(executor)
					if _, err = manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: "gpt-5.4"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{
  "input":[{"type":"function_call_output","name":"create_thread","namespace":"codex_app","output":"<codex_delegation>task</codex_delegation>"}],
  "tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}}}}]}]
}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Headers: http.Header{"User-Agent": {"codex_cli_rs/0.144.1"}, "X-Openai-Subagent": {"collab_spawn"}}}
					if stream {
						_, err = manager.ExecuteStream(context.Background(), []string{provider}, req, opts)
					} else {
						_, err = manager.Execute(context.Background(), []string{provider}, req, opts)
					}
					if err == nil {
						t.Fatal("expected the test upstream's rejection")
					}
					var body []byte
					select {
					case body = <-captured:
					default:
						t.Fatalf("request did not reach upstream: %v", err)
					}
					if got := ojson.GetBytes(body, "input.0.type").String(); got != "message" {
						t.Fatalf("shared orphan delegation type = %q; body=%s", got, body)
					}
					if provider == "codex" {
						if got := ojson.GetBytes(body, "tools.0.name").String(); got != "collaboration-optimize" {
							t.Fatalf("client collaboration namespace = %q, want collaboration-optimize", got)
						}
					} else if !ojson.GetBytes(body, `tools.#(type=="x_search")`).Exists() {
						t.Fatal("shared x_search setting was not applied")
					}
					if !cfg.Client.Codex.OptimizeMultiAgentV2 || !cfg.Codex.OrphanDelegationCompatibility || !cfg.XAI.InjectXSearch {
						t.Fatal("request changed shared configuration")
					}
				})
			}
		}
	}
}

func TestV8ScopedExecutorsPreserveWebsocketStores(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("upstream: {codex: {response-steering: true}, xai: {inject-x-search: true}}\noauth: {providers: {codex: {header-defaults: {user-agent: oauth-agent}}}}"))
	if err != nil {
		t.Fatal(err)
	}
	codex := NewCodexAutoExecutor(cfg)
	boundCodex := codex.ForAPIKey(nil).(*CodexAutoExecutor)
	if boundCodex.wsExec.store != codex.wsExec.store || !boundCodex.httpExec.cfg.Codex.ResponseSteering || !boundCodex.wsExec.cfg.Codex.ResponseSteering || boundCodex.httpExec.cfg.CodexHeaderDefaults.UserAgent != "" {
		t.Fatal("scoped Codex executor lost sessions or inherited OAuth settings")
	}
	xai := NewXAIAutoExecutor(cfg)
	boundXAI := xai.ForAPIKey(nil).(*XAIAutoExecutor)
	if boundXAI.wsExec.store != xai.wsExec.store || boundXAI.wsExec.idStore != xai.wsExec.idStore || !boundXAI.httpExec.cfg.XAI.InjectXSearch || !boundXAI.wsExec.cfg.XAI.InjectXSearch || boundXAI.httpExec.cfg.CodexHeaderDefaults.UserAgent != "" {
		t.Fatal("scoped xAI executor lost sessions or inherited OAuth settings")
	}
	if codex.httpExec.cfg != cfg || codex.wsExec.cfg != cfg || xai.httpExec.cfg != cfg || xai.wsExec.cfg != cfg || !cfg.Codex.ResponseSteering || !cfg.XAI.InjectXSearch {
		t.Fatal("scoping changed the registered executors")
	}
}
