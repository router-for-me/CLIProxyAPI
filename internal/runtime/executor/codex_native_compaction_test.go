package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexNativeCompactionV2RewritesFollowupAsExactAppend(t *testing.T) {
	var mu sync.Mutex
	var requestBodies [][]byte
	var betaHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, append([]byte(nil), body...))
		betaHeaders = append(betaHeaders, r.Header.Get("X-Codex-Beta-Features"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"opaque-summary\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":20,\"output_tokens\":3,\"total_tokens\":23}}}\n\n")
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled:               true,
		TriggerTokens:         3,
		ContextWindow:         1000,
		PreserveRecentTokens:  1,
		RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-v2", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol(xhigh)",
		Payload: []byte(`{"model":"gpt-5.6-sol(xhigh)","metadata":{"user_id":"user_a_account_b_session_deadbeef-1"}}`),
	}
	body := []byte(`{"model":"gpt-5.6-sol","instructions":"","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"first question"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"current question"}]}],"tools":[],"reasoning":{"effort":"xhigh"},"stream":true}`)

	firstBody, firstScope, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, cliproxyexecutor.Options{}, req.Payload, body,
		"gpt-5.6-sol", "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare first request: %v", err)
	}
	if !active || !firstScope.active {
		t.Fatal("expected an active compaction lane")
	}
	firstInput := gjson.GetBytes(firstBody, "input").Array()
	if len(firstInput) < 2 {
		t.Fatalf("compacted input too short: %s", firstBody)
	}
	compactionIndex := -1
	for i := range firstInput {
		if firstInput[i].Get("type").String() == "compaction" {
			compactionIndex = i
			if got := firstInput[i].Get("encrypted_content").String(); got != "opaque-summary" {
				t.Fatalf("compaction encrypted_content = %q", got)
			}
		}
	}
	if compactionIndex < 0 || compactionIndex == len(firstInput)-1 {
		t.Fatalf("expected compaction item followed by preserved current tail: %s", firstBody)
	}
	firstScope.observeTerminal([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":11}}}`))

	mu.Lock()
	if len(requestBodies) != 1 {
		mu.Unlock()
		t.Fatalf("compaction requests = %d, want 1", len(requestBodies))
	}
	compactRequest := append([]byte(nil), requestBodies[0]...)
	betaHeader := betaHeaders[0]
	mu.Unlock()
	compactInput := gjson.GetBytes(compactRequest, "input").Array()
	if len(compactInput) == 0 || compactInput[len(compactInput)-1].Get("type").String() != "compaction_trigger" {
		t.Fatalf("v2 request does not end in compaction_trigger: %s", compactRequest)
	}
	if !strings.Contains(betaHeader, codexRemoteCompactionV2Feature) {
		t.Fatalf("beta feature header = %q", betaHeader)
	}
	if got := gjson.GetBytes(compactRequest, "prompt_cache_key").String(); got == "" {
		t.Fatalf("compaction request missing stable prompt_cache_key: %s", compactRequest)
	}

	// Raise the threshold so this follow-up exercises lane reuse without a
	// second compact call. Its upstream input must be the first compacted input
	// plus exactly the newly appended source item.
	cfg.Codex.NativeCompaction.TriggerTokens = 1000
	followupItem := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"new append"}]}`)
	originalItems, _ := codexInputItems(body)
	followupBody := codexSetInputItems(body, append(originalItems, followupItem))
	secondBody, secondScope, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, cliproxyexecutor.Options{}, req.Payload, followupBody,
		"gpt-5.6-sol", "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare follow-up request: %v", err)
	}
	if !active || !secondScope.active {
		t.Fatal("expected follow-up to reuse the active compaction lane")
	}
	secondInput := gjson.GetBytes(secondBody, "input").Array()
	if len(secondInput) != len(firstInput)+1 {
		t.Fatalf("follow-up input length = %d, want %d; body=%s", len(secondInput), len(firstInput)+1, secondBody)
	}
	for i := range firstInput {
		if firstInput[i].Raw != secondInput[i].Raw {
			t.Fatalf("follow-up changed cached prefix at item %d\nfirst=%s\nsecond=%s", i, firstInput[i].Raw, secondInput[i].Raw)
		}
	}
	if secondInput[len(secondInput)-1].Get("content.0.text").String() != "new append" {
		t.Fatalf("follow-up did not append the new item: %s", secondBody)
	}
	mu.Lock()
	gotRequests := len(requestBodies)
	mu.Unlock()
	if gotRequests != 1 {
		t.Fatalf("follow-up unexpectedly recompacted; requests=%d", gotRequests)
	}
}

func TestCodexNativeCompactionSeparatesInterleavedClaudeAgents(t *testing.T) {
	var mu sync.Mutex
	compactionRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		compactionRequests++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"opaque-agent-a\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":20,\"output_tokens\":3,\"total_tokens\":23}}}\n\n")
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled:               true,
		TriggerTokens:         3,
		ContextWindow:         1000,
		PreserveRecentTokens:  1,
		RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-agent-isolation", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol(xhigh)",
		Payload: []byte(`{"model":"gpt-5.6-sol(xhigh)"}`),
	}
	options := func(agentID string) cliproxyexecutor.Options {
		headers := http.Header{}
		headers.Set(helps.ClaudeCodeSessionHeader, "11111111-1111-4111-8111-111111111111")
		headers.Set(helps.ClaudeCodeAgentHeader, agentID)
		return cliproxyexecutor.Options{Headers: headers}
	}
	agentABody := []byte(`{"model":"gpt-5.6-sol","instructions":"","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"first question"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"current question"}]}],"tools":[{"type":"function","name":"agent_a_tool","description":"agent a only","parameters":{"type":"object"}}],"stream":true}`)
	agentBBody := []byte(`{"model":"gpt-5.6-sol","instructions":"","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"other branch"}]}],"tools":[{"type":"function","name":"agent_b_tool","description":"agent b only","parameters":{"type":"object"}}],"stream":true}`)

	firstBody, firstScope, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, options("agent-a"), req.Payload, agentABody,
		"gpt-5.6-sol", "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare agent A: %v", err)
	}
	if !active || !strings.Contains(string(firstBody), "opaque-agent-a") {
		t.Fatalf("agent A compaction was not installed: active=%v body=%s", active, firstBody)
	}
	firstScope.observeTerminal([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":11}}}`))

	// Agent B uses the same Claude session, model, and auth but a different tool
	// envelope. It must create its own lane rather than resetting Agent A's
	// compacted replacement.
	cfg.Codex.NativeCompaction.TriggerTokens = 1000
	_, secondScope, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, options("agent-b"), req.Payload, agentBBody,
		"gpt-5.6-sol", "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare agent B: %v", err)
	}
	if active {
		t.Fatal("agent B unexpectedly inherited agent A's compacted replacement")
	}
	secondScope.observeTerminal([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":5}}}`))

	agentAItems, _ := codexInputItems(agentABody)
	followup := codexSetInputItems(agentABody, append(agentAItems, []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"new append"}]}`)))
	followupBody, followupScope, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, options("agent-a"), req.Payload, followup,
		"gpt-5.6-sol", "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare agent A follow-up: %v", err)
	}
	defer followupScope.abandon()
	if !active || !strings.Contains(string(followupBody), "opaque-agent-a") {
		t.Fatalf("agent A replacement was lost after agent B interleaved: active=%v body=%s", active, followupBody)
	}
	mu.Lock()
	gotCompactions := compactionRequests
	mu.Unlock()
	if gotCompactions != 1 {
		t.Fatalf("interleaved agents caused recompaction: requests=%d, want 1", gotCompactions)
	}
}

func TestCodexNativeCompactionFallsBackToLegacyForExplicitCapabilityError(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, append([]byte(nil), body...))
		mu.Unlock()
		if r.URL.Path == "/responses" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"compaction trigger unsupported"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"compact_1","output":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"server-retained"}],"metadata":{"retained":true}},{"type":"compaction","encrypted_content":"legacy-opaque","metadata":{"format":"legacy-v1"}}],"usage":{"input_tokens":17,"output_tokens":4,"total_tokens":21}}`)
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled:               true,
		TriggerTokens:         2,
		ContextWindow:         1000,
		PreserveRecentTokens:  1,
		RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-legacy", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-terra(xhigh)",
		Payload: []byte(`{"metadata":{"user_id":"user_a_account_b_session_feedface-2"}}`),
	}
	body := []byte(`{"model":"gpt-5.6-terra","instructions":"","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old history"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"new question"}]}],"tools":[],"stream":true}`)

	gotBody, _, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, cliproxyexecutor.Options{}, req.Payload, body,
		"gpt-5.6-terra", "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare with legacy fallback: %v", err)
	}
	if !active || !strings.Contains(string(gotBody), "legacy-opaque") {
		t.Fatalf("legacy compaction was not installed: %s", gotBody)
	}
	installed := gjson.GetBytes(gotBody, "input").Array()
	if len(installed) < 3 || installed[0].Get("content.0.text").String() != "server-retained" || !installed[0].Get("metadata.retained").Bool() {
		t.Fatalf("legacy authoritative retained output was not installed byte-for-byte: %s", gotBody)
	}
	if installed[1].Get("type").String() != "compaction" || installed[1].Get("metadata.format").String() != "legacy-v1" {
		t.Fatalf("legacy authoritative compaction metadata was not installed: %s", gotBody)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != "/responses" || paths[1] != "/responses/compact" {
		t.Fatalf("request paths = %#v", paths)
	}
	if gjson.GetBytes(bodies[1], "stream").Exists() {
		t.Fatalf("legacy body retained stream: %s", bodies[1])
	}
	for _, item := range gjson.GetBytes(bodies[1], "input").Array() {
		if item.Get("type").String() == "compaction_trigger" {
			t.Fatalf("legacy body retained compaction_trigger: %s", bodies[1])
		}
	}
}

