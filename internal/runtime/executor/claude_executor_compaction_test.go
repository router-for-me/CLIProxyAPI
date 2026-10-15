package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	claudeCompactionSummaryPrompt = "Please provide a concise and comprehensive summary"
	claudeCompactionSummaryText   = "Sealed summary of the build."
)

func TestClaudeExecutor_CompactionTriggerReturnsSingleCompactionItem(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(claudeCompactionUpstream(t, &seen))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Check the build next."}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK, I will check the build."}]},` +
		`{"type":"compaction_trigger"}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	assertSingleCompactionOutput(t, resp.Payload)
	assertClaudeCompactionSummaryRequest(t, seen)
}

func TestClaudeExecutor_CompactionTriggerStreamReturnsSingleCompactionItem(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(claudeCompactionUpstream(t, &seen))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":true,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Check the build next."}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK, I will check the build."}]},` +
		`{"type":"compaction_trigger"}]}`)

	streamResult, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		Stream:          true,
		OriginalRequest: payload,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var buf bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		buf.Write(chunk.Payload)
	}
	completed := claudeCompactionCompletedResponse(t, buf.String())
	assertSingleCompactionOutput(t, []byte(completed.Raw))
	assertClaudeCompactionSummaryRequest(t, seen)
}

func TestClaudeExecutor_ReplayedCompactionCapsuleIsExpanded(t *testing.T) {
	capsule, errSeal := helps.SealAntigravityCompaction(claudeCompactionSummaryText, "claude-haiku-4-5-20251001")
	if errSeal != nil {
		t.Fatalf("SealAntigravityCompaction() error = %v", errSeal)
	}

	var seen []byte
	server := httptest.NewServer(claudeCompactionUpstream(t, &seen))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"input":[` +
		`{"type":"compaction","encrypted_content":` + gjson.Get(`{"v":"`+capsule+`"}`, "v").Raw + `},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if !bytes.Contains(seen, []byte(claudeCompactionSummaryText)) {
		t.Fatalf("replayed compaction summary missing from upstream request: %s", seen)
	}
	if bytes.Contains(seen, []byte(`"type":"compaction"`)) || bytes.Contains(seen, []byte(`"type": "compaction"`)) {
		t.Fatalf("compaction item reached upstream request: %s", seen)
	}
}

func TestClaudeExecutor_CompactionTriggerKeepsToolsForToolHistory(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(claudeCompactionUpstream(t, &seen))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"tools":[{"type":"function","name":"check_build","description":"Check the build","parameters":{"type":"object","properties":{}}}],"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Check the build next."}]},` +
		`{"type":"function_call","call_id":"call_1","name":"check_build","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"build ok"},` +
		`{"type":"compaction_trigger"}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	assertSingleCompactionOutput(t, resp.Payload)
	assertClaudeCompactionSummaryRequest(t, seen)
	if !bytes.Contains(seen, []byte("check_build")) {
		t.Fatalf("tool definition missing from summary request: %s", seen)
	}
	if !bytes.Contains(seen, []byte(`"type":"tool_use"`)) || !bytes.Contains(seen, []byte(`"type":"tool_result"`)) {
		t.Fatalf("tool history missing from summary request: %s", seen)
	}
	if got := gjson.GetBytes(seen, "tool_choice.type").String(); got != "none" {
		t.Fatalf("tool_choice.type = %q, want none so the summary cannot call tools: %s", got, seen)
	}
}

func TestClaudeExecutor_CompactionTriggerFlattensToolHistoryWithoutTools(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(claudeCompactionUpstream(t, &seen))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Check the build next."}]},` +
		`{"type":"function_call","call_id":"call_1","name":"check_build","arguments":"{\"target\":\"api\"}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"build ok"},` +
		`{"type":"compaction_trigger"}]}`)

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if bytes.Contains(seen, []byte(`"type":"tool_use"`)) || bytes.Contains(seen, []byte(`"type":"tool_result"`)) {
		t.Fatalf("tool blocks reached summary request without tool definitions: %s", seen)
	}
	if !bytes.Contains(seen, []byte("build ok")) || !bytes.Contains(seen, []byte("check_build")) {
		t.Fatalf("flattened tool history missing from summary request: %s", seen)
	}
}

func TestClaudeExecutor_CompactionSummaryPayloadRuleIsLast(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(claudeCompactionUpstream(t, &seen))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "claude-haiku-4-5-20251001"}},
				Params: map[string]any{
					"max_tokens":  1234,
					"tool_choice": map[string]any{"type": "auto"},
				},
			}},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"tools":[{"type":"function","name":"check_build","description":"Check the build","parameters":{"type":"object","properties":{}}}],"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Check the build next."}]},` +
		`{"type":"function_call","call_id":"call_1","name":"check_build","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"build ok"},` +
		`{"type":"compaction_trigger"}]}`)

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(seen, "max_tokens").Int(); got != 1234 {
		t.Fatalf("max_tokens = %d, want payload rule 1234; body=%s", got, seen)
	}
	if got := gjson.GetBytes(seen, "tool_choice.type").String(); got != "auto" {
		t.Fatalf("tool_choice.type = %q, want payload rule auto to win over built-in none; body=%s", got, seen)
	}
}

