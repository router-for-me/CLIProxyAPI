package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// toolSearchExecutorGoldenPayload is a Codex Responses turn with a client
// tool_search session plus a named declaration, which predates tool_search
// support only as a generic tool.
const toolSearchExecutorGoldenPayload = `{"model":"claude-sonnet-4-5","input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"create a calendar event"}]},` +
	`{"type":"tool_search_call","call_id":"call_search_1","status":"completed","execution":"client","arguments":{"query":"calendar"}},` +
	`{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[{"type":"namespace","name":"mcp__codex_apps__","description":"Apps.","tools":[{"type":"function","name":"calendar_create_event","description":"Create a calendar event.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}}]}]}` +
	`],"tools":[` +
	`{"type":"tool_search","execution":"client","name":"ClientSearch","description":"Named search."},` +
	`{"type":"function","name":"exec_command","description":"Run a command.","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}},` +
	`{"type":"function","name":"lookup_order","description":"Look up an order.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}},` +
	`{"type":"tool_search","execution":"client","description":"Search deferred tools.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}` +
	`],"stream":false}`

var toolSearchExecutorGoldenCCH = regexp.MustCompile(`cch=[0-9a-f]+;`)

// toolSearchExecutorGoldenDate is the calendar day the dev golden was recorded
// on. OAuth cloaking injects a currentDate reminder, so the render pins the
// executor clock to this day to keep the golden comparison date-independent.
var toolSearchExecutorGoldenDate = time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)

// renderToolSearchExecutorGolden captures the upstream bodies the Claude
// executor sends for toolSearchExecutorGoldenPayload on every request path.
func renderToolSearchExecutorGolden(t *testing.T) map[string]string {
	t.Helper()
	helps.ResetClaudeDiagnosticsForTest()
	t.Cleanup(helps.ResetClaudeDiagnosticsForTest)
	claudeCodeCurrentTimeFunc = func(*config.Config, *cliproxyauth.Auth) time.Time {
		return toolSearchExecutorGoldenDate
	}
	t.Cleanup(func() { claudeCodeCurrentTimeFunc = nil })
	payload := []byte(toolSearchExecutorGoldenPayload)
	auths := map[string]map[string]string{
		"official": {"api_key": "key-123", "cloak_mode": "never"},
		"gateway":  {"api_key": "key-123", "cloak_mode": "never", "base_url": "https://gateway.example.com", "native_tool_search": "true"},
		"oauth":    {"api_key": "sk-ant-oat-test"},
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
	request := cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: payload}
	golden := make(map[string]string)
	for authName, attrs := range auths {
		for _, path := range []string{"execute", "stream", "count_tokens"} {
			auth := &cliproxyauth.Auth{Attributes: attrs, Metadata: map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}
			upstream := &midSystemUpstream{}
			ctx := upstream.context(t, nil)
			ex := NewClaudeExecutor(&config.Config{})
			var errSend error
			switch path {
			case "execute":
				_, errSend = ex.Execute(ctx, auth, request, opts)
			case "stream":
				result, errStream := ex.ExecuteStream(ctx, auth, request, opts)
				errSend = errStream
				if errStream == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errSend = chunk.Err
						}
					}
				}
			case "count_tokens":
				_, errSend = ex.CountTokens(ctx, auth, request, opts)
			}
			if errSend != nil {
				t.Fatalf("%s %s: %v", authName, path, errSend)
			}
			// OAuth cloaking derives metadata.user_id from a random device id,
			// and the billing header's cch hash covers it.
			body, _ := sjson.DeleteBytes(upstream.body, "metadata.user_id")
			golden[authName+"/"+path] = toolSearchExecutorGoldenCCH.ReplaceAllString(string(body), "cch=0;")
		}
	}
	return golden
}