func TestCodexNativeCompactionUsesPreCompactionTerminalUsage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"exact-threshold\"}}\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":902,\"output_tokens\":2}}}\n")
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled:               true,
		TriggerTokens:         1000,
		ContextWindow:         2000,
		PreserveRecentTokens:  1,
		RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-exact", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{Model: "gpt-5.6-luna(xhigh)", Payload: []byte(`{"metadata":{"user_id":"user_a_account_b_session_abc123-3"}}`)}
	body := []byte(`{"model":"gpt-5.6-luna","instructions":"","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"tools":[],"stream":true}`)

	_, scope, compacted, err := executor.prepareCodexNativeCompaction(context.Background(), auth, req, sdktranslator.FormatClaude, cliproxyexecutor.Options{}, req.Payload, body, "gpt-5.6-luna", "test", server.URL)
	if err != nil || compacted {
		t.Fatalf("pre-threshold prepare compacted=%v err=%v", compacted, err)
	}
	if !scope.active {
		t.Fatal("pre-compaction request did not reserve terminal usage observation")
	}
	scope.observeTerminal([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":700,"output_tokens_details":{"reasoning_tokens":250}}}}`))
	if requests != 0 {
		t.Fatalf("unexpected initial compaction requests = %d", requests)
	}

	cfg.Codex.NativeCompaction.TriggerTokens = 900
	items, _ := codexInputItems(body)
	followup := codexSetInputItems(body, append(items, []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`)))
	got, _, compacted, err := executor.prepareCodexNativeCompaction(context.Background(), auth, req, sdktranslator.FormatClaude, cliproxyexecutor.Options{}, req.Payload, followup, "gpt-5.6-luna", "test", server.URL)
	if err != nil || !compacted || !strings.Contains(string(got), "exact-threshold") {
		t.Fatalf("exact-usage follow-up compacted=%v err=%v body=%s", compacted, err, got)
	}
	if requests != 1 {
		t.Fatalf("compaction requests = %d, want 1", requests)
	}
}

func TestCodexNativeCompactionFallbackRequiresExplicitCapabilityFailure(t *testing.T) {
	if codexShouldFallbackToLegacyCompaction(codexCompactionProtocolError{message: "stream closed before response.completed"}) {
		t.Fatal("transient protocol error must not permanently or immediately downgrade to legacy")
	}
	if codexShouldFallbackToLegacyCompaction(statusErr{code: http.StatusBadRequest, msg: `{"error":{"message":"temporarily malformed stream"}}`}) {
		t.Fatal("generic 400 must not downgrade to legacy")
	}
	if !codexShouldFallbackToLegacyCompaction(statusErr{code: http.StatusBadRequest, msg: `{"error":{"message":"remote_compaction_v2 unsupported"}}`}) {
		t.Fatal("explicit feature capability error should use legacy fallback")
	}
	if !codexShouldFallbackToLegacyCompaction(statusErr{code: http.StatusNotFound, msg: "not found"}) {
		t.Fatal("404 should use legacy fallback")
	}
}

func TestCodexNativeCompactionRetriesTransientV2ProtocolFailure(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	paths := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		attempt := requests
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"truncated"}}`+"\n")
			return
		}
		_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"retried"}}`+"\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":3}}}`+"\n")
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "auth-retry", Attributes: map[string]string{"api_key": "test"}}
	result, err := executor.requestCodexNativeCompaction(
		context.Background(), auth,
		cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(`{"model":"gpt-5.6-sol"}`)},
		sdktranslator.FormatClaude, []byte(`{"model":"gpt-5.6-sol"}`),
		nil,
		[]byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
		"gpt-5.6-sol", "test", server.URL,
	)
	if err != nil {
		t.Fatalf("transient v2 retry: %v", err)
	}
	if result.legacy || len(result.items) != 1 || gjson.GetBytes(result.items[0], "encrypted_content").String() != "retried" {
		t.Fatalf("retry result = %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 || len(paths) != 2 || paths[0] != "/responses" || paths[1] != "/responses" {
		t.Fatalf("requests=%d paths=%v, want two v2 attempts", requests, paths)
	}
}

