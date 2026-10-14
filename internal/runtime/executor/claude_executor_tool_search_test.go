package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// nativeToolSearchResponsesPayload is a Codex Responses turn after one client
// tool_search discovered a namespaced MCP tool.
const nativeToolSearchResponsesPayload = `{"model":"claude-sonnet-4-5","input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"create a calendar event"}]},` +
	`{"type":"tool_search_call","call_id":"call_search_1","status":"completed","execution":"client","arguments":{"query":"calendar"}},` +
	`{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[{"type":"namespace","name":"mcp__codex_apps__","description":"Apps.","tools":[{"type":"function","name":"calendar_create_event","description":"Create a calendar event.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}}]}]}` +
	`],"tools":[` +
	`{"type":"function","name":"exec_command","description":"Run a command.","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}},` +
	`{"type":"tool_search","execution":"client","description":"Search deferred tools.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}` +
	`],"stream":false}`

const nativeToolSearchCalendar = "mcp__codex_apps__calendar_create_event"

const nativeToolSearchGateway = "https://gateway.example.com"

// enableClaudeToolSearchForTest sets upstream.claude.tool-search for one test.
func enableClaudeToolSearchForTest(t *testing.T, enabled bool) {
	t.Helper()
	previous := responsesclaude.ClientToolSearchEnabled()
	responsesclaude.SetClientToolSearchEnabled(enabled)
	t.Cleanup(func() { responsesclaude.SetClientToolSearchEnabled(previous) })
}

func nativeToolSearchAuth(attrs map[string]string) *cliproxyauth.Auth {
	out := map[string]string{"api_key": "key-123", "cloak_mode": "never"}
	for key, value := range attrs {
		out[key] = value
	}
	return &cliproxyauth.Auth{Attributes: out}
}

type nativeToolSearchSend func(t *testing.T, ex *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, model string, payload, original []byte) error

func sendNativeToolSearchExecute(t *testing.T, ex *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, model string, payload, original []byte) error {
	t.Helper()
	_, err := ex.Execute(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: original})
	return err
}

func sendNativeToolSearchStream(t *testing.T, ex *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, model string, payload, original []byte) error {
	t.Helper()
	result, err := ex.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: original})
	if err != nil {
		return err
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return chunk.Err
		}
	}
	return nil
}

func sendNativeToolSearchCountTokens(t *testing.T, ex *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, model string, payload, original []byte) error {
	t.Helper()
	_, err := ex.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: original})
	return err
}

// captureNativeToolSearchBody sends one request and returns the upstream body.
func captureNativeToolSearchBody(t *testing.T, cfg *config.Config, send nativeToolSearchSend, auth *cliproxyauth.Auth, model string, payload, original []byte) []byte {
	t.Helper()
	upstream := &midSystemUpstream{}
	if errSend := send(t, NewClaudeExecutor(cfg), upstream.context(t, nil), auth, model, payload, original); errSend != nil {
		t.Fatalf("send: %v", errSend)
	}
	if !upstream.called {
		t.Fatal("upstream was not called")
	}
	return upstream.body
}

func assertNativeToolSearchBody(t *testing.T, body []byte) {
	t.Helper()
	calendar := gjson.GetBytes(body, `tools.#(name=="`+nativeToolSearchCalendar+`")`)
	if !calendar.Get("defer_loading").Bool() {
		t.Fatalf("discovered tool is not deferred: %s", gjson.GetBytes(body, "tools").Raw)
	}
	if !bytes.Contains(body, []byte(`{"type":"tool_reference","tool_name":"`+nativeToolSearchCalendar+`"}`)) {
		t.Fatalf("tool_reference missing: %s", gjson.GetBytes(body, "messages").Raw)
	}
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("defer_loading").Bool() && tool.Get("cache_control").Exists() {
			t.Fatalf("deferred tool carries cache_control: %s", tool.Raw)
		}
		return true
	})
}

func assertDevToolSearchBody(t *testing.T, body []byte) {
	t.Helper()
	if gjson.GetBytes(body, `tools.#(name=="tool_search")`).Exists() || bytes.Contains(body, []byte(`"tool_reference"`)) || bytes.Contains(body, []byte(`"defer_loading"`)) {
		t.Fatalf("fallback body still uses tool search: %s", body)
	}
}