// The dev golden was produced by running renderToolSearchExecutorGolden against
// the executor at origin/dev d739485df, before Codex tool_search support. With
// upstream.claude.tool-search off every request path must reproduce it, even
// for credentials that would otherwise use native tool search.
func TestClaudeExecutor_ToolSearchDisabledMatchesDevGolden(t *testing.T) {
	enableClaudeToolSearchForTest(t, false)
	if NewClaudeExecutor(&config.Config{}).SupportsNativeToolSearch(nativeToolSearchAuth(nil), "claude-sonnet-4-5") {
		t.Fatal("SupportsNativeToolSearch() = true with upstream.claude.tool-search off")
	}
	raw, errRead := os.ReadFile(filepath.Join("testdata", "tool_search_disabled", "dev_executor_golden.json"))
	if errRead != nil {
		t.Fatalf("read golden: %v", errRead)
	}
	var want map[string]string
	if errUnmarshal := json.Unmarshal(raw, &want); errUnmarshal != nil {
		t.Fatalf("decode golden: %v", errUnmarshal)
	}
	got := renderToolSearchExecutorGolden(t)
	if len(got) != len(want) {
		t.Fatalf("rendered %d bodies, golden has %d", len(got), len(want))
	}
	for name, body := range got {
		if body != want[name] {
			t.Errorf("%s differs from dev\n got: %s\nwant: %s", name, body, want[name])
		}
	}
}

// With the switch on, the same request must leave the dev translation, so the
// golden comparison above cannot pass by accident.
func TestClaudeExecutor_ToolSearchEnabledDiffersFromDevGolden(t *testing.T) {
	enableClaudeToolSearchForTest(t, true)
	raw, errRead := os.ReadFile(filepath.Join("testdata", "tool_search_disabled", "dev_executor_golden.json"))
	if errRead != nil {
		t.Fatalf("read golden: %v", errRead)
	}
	var want map[string]string
	if errUnmarshal := json.Unmarshal(raw, &want); errUnmarshal != nil {
		t.Fatalf("decode golden: %v", errUnmarshal)
	}
	if got := renderToolSearchExecutorGolden(t); got["official/execute"] == want["official/execute"] {
		t.Fatal("official/execute matches dev with upstream.claude.tool-search on")
	}
}

// toolSearchReloadContext routes the executor to a stub upstream that flips
// upstream.claude.tool-search to flipTo when the request arrives, as a config
// reload would, and answers with a Claude call to the tool_search tool.
func toolSearchReloadContext(t *testing.T, flipTo bool) (context.Context, *[]byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(nil)
	ginCtx.Request = httptest_NewRequest()
	ginCtx.Request.Header = make(http.Header)
	var sent []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		sent = body
		responsesclaude.SetClientToolSearchEnabled(flipTo)
		contentType := "application/json"
		response := `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_search","name":"tool_search","input":{"query":"calendar"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
		if gjson.GetBytes(body, "stream").Bool() {
			contentType = "text/event-stream"
			response = strings.Join([]string{
				`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_search","name":"tool_search","input":{}}}`,
				`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"calendar\"}"}}`,
				`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
				`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}`,
				`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
			}, "\n\n") + "\n\n"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	return context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport)), &sent
}