func TestParseCodexLegacyCompactionPreservesCompleteOutput(t *testing.T) {
	data := []byte(`{"output":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"retained"}]},{"type":"compaction","encrypted_content":"opaque","metadata":{"version":2}}]}`)
	items, err := parseCodexLegacyCompaction(data)
	if err != nil {
		t.Fatalf("parse legacy output: %v", err)
	}
	if len(items) != 2 || gjson.GetBytes(items[0], "content.0.text").String() != "retained" || gjson.GetBytes(items[1], "metadata.version").Int() != 2 {
		t.Fatalf("legacy output was not preserved: %s", codexJSONItems(items))
	}
}

func TestCountCodexInputTokensConservativelyAccountsForOpaqueReasoningAndImages(t *testing.T) {
	enc, err := tokenizerForCodexModel("gpt-5.6-sol")
	if err != nil {
		t.Fatalf("tokenizer: %v", err)
	}
	base := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	baseTokens, err := countCodexInputTokens(enc, base)
	if err != nil {
		t.Fatalf("count base: %v", err)
	}
	withOpaque := []byte(`{"input":[{"type":"reasoning","encrypted_content":"abcdefghijklmnopqrstuvwxyz0123456789"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_image","image_url":"https://example.test/image.png"}]}]}`)
	opaqueTokens, err := countCodexInputTokens(enc, withOpaque)
	if err != nil {
		t.Fatalf("count opaque: %v", err)
	}
	if opaqueTokens < baseTokens+8_192+18 {
		t.Fatalf("opaque/image count = %d, want at least %d", opaqueTokens, baseTokens+8_192+18)
	}
}

func TestCodexNativeCompactionParserRequiresOneCompletedItem(t *testing.T) {
	valid := []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":2}}}\n")
	item, input, output, err := parseCodexRemoteCompactionV2(valid)
	if err != nil || gjson.GetBytes(item, "encrypted_content").String() != "opaque" || input != 8 || output != 2 {
		t.Fatalf("valid parse = item:%s input:%d output:%d err:%v", item, input, output, err)
	}
	if _, _, _, err = parseCodexRemoteCompactionV2([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n")); err == nil {
		t.Fatal("missing response.completed unexpectedly succeeded")
	}
	duplicate := append(append([]byte(nil), valid...), []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"second\"}}\n")...)
	if _, _, _, err = parseCodexRemoteCompactionV2(duplicate); err == nil {
		t.Fatal("duplicate compaction items unexpectedly succeeded")
	}
}

func TestCodexCompactionCutDoesNotSplitToolPair(t *testing.T) {
	items := [][]byte{
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"run"}]}`),
		[]byte(`{"type":"function_call","call_id":"call-1","name":"tool","arguments":"{}"}`),
		[]byte(`{"type":"function_call_output","call_id":"call-1","output":"ok"}`),
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`),
	}
	if got := codexAdjustCompactionCutForToolPairs(items, 2); got != 1 {
		t.Fatalf("adjusted cut = %d, want 1", got)
	}
	customItems := [][]byte{
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"run"}]}`),
		[]byte(`{"type":"custom_tool_call","call_id":"custom-1","name":"tool","input":"{}"}`),
		[]byte(`{"type":"custom_tool_call_output","call_id":"custom-1","output":"ok"}`),
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`),
	}
	if got := codexAdjustCompactionCutForToolPairs(customItems, 2); got != 1 {
		t.Fatalf("custom-tool adjusted cut = %d, want 1", got)
	}
	parallelItems := [][]byte{
		[]byte(`{"type":"function_call","call_id":"call-1","name":"first","arguments":"{}"}`),
		[]byte(`{"type":"function_call","call_id":"call-2","name":"second","arguments":"{}"}`),
		[]byte(`{"type":"function_call_output","call_id":"call-1","output":"one"}`),
		[]byte(`{"type":"function_call_output","call_id":"call-2","output":"two"}`),
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`),
	}
	if got := codexAdjustCompactionCutForToolPairs(parallelItems, 3); got != 0 {
		t.Fatalf("transitive parallel-tool adjusted cut = %d, want 0", got)
	}
	parallelCustomItems := [][]byte{
		[]byte(`{"type":"custom_tool_call","call_id":"custom-1","name":"first","input":"{}"}`),
		[]byte(`{"type":"custom_tool_call","call_id":"custom-2","name":"second","input":"{}"}`),
		[]byte(`{"type":"custom_tool_call_output","call_id":"custom-1","output":"one"}`),
		[]byte(`{"type":"custom_tool_call_output","call_id":"custom-2","output":"two"}`),
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}`),
	}
	if got := codexAdjustCompactionCutForToolPairs(parallelCustomItems, 3); got != 0 {
		t.Fatalf("transitive parallel custom-tool adjusted cut = %d, want 0", got)
	}
}