func TestClaudeExecutor_SupportsNativeToolSearch(t *testing.T) {
	enableClaudeToolSearchForTest(t, true)
	forcedCfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "k", BaseURL: nativeToolSearchGateway, NativeToolSearch: true}}}
	for _, tc := range []struct {
		name  string
		cfg   *config.Config
		attrs map[string]string
		model string
		want  bool
	}{
		{name: "default origin", attrs: map[string]string{"api_key": "k"}, model: "claude-sonnet-4-5", want: true},
		{name: "explicit anthropic", attrs: map[string]string{"api_key": "k", "base_url": "https://api.anthropic.com"}, model: "claude-sonnet-4-5", want: true},
		{name: "anthropic with port", attrs: map[string]string{"api_key": "k", "base_url": "https://api.anthropic.com:443/"}, model: "claude-sonnet-4-5", want: true},
		{name: "plain http anthropic", attrs: map[string]string{"api_key": "k", "base_url": "http://api.anthropic.com"}, model: "claude-sonnet-4-5", want: false},
		{name: "gateway", attrs: map[string]string{"api_key": "k", "base_url": nativeToolSearchGateway}, model: "claude-sonnet-4-5", want: false},
		{name: "gateway forced by attribute", attrs: map[string]string{"api_key": "k", "base_url": nativeToolSearchGateway, "native_tool_search": "TRUE"}, model: "claude-sonnet-4-5", want: true},
		{name: "gateway forced by config", cfg: forcedCfg, attrs: map[string]string{"api_key": "k", "base_url": nativeToolSearchGateway}, model: "claude-sonnet-4-5", want: true},
		{name: "legacy model on anthropic", attrs: map[string]string{"api_key": "k"}, model: "claude-opus-4-1-20250805", want: false},
		{name: "legacy model on forced gateway", attrs: map[string]string{"api_key": "k", "base_url": nativeToolSearchGateway, "native_tool_search": "true"}, model: "claude-3-7-sonnet-20250219", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			if cfg == nil {
				cfg = &config.Config{}
			}
			auth := &cliproxyauth.Auth{Attributes: tc.attrs}
			if got := NewClaudeExecutor(cfg).SupportsNativeToolSearch(auth, tc.model); got != tc.want {
				t.Fatalf("SupportsNativeToolSearch() = %v, want %v", got, tc.want)
			}
		})
	}
	if NewKimiExecutor(&config.Config{}).SupportsNativeToolSearch(&cliproxyauth.Auth{}, "kimi-k2") {
		t.Fatal("Kimi must never advertise native tool search")
	}
}

func TestClaudeExecutor_NativeToolSearchOfficialSendsReferences(t *testing.T) {
	enableClaudeToolSearchForTest(t, true)
	payload := []byte(nativeToolSearchResponsesPayload)
	for _, tc := range []struct {
		name string
		send nativeToolSearchSend
	}{
		{name: "execute", send: sendNativeToolSearchExecute},
		{name: "stream", send: sendNativeToolSearchStream},
		{name: "count tokens upstream", send: sendNativeToolSearchCountTokens},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := captureNativeToolSearchBody(t, &config.Config{}, tc.send, nativeToolSearchAuth(nil), "claude-sonnet-4-5", payload, nil)
			assertNativeToolSearchBody(t, body)
		})
	}
}

func TestClaudeExecutor_NativeToolSearchFallbackMatchesDev(t *testing.T) {
	enableClaudeToolSearchForTest(t, true)
	payload := []byte(nativeToolSearchResponsesPayload)
	stripped := helps.StripResponsesToolSearch(append([]byte(nil), payload...))
	gateway := nativeToolSearchAuth(map[string]string{"base_url": nativeToolSearchGateway})
	official := nativeToolSearchAuth(nil)
	for _, tc := range []struct {
		name     string
		send     nativeToolSearchSend
		auth     *cliproxyauth.Auth
		model    string
		original bool
	}{
		{name: "gateway execute", send: sendNativeToolSearchExecute, auth: gateway, model: "claude-sonnet-4-5"},
		{name: "gateway stream", send: sendNativeToolSearchStream, auth: gateway, model: "claude-sonnet-4-5"},
		{name: "gateway execute with original request", send: sendNativeToolSearchExecute, auth: gateway, model: "claude-sonnet-4-5", original: true},
		{name: "legacy model execute", send: sendNativeToolSearchExecute, auth: official, model: "claude-opus-4-1-20250805"},
		{name: "legacy model count tokens upstream", send: sendNativeToolSearchCountTokens, auth: official, model: "claude-opus-4-1-20250805"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var original, strippedOriginal []byte
			if tc.original {
				original, strippedOriginal = payload, stripped
			}
			got := captureNativeToolSearchBody(t, &config.Config{}, tc.send, tc.auth, tc.model, payload, original)
			want := captureNativeToolSearchBody(t, &config.Config{}, tc.send, tc.auth, tc.model, stripped, strippedOriginal)
			if !bytes.Equal(got, want) {
				t.Fatalf("fallback body differs from the pre-tool-search translation:\n got: %s\nwant: %s", got, want)
			}
			assertDevToolSearchBody(t, got)
		})
	}

	t.Run("gateway forced by attribute", func(t *testing.T) {
		forced := nativeToolSearchAuth(map[string]string{"base_url": nativeToolSearchGateway, "native_tool_search": "true"})
		body := captureNativeToolSearchBody(t, &config.Config{}, sendNativeToolSearchExecute, forced, "claude-sonnet-4-5", payload, nil)
		assertNativeToolSearchBody(t, body)
	})
}

