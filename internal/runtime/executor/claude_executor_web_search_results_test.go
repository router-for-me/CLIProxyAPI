package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeWebSearchExecutorResults = `[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc-a","page_age":null},{"type":"web_search_result","url":"https://example.com/b","title":"B","encrypted_content":"enc-b","page_age":null}]`

// claudeWebSearchUpstream answers the first request with a native web search
// and every later one with plain text, recording each request body.
func claudeWebSearchUpstream(t *testing.T, bodies *[][]byte, mu *sync.Mutex) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		mu.Lock()
		*bodies = append(*bodies, body)
		mu.Unlock()

		blocks := []string{`{"type":"text","text":"done"}`}
		if !strings.Contains(string(body), `"web_search_tool_result"`) {
			blocks = []string{
				`{"type":"server_tool_use","id":"srvtoolu_01abc","name":"web_search","input":{"query":"q"}}`,
				`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_01abc","content":` + claudeWebSearchExecutorResults + `}`,
				`{"type":"text","text":"found two pages"}`,
			}
		}
		if !gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_ws","type":"message","role":"assistant","model":"claude-opus-5-5","content":[` + strings.Join(blocks, ",") + `],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":5}}`))
			return
		}
		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_ws\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}",
		}
		for i, block := range blocks {
			index := string(rune('0' + i))
			start := block
			if strings.Contains(block, `"type":"text"`) {
				start = `{"type":"text","text":""}`
			}
			events = append(events, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":"+index+",\"content_block\":"+start+"}")
			if strings.Contains(block, `"type":"text"`) {
				events = append(events, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":"+index+",\"delta\":{\"type\":\"text_delta\",\"text\":"+gjson.Get(block, "text").Raw+"}}")
			}
			events = append(events, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":"+index+"}")
		}
		events = append(events,
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}",
		)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join(events, "\n\n") + "\n\n"))
	})
}

type claudeWebSearchExecutorRun struct {
	sessionID string
	input     string
}

func runClaudeWebSearchScenario(t *testing.T, cfg *config.Config, stream bool, runs []claudeWebSearchExecutorRun) [][]byte {
	t.Helper()
	cache.ClearClaudeWebSearchResultCache()
	t.Cleanup(cache.ClearClaudeWebSearchResultCache)
	var (
		bodies [][]byte
		mu     sync.Mutex
	)
	server := httptest.NewServer(claudeWebSearchUpstream(t, &bodies, &mu))
	defer server.Close()

	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-web-search", "base_url": server.URL}}
	streamJSON := "false"
	if stream {
		streamJSON = "true"
	}
	for _, run := range runs {
		payload := []byte(`{"model":"claude-opus-5-5","stream":` + streamJSON + `,"input":[` + run.input + `]}`)
		req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
		opts := cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			ResponseFormat:  sdktranslator.FormatOpenAIResponse,
			Stream:          stream,
			OriginalRequest: payload,
		}
		if run.sessionID != "" {
			opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: run.sessionID}
		}
		if !stream {
			if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			continue
		}
		result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("chunk error: %v", chunk.Err)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return append([][]byte(nil), bodies...)
}

const (
	claudeWebSearchExecutorUser = `{"type":"message","role":"user","content":[{"type":"input_text","text":"search for q"}]}`
	// The history as Codex stores it: the web_search_call has no results.
	claudeWebSearchExecutorHistory = claudeWebSearchExecutorUser +
		`,{"type":"web_search_call","id":"ws_srvtoolu_01abc","status":"completed","action":{"type":"search","query":"q"}},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"found two pages"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"how many results?"}]}`
)

func enabledClaudeWebSearchCacheConfig() *config.Config {
	return &config.Config{
		SDKConfig: config.SDKConfig{
			Client: config.ClientConfig{
				Codex: config.CodexClientConfig{
					WebSearchResultCache: config.CodexWebSearchResultCacheConfig{
						Enabled: true,
						TTL:     "30m",
						Limit:   16,
					},
				},
			},
		},
	}
}

func TestClaudeExecutor_RestoresWebSearchResultsDroppedByResponsesClient(t *testing.T) {
	cfg, errParse := config.ParseConfigBytes([]byte("client:\n  codex:\n    web-search-result-cache:\n      enabled: true\n      ttl: 30m\n      limit: 16\n"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			bodies := runClaudeWebSearchScenario(t, cfg, stream, []claudeWebSearchExecutorRun{
				{sessionID: "session-ws", input: claudeWebSearchExecutorUser},
				{sessionID: "session-ws", input: claudeWebSearchExecutorHistory},
				{sessionID: "other-session", input: claudeWebSearchExecutorHistory},
				{sessionID: "", input: claudeWebSearchExecutorHistory},
			})
			if len(bodies) != 4 {
				t.Fatalf("upstream requests = %d, want 4", len(bodies))
			}
			if got := claudeWebSearchResultContent(t, bodies[1]); got != claudeWebSearchExecutorResults {
				t.Fatalf("same session: web_search_tool_result content = %s, want original results", got)
			}
			if got := claudeWebSearchResultContent(t, bodies[2]); got != "[]" {
				t.Fatalf("other session: web_search_tool_result content = %s, want []", got)
			}
			if got := claudeWebSearchResultContent(t, bodies[3]); got != "[]" {
				t.Fatalf("no session identity: web_search_tool_result content = %s, want []", got)
			}
		})
	}
}

func TestClaudeExecutor_WebSearchResultCacheDisabledByDefault(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			bodies := runClaudeWebSearchScenario(t, &config.Config{}, stream, []claudeWebSearchExecutorRun{
				{sessionID: "session-ws", input: claudeWebSearchExecutorUser},
				{sessionID: "session-ws", input: claudeWebSearchExecutorHistory},
			})
			if len(bodies) != 2 {
				t.Fatalf("upstream requests = %d, want 2", len(bodies))
			}
			if got := claudeWebSearchResultContent(t, bodies[1]); got != "[]" {
				t.Fatalf("disabled cache: web_search_tool_result content = %s, want []", got)
			}
		})
	}
}

// User payload rules stay the final barrier: a filter rule that removes the
// result content must not be undone by the restore.
func TestClaudeExecutor_WebSearchResultRestoreRunsBeforePayloadRules(t *testing.T) {
	cfg := enabledClaudeWebSearchCacheConfig()
	cfg.Payload = config.PayloadConfig{Filter: []config.PayloadFilterRule{{
		Models: []config.PayloadModelRule{{Name: "claude-opus-5-5"}},
		Params: []string{"messages.1.content.1.content"},
	}}}
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			bodies := runClaudeWebSearchScenario(t, cfg, stream, []claudeWebSearchExecutorRun{
				{sessionID: "session-ws", input: claudeWebSearchExecutorUser},
				{sessionID: "session-ws", input: claudeWebSearchExecutorHistory},
			})
			if len(bodies) != 2 {
				t.Fatalf("upstream requests = %d, want 2", len(bodies))
			}
			block := gjson.GetBytes(bodies[1], "messages.1.content.1")
			if block.Get("type").String() != "web_search_tool_result" {
				t.Fatalf("messages.1.content.1 = %s, want the web_search_tool_result block", block.Raw)
			}
			if content := block.Get("content"); content.Exists() {
				t.Fatalf("filtered content came back: %s", content.Raw)
			}
		})
	}
}

func TestClaudeExecutor_WebSearchResultCacheHonorsConfiguredLimit(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			Client: config.ClientConfig{
				Codex: config.CodexClientConfig{
					WebSearchResultCache: config.CodexWebSearchResultCacheConfig{
						Enabled: true,
						TTL:     "1800",
						Limit:   1,
					},
				},
			},
		},
	}
	bodies := runClaudeWebSearchScenario(t, cfg, false, []claudeWebSearchExecutorRun{
		{sessionID: "session-1", input: claudeWebSearchExecutorUser},
		{sessionID: "session-2", input: claudeWebSearchExecutorUser},
		{sessionID: "session-1", input: claudeWebSearchExecutorHistory},
		{sessionID: "session-2", input: claudeWebSearchExecutorHistory},
	})
	if len(bodies) != 4 {
		t.Fatalf("upstream requests = %d, want 4", len(bodies))
	}
	// Limit is 1, so session-2's search result evicted session-1's entry.
	if got := claudeWebSearchResultContent(t, bodies[2]); got != "[]" {
		t.Fatalf("evicted session-1: web_search_tool_result content = %s, want []", got)
	}
	if got := claudeWebSearchResultContent(t, bodies[3]); got != claudeWebSearchExecutorResults {
		t.Fatalf("retained session-2: web_search_tool_result content = %s, want original results", got)
	}
}

func TestClaudeExecutor_CountTokensRestoresWebSearchResultsWhenEnabled(t *testing.T) {
	cache.ClearClaudeWebSearchResultCache()
	t.Cleanup(cache.ClearClaudeWebSearchResultCache)

	var bodies [][]byte
	var mu sync.Mutex
	server := httptest.NewServer(claudeWebSearchUpstream(t, &bodies, &mu))
	defer server.Close()

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-web-search", "base_url": server.URL}}
	enabledExec := NewClaudeExecutor(enabledClaudeWebSearchCacheConfig())
	disabledExec := NewClaudeExecutor(&config.Config{})

	firstPayload := []byte(`{"model":"claude-opus-5-5","stream":false,"input":[` + claudeWebSearchExecutorUser + `]}`)
	firstReq := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: firstPayload}
	firstOpts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: firstPayload,
		Metadata:        map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "session-count"},
	}
	if _, errExec := enabledExec.Execute(context.Background(), auth, firstReq, firstOpts); errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	historyPayload := []byte(`{"model":"claude-opus-5-5","stream":false,"input":[` + claudeWebSearchExecutorHistory + `]}`)
	historyReq := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: historyPayload}
	historyOpts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: historyPayload,
		Metadata:        map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "session-count"},
	}
	enabledCountResp, errEnabledCount := enabledExec.CountTokens(context.Background(), auth, historyReq, historyOpts)
	if errEnabledCount != nil {
		t.Fatalf("enabled CountTokens() error = %v", errEnabledCount)
	}
	disabledCountResp, errDisabledCount := disabledExec.CountTokens(context.Background(), auth, historyReq, historyOpts)
	if errDisabledCount != nil {
		t.Fatalf("disabled CountTokens() error = %v", errDisabledCount)
	}
	enabledTokens := gjson.GetBytes(enabledCountResp.Payload, "input_tokens").Int()
	disabledTokens := gjson.GetBytes(disabledCountResp.Payload, "input_tokens").Int()
	if enabledTokens <= disabledTokens {
		t.Fatalf("enabled input_tokens (%d) <= disabled input_tokens (%d); want restored search results counted", enabledTokens, disabledTokens)
	}
}

func claudeWebSearchResultContent(t *testing.T, body []byte) string {
	t.Helper()
	var found string
	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "web_search_tool_result" {
				found = block.Get("content").Raw
				return false
			}
			return true
		})
		return found == ""
	})
	if found == "" {
		t.Fatalf("no web_search_tool_result block in upstream body: %s", body)
	}
	return found
}