func TestCodexNativeCompactionIncludesReasoningReplayExactlyOnce(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	var mu sync.Mutex
	var compactionBodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		compactionBodies = append(compactionBodies, append([]byte(nil), body...))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"replay-summary"}}`+"\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":40,"output_tokens":3}}}`+"\n")
	}))
	defer server.Close()

	const sessionID = "native-replay-session"
	model := "gpt-5.6-sol"
	firstReasoning := validCodexReasoningEncryptedContentForTestSeed(31)
	replayKey := claudeReplayKeyForTest("claude:"+sessionID, nil)
	internalcache.CacheCodexReasoningReplayItem(model, replayKey, []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+firstReasoning+`"}`))

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled:               true,
		TriggerTokens:         3,
		ContextWindow:         1000,
		PreserveRecentTokens:  1,
		RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-replay-compaction", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{
		Model:   model + "(xhigh)",
		Payload: []byte(`{"model":"gpt-5.6-sol(xhigh)","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"` + sessionID + `\"}"}}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	body := []byte(`{"model":"gpt-5.6-sol","instructions":"","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old question"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"current question"}]}],"tools":[],"stream":true}`)

	firstBody, firstScope, compacted, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, opts, req.Payload, body,
		model, "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare with reasoning replay: %v", err)
	}
	if !compacted || !firstScope.replayApplied || !firstScope.replayScope.valid() {
		t.Fatalf("scope compacted=%v replayApplied=%v replayScope=%+v", compacted, firstScope.replayApplied, firstScope.replayScope)
	}
	firstScope.abandon()

	mu.Lock()
	if len(compactionBodies) != 1 {
		mu.Unlock()
		t.Fatalf("compaction requests = %d, want 1", len(compactionBodies))
	}
	compactRequest := append([]byte(nil), compactionBodies[0]...)
	mu.Unlock()
	if got := countCodexInputItemsOfType(compactRequest, "reasoning"); got != 1 {
		t.Fatalf("compaction reasoning count = %d, want 1; body=%s", got, compactRequest)
	}
	if got := gjson.GetBytes(compactRequest, "input.#(type==\"reasoning\").encrypted_content").String(); got != firstReasoning {
		t.Fatalf("compaction replay signature = %q, want cached reasoning; body=%s", got, compactRequest)
	}
	if got := countCodexInputItemsOfType(firstBody, "reasoning"); got != 0 {
		t.Fatalf("generation retained replay outside the compacted summary: count=%d body=%s", got, firstBody)
	}

	cfg.Codex.NativeCompaction.TriggerTokens = 1000
	items, _ := codexInputItems(body)
	followup := codexSetInputItems(body, append(items,
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"current answer"}]}`),
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"next question"}]}`),
	))

	// If generation never completed, the cache still contains the item already
	// absorbed by compaction. The durable marker must suppress that stale replay.
	retryBody, retryScope, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, opts, req.Payload, followup,
		model, "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare active lane after abandoned generation: %v", err)
	}
	if !active || !retryScope.replayApplied {
		t.Fatalf("retry active=%v replayApplied=%v", active, retryScope.replayApplied)
	}
	retryScope.abandon()
	if got := countCodexInputItemsOfType(retryBody, "reasoning"); got != 0 {
		t.Fatalf("absorbed replay was injected again: count=%d body=%s", got, retryBody)
	}

	// Once completion advances the replay cache, the active lane injects the new
	// reasoning into the exact client tail without duplicating it.
	secondReasoning := validCodexReasoningEncryptedContentForTestSeed(32)
	internalcache.CacheCodexReasoningReplayItem(model, replayKey, []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+secondReasoning+`"}`))
	secondBody, secondScope, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude, opts, req.Payload, followup,
		model, "test", server.URL,
	)
	if err != nil {
		t.Fatalf("prepare active lane with reasoning replay: %v", err)
	}
	if !active || !secondScope.replayApplied {
		t.Fatalf("active=%v replayApplied=%v", active, secondScope.replayApplied)
	}
	secondScope.abandon()
	if got := countCodexInputItemsOfType(secondBody, "reasoning"); got != 1 {
		t.Fatalf("follow-up reasoning count = %d, want 1; body=%s", got, secondBody)
	}
	if got := gjson.GetBytes(secondBody, "input.#(type==\"reasoning\").encrypted_content").String(); got != secondReasoning {
		t.Fatalf("follow-up replay signature = %q, want newest reasoning; body=%s", got, secondBody)
	}
	mu.Lock()
	requestCount := len(compactionBodies)
	mu.Unlock()
	if requestCount != 1 {
		t.Fatalf("active-lane follow-up unexpectedly recompacted; requests=%d", requestCount)
	}
}