// A config reload while a request is upstream must not change how that
// request's response is translated: the value seen when it started holds.
func TestClaudeExecutor_ToolSearchSwitchIsPinnedPerRequest(t *testing.T) {
	payload := []byte(nativeToolSearchResponsesPayload)
	auth := nativeToolSearchAuth(nil)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
	for _, tc := range []struct {
		name      string
		start     bool
		wantItem  string
		wantTools bool
	}{
		{name: "on then reloaded off", start: true, wantItem: "tool_search_call", wantTools: true},
		{name: "off then reloaded on", start: false, wantItem: "function_call", wantTools: false},
	} {
		t.Run(tc.name+" execute", func(t *testing.T) {
			enableClaudeToolSearchForTest(t, tc.start)
			ctx, sent := toolSearchReloadContext(t, !tc.start)
			resp, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: payload}, opts)
			if errExecute != nil {
				t.Fatalf("Execute: %v", errExecute)
			}
			if got := gjson.GetBytes(*sent, `tools.#(name=="tool_search")`).Exists(); got != tc.wantTools {
				t.Fatalf("upstream tool_search declared = %v, want %v: %s", got, tc.wantTools, *sent)
			}
			if got := gjson.GetBytes(resp.Payload, "output.0.type").String(); got != tc.wantItem {
				t.Fatalf("output item = %s, want %s: %s", got, tc.wantItem, resp.Payload)
			}
		})
		t.Run(tc.name+" stream", func(t *testing.T) {
			enableClaudeToolSearchForTest(t, tc.start)
			ctx, sent := toolSearchReloadContext(t, !tc.start)
			result, errStream := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: payload}, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream: %v", errStream)
			}
			var items []string
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk: %v", chunk.Err)
				}
				for _, line := range strings.Split(string(chunk.Payload), "\n") {
					data := gjson.Parse(strings.TrimPrefix(line, "data: "))
					if data.Get("type").String() == "response.output_item.done" {
						items = append(items, data.Get("item.type").String())
					}
				}
			}
			if got := gjson.GetBytes(*sent, `tools.#(name=="tool_search")`).Exists(); got != tc.wantTools {
				t.Fatalf("upstream tool_search declared = %v, want %v: %s", got, tc.wantTools, *sent)
			}
			if len(items) != 1 || items[0] != tc.wantItem {
				t.Fatalf("stream output items = %v, want [%s]", items, tc.wantItem)
			}
		})
	}
}

// A reload between the fallback decision and translation must not change the
// translation: it follows the value pinned when the fallback was decided.
func TestClaudeExecutor_ToolSearchFallbackPinsTranslation(t *testing.T) {
	hosted := `{"type":"tool_search","name":"HostedSearch"}`
	payload := []byte(strings.Replace(nativeToolSearchResponsesPayload, `],"stream":false}`, `,`+hosted+`],"stream":false}`, 1))
	official := nativeToolSearchAuth(nil)
	gateway := nativeToolSearchAuth(map[string]string{"base_url": nativeToolSearchGateway})
	for _, tc := range []struct {
		name          string
		start         bool
		auth          *cliproxyauth.Auth
		wantToolNames map[string]bool
	}{
		// Native: the client search tool stays and hosted search is dropped.
		{name: "on official then off", start: true, auth: official, wantToolNames: map[string]bool{"tool_search": true, "HostedSearch": false}},
		// Fallback strips client search; the pinned translation still drops hosted search.
		{name: "on gateway then off", start: true, auth: gateway, wantToolNames: map[string]bool{"tool_search": false, "HostedSearch": false}},
		// Dev: no client search tool and the named hosted declaration passes through.
		{name: "off official then on", start: false, auth: official, wantToolNames: map[string]bool{"tool_search": false, "HostedSearch": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enableClaudeToolSearchForTest(t, tc.start)
			ex := NewClaudeExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
			ctx, req, _ := ex.applyNativeToolSearchFallback(context.Background(), tc.auth, sdktranslator.FormatOpenAIResponse, "claude-sonnet-4-5", req, opts)
			responsesclaude.SetClientToolSearchEnabled(!tc.start)
			body, errTranslate := helps.TranslateRequestReturningError(ctx, nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, "claude-sonnet-4-5", req.Payload, false, false)
			if errTranslate != nil {
				t.Fatalf("translate: %v", errTranslate)
			}
			for name, want := range tc.wantToolNames {
				if got := gjson.GetBytes(body, `tools.#(name=="`+name+`")`).Exists(); got != want {
					t.Errorf("tool %s declared = %v, want %v: %s", name, got, want, gjson.GetBytes(body, "tools").Raw)
				}
			}
		})
	}
}