func TestClaudeExecutor_NativeToolSearchCountTokensLocalFallback(t *testing.T) {
	enableClaudeToolSearchForTest(t, true)
	payload := []byte(nativeToolSearchResponsesPayload)
	stripped := helps.StripResponsesToolSearch(append([]byte(nil), payload...))
	auth := nativeToolSearchAuth(map[string]string{"base_url": nativeToolSearchGateway})
	count := func(body []byte) string {
		t.Helper()
		resp, err := NewClaudeExecutor(&config.Config{}).CountTokens(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: body},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
		if err != nil {
			t.Fatalf("CountTokens: %v", err)
		}
		return string(resp.Payload)
	}
	if got, want := count(payload), count(stripped); got != want {
		t.Fatalf("local count = %s, want the pre-tool-search count %s", got, want)
	}
}

func TestClaudeExecutor_NativeToolSearchPayloadRuleIsFinal(t *testing.T) {
	enableClaudeToolSearchForTest(t, true)
	cfg := &config.Config{}
	// tools: exec_command, tool_search, the constant deferred anchor, then the discovered calendar.
	cfg.Payload.Override = []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"tools.3.defer_loading": false},
	}}
	body := captureNativeToolSearchBody(t, cfg, sendNativeToolSearchExecute, nativeToolSearchAuth(nil), "claude-sonnet-4-5", []byte(nativeToolSearchResponsesPayload), nil)
	if got := gjson.GetBytes(body, "tools.3.name").String(); got != nativeToolSearchCalendar {
		t.Fatalf("tools.3.name = %q, want %s: %s", got, nativeToolSearchCalendar, gjson.GetBytes(body, "tools").Raw)
	}
	if deferred := gjson.GetBytes(body, "tools.3.defer_loading"); !deferred.Exists() || deferred.Bool() {
		t.Fatalf("payload rule did not win over native tool search: %s", gjson.GetBytes(body, "tools.3").Raw)
	}
}

func TestClaudeExecutor_NativeToolSearchOAuthAliasesReferences(t *testing.T) {
	enableClaudeToolSearchForTest(t, true)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "sk-ant-oat-test"},
		Metadata:   map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}
	body := captureNativeToolSearchBody(t, &config.Config{}, sendNativeToolSearchExecute, auth, "claude-sonnet-4-5", []byte(nativeToolSearchResponsesPayload), nil)
	declared := map[string]bool{}
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		declared[tool.Get("name").String()] = true
		return true
	})
	references := 0
	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			block.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() != "tool_reference" {
					return true
				}
				references++
				if name := part.Get("tool_name").String(); !declared[name] {
					t.Fatalf("tool_reference %q has no declared tool in %v", name, declared)
				}
				return true
			})
			return true
		})
		return true
	})
	if references == 0 {
		t.Fatalf("no tool_reference in OAuth body: %s", gjson.GetBytes(body, "messages").Raw)
	}
}

// A discovered non-MCP tool is aliased on cloaked OAuth requests; its
// tool_reference must carry the same alias as its deferred declaration.
func TestClaudeExecutor_NativeToolSearchOAuthAliasesRenamedReferences(t *testing.T) {
	enableClaudeToolSearchForTest(t, true)
	payload := []byte(`{"model":"claude-sonnet-4-5","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"look up order 7"}]},` +
		`{"type":"tool_search_call","call_id":"call_search_1","status":"completed","execution":"client","arguments":{"query":"order"}},` +
		`{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[{"type":"function","name":"lookup_order","description":"Look up an order.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}]}` +
		`],"tools":[` +
		`{"type":"function","name":"exec_command","description":"Run a command.","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}},` +
		`{"type":"tool_search","execution":"client","description":"Search deferred tools.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}` +
		`],"stream":false}`)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "sk-ant-oat-test"},
		Metadata:   map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}
	body := captureNativeToolSearchBody(t, &config.Config{}, sendNativeToolSearchExecute, auth, "claude-sonnet-4-5", payload, nil)
	if gjson.GetBytes(body, `tools.#(name=="lookup_order")`).Exists() {
		t.Fatalf("OAuth request kept the unaliased tool name: %s", gjson.GetBytes(body, "tools").Raw)
	}
	var deferred []string
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		// The constant deferred anchor is aliased too but never referenced.
		if tool.Get("defer_loading").Bool() && !strings.HasSuffix(tool.Get("name").String(), "tool_search_deferred_anchor") {
			deferred = append(deferred, tool.Get("name").String())
		}
		return true
	})
	if len(deferred) != 1 {
		t.Fatalf("deferred tools = %v, want exactly the aliased lookup_order", deferred)
	}
	var references []string
	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			block.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "tool_reference" {
					references = append(references, part.Get("tool_name").String())
				}
				return true
			})
			return true
		})
		return true
	})
	if len(references) != 1 || references[0] != deferred[0] {
		t.Fatalf("tool_reference names = %v, want the deferred alias %q", references, deferred[0])
	}
}