func TestCodexNativeCompactionSuppressesRejectedReasoningReplay(t *testing.T) {
	for _, transport := range []string{"http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			internalcache.ClearCodexReasoningReplayCache()
			t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

			var mu sync.Mutex
			attempts := 0
			var bodies [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				attempts++
				attempt := attempts
				bodies = append(bodies, append([]byte(nil), body...))
				mu.Unlock()
				if attempt == 1 {
					if transport == "http" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"invalid_encrypted_content"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, `data: {"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"invalid encrypted content"}}}`+"\n")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"recovered-summary"}}`+"\n")
				_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":3}}}`+"\n")
			}))
			defer server.Close()

			const sessionID = "rejected-replay-session"
			model := "gpt-5.6-luna"
			reasoning := validCodexReasoningEncryptedContentForTestSeed(41)
			replayKey := claudeReplayKeyForTest("claude:"+sessionID, nil)
			internalcache.CacheCodexReasoningReplayItem(model, replayKey, []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+reasoning+`"}`))
			cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
				Enabled:               true,
				TriggerTokens:         3,
				ContextWindow:         4,
				PreserveRecentTokens:  1,
				RetainedMessageTokens: 64,
			}}}
			executor := NewCodexExecutor(cfg)
			auth := &cliproxyauth.Auth{ID: "auth-rejected-" + transport, Attributes: map[string]string{"api_key": "test"}}
			req := cliproxyexecutor.Request{
				Model:   model + "(xhigh)",
				Payload: []byte(`{"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"` + sessionID + `\"}"}}`),
			}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			body := []byte(`{"model":"gpt-5.6-luna","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"current"}]}],"stream":true}`)

			recoveredBody, scope, compacted, firstErr := executor.prepareCodexNativeCompaction(
				context.Background(), auth, req, sdktranslator.FormatClaude, opts, req.Payload, body,
				model, "test", server.URL,
			)
			if firstErr != nil || !compacted {
				t.Fatalf("same-request recovery compacted=%v err=%v body=%s", compacted, firstErr, recoveredBody)
			}
			scope.abandon()
			mu.Lock()
			firstAttempts := attempts
			requestBodies := codexCloneItems(bodies)
			mu.Unlock()
			if firstAttempts != 2 {
				t.Fatalf("compaction attempts = %d, want one rejection plus one recovery", firstAttempts)
			}
			if _, ok := internalcache.GetCodexReasoningReplayItem(model, replayKey); !ok {
				t.Fatal("per-auth rejection unexpectedly destroyed the shared replay cache")
			}
			if got := countCodexInputItemsOfType(recoveredBody, "reasoning"); got != 0 {
				t.Fatalf("recovered generation still included rejected replay: count=%d body=%s", got, recoveredBody)
			}
			if len(requestBodies) != 2 || countCodexInputItemsOfType(requestBodies[0], "reasoning") != 1 || countCodexInputItemsOfType(requestBodies[1], "reasoning") != 0 {
				t.Fatalf("recovery request did not remove only the rejected replay: first=%s second=%s", requestBodies[0], requestBodies[1])
			}

			// A racing completion may repopulate the same shared cache entry. The
			// per-auth durable tombstone must continue to suppress it without
			// deleting continuity for another credential.
			cfg.Codex.NativeCompaction.TriggerTokens = 1000
			cfg.Codex.NativeCompaction.ContextWindow = 10_000
			clientItems, _ := codexInputItems(body)
			followup := codexSetInputItems(body, append(clientItems,
				[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"current answer"}]}`),
				[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`),
			))
			followupBody, followupScope, active, followupErr := executor.prepareCodexNativeCompaction(
				context.Background(), auth, req, sdktranslator.FormatClaude, opts, req.Payload, followup,
				model, "test", server.URL,
			)
			if followupErr != nil || !active {
				t.Fatalf("follow-up after rejection active=%v err=%v body=%s", active, followupErr, followupBody)
			}
			followupScope.abandon()
			if countCodexInputItemsOfType(followupBody, "reasoning") != 0 {
				t.Fatalf("persisted rejection did not suppress repopulated replay: %s", followupBody)
			}
		})
	}
}

func TestCodexNativeCompactionRecoveryPreservesReplayToolCall(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	const sessionID = "rejected-composite-replay-session"
	model := "gpt-5.6-terra"
	reasoning := validCodexReasoningEncryptedContentForTestSeed(42)
	replayItems := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + reasoning + `"}`),
		[]byte(`{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"weather\"}"}`),
	}
	replayKey := claudeReplayKeyForTest("claude:"+sessionID, nil)
	if !internalcache.CacheCodexReasoningReplayItems(model, replayKey, replayItems) {
		t.Fatal("failed to cache composite reasoning replay")
	}

	var mu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		attempt := len(bodies)
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"invalid encrypted content"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"composite-recovered"}}`+"\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":3}}}`+"\n")
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled: true, TriggerTokens: 3, ContextWindow: 1000, PreserveRecentTokens: 1, RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-rejected-composite", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{
		Model:   model + "(xhigh)",
		Payload: []byte(`{"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"` + sessionID + `\"}"}}`),
	}
	body := []byte(`{"model":"gpt-5.6-terra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"function_call_output","call_id":"call-1","output":"sunny"},{"type":"message","role":"user","content":[{"type":"input_text","text":"current"}]}],"stream":true}`)

	gotBody, scope, compacted, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude,
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, req.Payload, body,
		model, "test", server.URL,
	)
	if err != nil || !compacted {
		t.Fatalf("composite recovery compacted=%v err=%v body=%s", compacted, err, gotBody)
	}
	scope.abandon()
	mu.Lock()
	requestBodies := codexCloneItems(bodies)
	mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("compaction requests = %d, want 2", len(requestBodies))
	}
	if countCodexInputItemsOfType(requestBodies[1], "reasoning") != 0 {
		t.Fatalf("recovery retained rejected reasoning: %s", requestBodies[1])
	}
	input := gjson.GetBytes(requestBodies[1], "input").Array()
	callIndex, outputIndex := -1, -1
	for i, item := range input {
		switch item.Get("type").String() {
		case "function_call":
			if item.Get("call_id").String() == "call-1" {
				callIndex = i
			}
		case "function_call_output":
			if item.Get("call_id").String() == "call-1" {
				outputIndex = i
			}
		}
	}
	if callIndex < 0 || outputIndex < 0 || callIndex >= outputIndex {
		t.Fatalf("recovery orphaned tool output: call=%d output=%d body=%s", callIndex, outputIndex, requestBodies[1])
	}
	cachedItems, ok := internalcache.GetCodexReasoningReplayItems(model, replayKey)
	if !ok || len(cachedItems) != 2 || gjson.GetBytes(cachedItems[0], "type").String() != "reasoning" || gjson.GetBytes(cachedItems[1], "type").String() != "function_call" {
		t.Fatalf("shared replay cache = %s, want auth-independent reasoning and function call", codexJSONItems(cachedItems))
	}
}