func TestClaudeExecutor_CompactionTriggerIncludesPreviousCapsule(t *testing.T) {
	capsule, errSeal := helps.SealAntigravityCompaction("Earlier sealed summary.", "claude-haiku-4-5-20251001")
	if errSeal != nil {
		t.Fatalf("SealAntigravityCompaction() error = %v", errSeal)
	}
	var seen []byte
	server := httptest.NewServer(claudeCompactionUpstream(t, &seen))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"input":[` +
		`{"type":"compaction","encrypted_content":` + gjson.Get(`{"v":"`+capsule+`"}`, "v").Raw + `},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"continued"}]},` +
		`{"type":"compaction_trigger"}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	assertSingleCompactionOutput(t, resp.Payload)
	if !bytes.Contains(seen, []byte("Earlier sealed summary.")) {
		t.Fatalf("previous capsule missing from the next summary request: %s", seen)
	}
	assertClaudeCompactionSummaryRequest(t, seen)
}

func TestClaudeExecutor_ForeignCompactionCapsuleIsDropped(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(claudeCompactionUpstream(t, &seen))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"input":[` +
		`{"type":"compaction","encrypted_content":"gAAAAA-openai-native"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]},` +
		`{"type":"compaction_trigger"}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	assertSingleCompactionOutput(t, resp.Payload)
	if bytes.Contains(seen, []byte("gAAAAA-openai-native")) {
		t.Fatalf("foreign compaction capsule reached upstream: %s", seen)
	}
	assertClaudeCompactionSummaryRequest(t, seen)
}

func TestClaudeExecutor_CompactionUsageMatchesResponsesAccounting(t *testing.T) {
	var seen []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
		}
		seen = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_compact","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"Sealed summary of the build."}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":7,"cache_creation_input_tokens":3,"cache_read_input_tokens":100}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Check the build next."}]},` +
		`{"type":"compaction_trigger"}]}`)

	resp, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	usage := gjson.GetBytes(resp.Payload, "usage")
	if usage.Get("input_tokens").Int() != 114 || usage.Get("output_tokens").Int() != 7 || usage.Get("total_tokens").Int() != 121 || usage.Get("input_tokens_details.cached_tokens").Int() != 100 {
		t.Fatalf("usage = %s, want input 114 output 7 total 121 cached 100", usage.Raw)
	}
	_ = seen
}

func TestClaudeExecutor_InvalidCompactionCapsuleReturns400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid compaction capsule must not reach upstream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-compaction",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-haiku-4-5-20251001","stream":false,"input":[` +
		`{"type":"compaction","encrypted_content":"cpa-ag-compact-v1:not-valid-base64"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)

	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-haiku-4-5-20251001",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: payload,
	})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want 400")
	}
	assertStatusErr(t, errExecute, http.StatusBadRequest)
}

func claudeCompactionUpstream(t *testing.T, seen *[]byte) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream body: %v", errRead)
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		*seen = body
		if gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(strings.Join([]string{
				`event: message_start`,
				`data: {"type":"message_start","message":{"id":"msg_compact","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":11,"output_tokens":0}}}`,
				`event: content_block_start`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me examine the current build status."}}`,
				`event: content_block_stop`,
				`data: {"type":"content_block_stop","index":0}`,
				`event: message_delta`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
				`event: message_stop`,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_compact","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"Sealed summary of the build."}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":7}}`))
	})
}

func assertSingleCompactionOutput(t *testing.T, payload []byte) {
	t.Helper()
	output := gjson.GetBytes(payload, "output")
	if !output.Exists() {
		output = gjson.GetBytes(payload, "response.output")
	}
	items := output.Array()
	if len(items) != 1 || items[0].Get("type").String() != "compaction" {
		t.Fatalf("output types = %s, want exactly one compaction; payload=%s", compactionOutputTypes(items), payload)
	}
	plain, errUnseal := helps.UnsealAntigravityCompaction(items[0].Get("encrypted_content").String())
	if errUnseal != nil {
		t.Fatalf("unseal compaction capsule: %v", errUnseal)
	}
	if plain != claudeCompactionSummaryText {
		t.Fatalf("capsule summary = %q, want %q", plain, claudeCompactionSummaryText)
	}
	usage := gjson.GetBytes(payload, "usage")
	if !usage.Exists() {
		usage = gjson.GetBytes(payload, "response.usage")
	}
	if usage.Get("input_tokens").Int() != 11 || usage.Get("output_tokens").Int() != 7 || usage.Get("total_tokens").Int() != 18 {
		t.Fatalf("usage = %s, want input 11 output 7 total 18", usage.Raw)
	}
}

func compactionOutputTypes(items []gjson.Result) string {
	if len(items) == 0 {
		return "<empty>"
	}
	types := make([]string, 0, len(items))
	for _, item := range items {
		types = append(types, item.Get("type").String())
	}
	return strings.Join(types, ",")
}

func assertClaudeCompactionSummaryRequest(t *testing.T, body []byte) {
	t.Helper()
	if !bytes.Contains(body, []byte(claudeCompactionSummaryPrompt)) {
		t.Fatalf("upstream body missing summary prompt %q: %s", claudeCompactionSummaryPrompt, body)
	}
	if bytes.Contains(body, []byte("compaction_trigger")) {
		t.Fatalf("compaction_trigger reached upstream: %s", body)
	}
}

func claudeCompactionCompletedResponse(t *testing.T, stream string) gjson.Result {
	t.Helper()
	for _, frame := range strings.Split(stream, "\n\n") {
		if !strings.Contains(frame, "event: response.completed") {
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			return gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	t.Fatalf("response.completed missing from stream: %s", stream)
	return gjson.Result{}
}