func TestCodexNativeCompactionRejectedSummaryClearsAbsorbedReplayMarkers(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	const sessionID = "rejected-summary-absorbed-replay"
	model := "gpt-5.6-terra"
	if !internalcache.CacheCodexReasoningReplayItems(model, claudeReplayKeyForTest("claude:"+sessionID, nil), [][]byte{
		[]byte(`{"type":"function_call","call_id":"call-absorbed","name":"lookup","arguments":"{}"}`),
	}) {
		t.Fatal("failed to cache tool replay")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"later-rejected"}}`+"\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":3}}}`+"\n")
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled: true, TriggerTokens: 3, ContextWindow: 1000, PreserveRecentTokens: 1, RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-rejected-summary", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{
		Model:   model + "(xhigh)",
		Payload: []byte(`{"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"` + sessionID + `\"}"}}`),
	}
	body := []byte(`{"model":"gpt-5.6-terra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"function_call_output","call_id":"call-absorbed","output":"sunny"},{"type":"message","role":"user","content":[{"type":"input_text","text":"current"}]}],"stream":true}`)

	compactedBody, firstScope, compacted, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude,
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, req.Payload, body,
		model, "test", server.URL,
	)
	if err != nil || !compacted {
		t.Fatalf("initial compaction compacted=%v err=%v body=%s", compacted, err, compactedBody)
	}
	replaced, err := firstScope.rejectEncryptedState(compactedBody, false)
	firstScope.abandon()
	if err != nil || !replaced {
		t.Fatalf("retire rejected summary replaced=%v err=%v", replaced, err)
	}

	cfg.Codex.NativeCompaction.TriggerTokens = 1000
	cfg.Codex.NativeCompaction.ContextWindow = 10_000
	clientItems, _ := codexInputItems(body)
	followup := codexSetInputItems(body, append(clientItems,
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"current answer"}]}`),
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}`),
	))
	rebuiltBody, rebuiltScope, active, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude,
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, req.Payload, followup,
		model, "test", server.URL,
	)
	if err != nil || active {
		t.Fatalf("raw rebuild active=%v err=%v body=%s", active, err, rebuiltBody)
	}
	rebuiltScope.abandon()
	input := gjson.GetBytes(rebuiltBody, "input").Array()
	callIndex, outputIndex := -1, -1
	for i, item := range input {
		if item.Get("type").String() == "function_call" && item.Get("call_id").String() == "call-absorbed" {
			callIndex = i
		}
		if item.Get("type").String() == "function_call_output" && item.Get("call_id").String() == "call-absorbed" {
			outputIndex = i
		}
	}
	if callIndex < 0 || outputIndex < 0 || callIndex >= outputIndex {
		t.Fatalf("raw rebuild suppressed a call that existed only in the rejected summary: call=%d output=%d body=%s", callIndex, outputIndex, rebuiltBody)
	}
}

func TestCodexNativeCompactionSecondRejectionStillPreservesReplayToolCall(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	const sessionID = "twice-rejected-composite-session"
	model := "gpt-5.6-terra"
	reasoning := validCodexReasoningEncryptedContentForTestSeed(45)
	if !internalcache.CacheCodexReasoningReplayItems(model, claudeReplayKeyForTest("claude:"+sessionID, nil), [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + reasoning + `"}`),
		[]byte(`{"type":"function_call","call_id":"call-2","name":"lookup","arguments":"{}"}`),
	}) {
		t.Fatal("failed to cache composite replay")
	}

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"invalid encrypted content"}}`)
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled: true, TriggerTokens: 3, ContextWindow: 1000, PreserveRecentTokens: 1, RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-twice-rejected", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{
		Model:   model + "(xhigh)",
		Payload: []byte(`{"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"` + sessionID + `\"}"}}`),
	}
	body := []byte(`{"model":"gpt-5.6-terra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"function_call_output","call_id":"call-2","output":"sunny"},{"type":"message","role":"user","content":[{"type":"input_text","text":"current"}]}],"stream":true}`)

	gotBody, scope, compacted, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude,
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, req.Payload, body,
		model, "test", server.URL,
	)
	if err != nil || compacted {
		t.Fatalf("second rejection compacted=%v err=%v body=%s", compacted, err, gotBody)
	}
	scope.abandon()
	if attempts != 2 {
		t.Fatalf("compaction attempts = %d, want initial plus one recovery", attempts)
	}
	if countCodexInputItemsOfType(gotBody, "reasoning") != 0 {
		t.Fatalf("fallback generation retained rejected reasoning: %s", gotBody)
	}
	input := gjson.GetBytes(gotBody, "input").Array()
	callIndex, outputIndex := -1, -1
	for i, item := range input {
		if item.Get("type").String() == "function_call" && item.Get("call_id").String() == "call-2" {
			callIndex = i
		}
		if item.Get("type").String() == "function_call_output" && item.Get("call_id").String() == "call-2" {
			outputIndex = i
		}
	}
	if callIndex < 0 || outputIndex < 0 || callIndex >= outputIndex {
		t.Fatalf("fallback generation orphaned tool output: call=%d output=%d body=%s", callIndex, outputIndex, gotBody)
	}
}

func TestCodexNativeCompactionRecoveryPreservesUnsentClientReasoningTail(t *testing.T) {
	firstReasoning := validCodexReasoningEncryptedContentForTestSeed(43)
	secondReasoning := validCodexReasoningEncryptedContentForTestSeed(44)
	items := [][]byte{
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]}`),
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + firstReasoning + `"}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`),
		[]byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + secondReasoning + `"}`),
		[]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"current"}]}`),
	}
	enc, err := tokenizerForCodexModel("gpt-5.6-luna")
	if err != nil {
		t.Fatalf("tokenizer: %v", err)
	}
	preserveTail := codexItemTokens(enc, items[3]) + codexItemTokens(enc, items[4])

	var mu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		attempt := len(bodies)
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"invalid encrypted content"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"client-recovered"}}`+"\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":3}}}`+"\n")
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled: true, TriggerTokens: 3, ContextWindow: 1000, PreserveRecentTokens: preserveTail, RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-client-reasoning", Attributes: map[string]string{"api_key": "test"}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna(xhigh)",
		Payload: []byte(`{"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"client-tail-session\"}"}}`),
	}
	body := codexSetInputItems([]byte(`{"model":"gpt-5.6-luna","input":[],"stream":true}`), items)

	gotBody, scope, compacted, err := executor.prepareCodexNativeCompaction(
		context.Background(), auth, req, sdktranslator.FormatClaude,
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, req.Payload, body,
		"gpt-5.6-luna", "test", server.URL,
	)
	if err != nil || !compacted {
		t.Fatalf("client reasoning recovery compacted=%v err=%v body=%s", compacted, err, gotBody)
	}
	scope.abandon()
	if strings.Contains(string(gotBody), firstReasoning) {
		t.Fatalf("recovered generation retained rejected prefix reasoning: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), secondReasoning) {
		t.Fatalf("recovered generation removed unsent tail reasoning: %s", gotBody)
	}
	mu.Lock()
	requestBodies := codexCloneItems(bodies)
	mu.Unlock()
	if len(requestBodies) != 2 || !strings.Contains(string(requestBodies[0]), firstReasoning) || strings.Contains(string(requestBodies[0]), secondReasoning) {
		t.Fatalf("failed prefix did not isolate the first reasoning item: %s", codexJSONItems(requestBodies))
	}
}

func TestCodexExecutorRetriesRejectedDurableCompactionFromClientHistory(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)
	validTailReasoning := validCodexReasoningEncryptedContentForTestSeed(46)
	if !internalcache.CacheCodexReasoningReplayItem("gpt-5.6-sol", claudeReplayKeyForTest("claude:durable-recovery-session", nil), []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+validTailReasoning+`"}`)) {
		t.Fatal("cache valid reasoning for durable-summary recovery")
	}

	var mu sync.Mutex
	var compactionBodies, generationBodies [][]byte
	compactions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		input := gjson.GetBytes(body, "input").Array()
		isCompaction := len(input) > 0 && input[len(input)-1].Get("type").String() == "compaction_trigger"
		mu.Lock()
		if isCompaction {
			compactions++
			compactionBodies = append(compactionBodies, append([]byte(nil), body...))
		} else {
			generationBodies = append(generationBodies, append([]byte(nil), body...))
		}
		compactionAttempt := compactions
		mu.Unlock()

		if isCompaction {
			summary := "bad-durable-summary"
			if compactionAttempt > 1 {
				summary = "recovered-durable-summary"
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"`+summary+`"}}`+"\n")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":3}}}`+"\n")
			return
		}
		if strings.Contains(string(body), "bad-durable-summary") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"invalid encrypted content"}}}`+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_recovered","object":"response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":1}}}`+"\n\n")
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled: true, TriggerTokens: 3, ContextWindow: 1000, PreserveRecentTokens: 1, RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-rejected-durable", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	usageCollector := newCodexRetryUsageCollector(auth.ID)
	cliproxyusage.RegisterNamedPlugin("test-codex-rejected-durable-retry", usageCollector)
	req := cliproxyexecutor.Request{
		Model: "gpt-5.6-sol(xhigh)",
		Payload: []byte(`{"model":"gpt-5.6-sol(xhigh)","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"durable-recovery-session\"}"},"messages":[` +
			`{"role":"user","content":[{"type":"text","text":"old question"}]},` +
			`{"role":"assistant","content":[{"type":"text","text":"old answer"}]},` +
			`{"role":"user","content":[{"type":"text","text":"current question"}]}]}`),
	}
	_, err := executor.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("Execute did not recover rejected durable compaction: %v", err)
	}
	mu.Lock()
	compactRequests := codexCloneItems(compactionBodies)
	generationRequests := codexCloneItems(generationBodies)
	mu.Unlock()
	if len(compactRequests) != 2 || len(generationRequests) != 2 {
		t.Fatalf("requests compaction=%d generation=%d, want 2 each", len(compactRequests), len(generationRequests))
	}
	if !strings.Contains(string(generationRequests[0]), "bad-durable-summary") || strings.Contains(string(compactRequests[1]), "bad-durable-summary") {
		t.Fatalf("retry did not rebuild from raw client history: first-generation=%s retry-compaction=%s", generationRequests[0], compactRequests[1])
	}
	if !strings.Contains(string(compactRequests[1]), validTailReasoning) {
		t.Fatalf("durable-summary rejection blacklisted unrelated valid reasoning: %s", compactRequests[1])
	}
	if !strings.Contains(string(generationRequests[1]), "recovered-durable-summary") {
		t.Fatalf("retry did not install recovered durable summary: %s", generationRequests[1])
	}
	records := usageCollector.waitFor(t, 2)
	if !records[0].Failed || records[1].Failed {
		t.Fatalf("generation retry usage outcomes = [%v, %v], want [failed, succeeded]", records[0].Failed, records[1].Failed)
	}
	usageCollector.assertNoAdditional(t)
}

func TestCodexExecutorRecoversWhenPreservedReasoningNotActiveSummaryIsRejected(t *testing.T) {
	const sessionID = "valid-summary-bad-tail-session"
	const model = "gpt-5.6-sol"
	badTailReasoning := validCodexReasoningEncryptedContentForTestSeed(47)
	payload := []byte(`{"model":"gpt-5.6-sol(xhigh)","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"` + sessionID + `\"}"},"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"old question"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"old answer"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"provider state","signature":"` + badTailReasoning + `"},{"type":"text","text":"recent answer"}]},` +
		`{"role":"user","content":[{"type":"text","text":"current question"}]}]}`)
	req := cliproxyexecutor.Request{Model: model + "(xhigh)", Payload: payload}

	_, translated := translateCodexRequestPair(
		sdktranslator.FormatClaude,
		sdktranslator.FromString("codex"),
		model,
		payload,
		payload,
		false,
	)
	items, ok := codexInputItems(translated)
	if !ok {
		t.Fatalf("translated request has no Codex input: %s", translated)
	}
	reasoningIndex := -1
	for i, item := range items {
		if gjson.GetBytes(item, "type").String() == "reasoning" {
			reasoningIndex = i
		}
	}
	if reasoningIndex <= 0 {
		t.Fatalf("translated request did not retain signed reasoning: %s", translated)
	}
	enc, err := tokenizerForCodexModel(model)
	if err != nil {
		t.Fatalf("tokenizer: %v", err)
	}
	var preserveTail int64
	for _, item := range items[reasoningIndex:] {
		preserveTail += codexItemTokens(enc, item)
	}

	var mu sync.Mutex
	var compactionBodies, generationBodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		input := gjson.GetBytes(body, "input").Array()
		isCompaction := len(input) > 0 && input[len(input)-1].Get("type").String() == "compaction_trigger"
		mu.Lock()
		if isCompaction {
			compactionBodies = append(compactionBodies, append([]byte(nil), body...))
		} else {
			generationBodies = append(generationBodies, append([]byte(nil), body...))
		}
		generationAttempt := len(generationBodies)
		mu.Unlock()

		if isCompaction {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"type":"response.output_item.done","item":{"type":"compaction","encrypted_content":"valid-active-summary"}}`+"\n")
			_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"usage":{"input_tokens":20,"output_tokens":3}}}`+"\n")
			return
		}
		if generationAttempt == 2 || generationAttempt == 3 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"invalid encrypted content"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_ok","object":"response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":1}}}`+"\n\n")
	}))
	defer server.Close()

	cfg := &config.Config{AuthDir: t.TempDir(), Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{
		Enabled: true, TriggerTokens: 3, ContextWindow: 200_000, PreserveRecentTokens: preserveTail, RetainedMessageTokens: 64,
	}}}
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "auth-valid-summary-bad-tail", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	if _, err = executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("seed active compaction: %v", err)
	}
	// Keep the active replacement but prevent the follow-up and its retries from
	// creating a new summary. This isolates the summary-vs-tail ambiguity.
	cfg.Codex.NativeCompaction.TriggerTokens = 100_000
	if _, err = executor.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("same request did not recover summary then reasoning ambiguity: %v", err)
	}

	mu.Lock()
	compactRequests := codexCloneItems(compactionBodies)
	generationRequests := codexCloneItems(generationBodies)
	mu.Unlock()
	if len(compactRequests) != 1 || len(generationRequests) != 4 {
		t.Fatalf("requests compaction=%d generation=%d, want 1 seed compaction and 4 generations", len(compactRequests), len(generationRequests))
	}
	if !strings.Contains(string(generationRequests[1]), "valid-active-summary") || !strings.Contains(string(generationRequests[1]), badTailReasoning) {
		t.Fatalf("first rejected request did not contain both suspects: %s", generationRequests[1])
	}
	if strings.Contains(string(generationRequests[2]), "valid-active-summary") || !strings.Contains(string(generationRequests[2]), badTailReasoning) {
		t.Fatalf("first retry did not retire only the summary: %s", generationRequests[2])
	}
	if strings.Contains(string(generationRequests[3]), "valid-active-summary") || strings.Contains(string(generationRequests[3]), badTailReasoning) {
		t.Fatalf("second retry did not retire the rejected reasoning tail: %s", generationRequests[3])
	}
}

func TestCodexExecutorHTTPInvalidRetryFailurePublishesOneRecordPerAttempt(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"invalid encrypted content"}}`)
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "auth-http-invalid-twice", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	usageCollector := newCodexRetryUsageCollector(auth.ID)
	cliproxyusage.RegisterNamedPlugin("test-codex-http-invalid-twice", usageCollector)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna(xhigh)",
		Payload: []byte(`{"model":"gpt-5.6-luna(xhigh)","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"http-invalid-twice\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}
	if _, err := executor.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err == nil {
		t.Fatal("two invalid generation attempts unexpectedly succeeded")
	}
	if requests != 2 {
		t.Fatalf("generation requests = %d, want exactly 2", requests)
	}
	records := usageCollector.waitFor(t, 2)
	if !records[0].Failed || !records[1].Failed {
		t.Fatalf("generation retry usage outcomes = [%v, %v], want two failures", records[0].Failed, records[1].Failed)
	}
	usageCollector.assertNoAdditional(t)
}

type codexRetryUsageCollector struct {
	authID  string
	records chan cliproxyusage.Record
}

func newCodexRetryUsageCollector(authID string) *codexRetryUsageCollector {
	return &codexRetryUsageCollector{authID: authID, records: make(chan cliproxyusage.Record, 8)}
}

func (c *codexRetryUsageCollector) HandleUsage(_ context.Context, record cliproxyusage.Record) {
	if c == nil || record.AuthID != c.authID || record.Operation == "compaction" {
		return
	}
	c.records <- record
}

func (c *codexRetryUsageCollector) waitFor(t *testing.T, count int) []cliproxyusage.Record {
	t.Helper()
	records := make([]cliproxyusage.Record, 0, count)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for len(records) < count {
		select {
		case record := <-c.records:
			records = append(records, record)
		case <-deadline.C:
			t.Fatalf("usage records = %d, want %d", len(records), count)
		}
	}
	return records
}

func (c *codexRetryUsageCollector) assertNoAdditional(t *testing.T) {
	t.Helper()
	select {
	case record := <-c.records:
		t.Fatalf("unexpected duplicate usage record: failed=%v operation=%q", record.Failed, record.Operation)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCodexInsertedItemSpanMapsCutsBackToClientHistory(t *testing.T) {
	base := [][]byte{[]byte(`{"id":1}`), []byte(`{"id":2}`), []byte(`{"id":3}`)}
	transformed := [][]byte{base[0], []byte(`{"type":"reasoning"}`), []byte(`{"type":"function_call"}`), base[1], base[2]}
	inserted, ok := codexInsertedItemSpan(base, transformed)
	if !ok || inserted.start != 1 || inserted.count != 2 {
		t.Fatalf("inserted span = %+v ok=%v", inserted, ok)
	}
	if got := codexAdjustCompactionCutForInsertedItems(2, inserted); got != 1 {
		t.Fatalf("cut through replay = %d, want 1", got)
	}
	if got := codexBaseItemCut(4, inserted); got != 2 {
		t.Fatalf("base cut = %d, want 2", got)
	}
}

func countCodexInputItemsOfType(body []byte, itemType string) int {
	count := 0
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == itemType {
			count++
		}
	}
	return count
}

func TestCodexNativeCompactionDefaultSettings(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{NativeCompaction: config.CodexNativeCompaction{Enabled: true}}})
	settings, enabled := executor.nativeCompactionSettings()
	if !enabled || settings.triggerTokens != 240_000 || settings.contextWindow != 272_000 || settings.preserveRecentTokens != 32_000 || settings.retainedMessageTokens != 64_000 {
		t.Fatalf("unexpected defaults: enabled=%v settings=%+v", enabled, settings)
	}
}
