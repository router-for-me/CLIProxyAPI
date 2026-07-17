package executor

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func validCodexReasoningEncryptedContentForTestSeed(seed byte) string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = seed + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func shortenedCodexReplayCallIDForTest(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLen := limit - len(suffix)
	if prefixLen <= 0 {
		return suffix[len(suffix)-limit:]
	}
	return id[:prefixLen] + suffix
}

func TestCodexExecutorReasoningReplayCacheStoresFinalDoneAndInjectsNextClaudeRequest(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	addedEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(1)
	doneEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(2)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","item":{"id":"rs_added","type":"reasoning","status":"in_progress","summary":[],"encrypted_content":"` + addedEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_done","type":"reasoning","summary":[],"encrypted_content":"` + doneEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "auth-replay-1",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-1\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-1\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(bodies))
	}
	secondBody := bodies[1]
	if got := gjson.GetBytes(secondBody, "input.0.type").String(); got != "reasoning" {
		t.Fatalf("input.0.type = %q, want reasoning; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.0.encrypted_content").String(); got != doneEncryptedContent {
		t.Fatalf("injected encrypted_content = %q, want final done %q; body=%s", got, doneEncryptedContent, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.1.role").String(); got != "user" {
		t.Fatalf("input.1.role = %q, want user; body=%s", got, string(secondBody))
	}
}

func TestCodexExecutorReasoningReplayCacheSharesSameSessionAcrossClientKeys(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	from := sdktranslator.FromString("claude")
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-only\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: from}
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`)
	encryptedContent := validCodexReasoningEncryptedContentForTestSeed(11)

	firstScope := codexReasoningReplayScopeFromRequest(codexReplaySessionOnlyContext("client-key-a"), from, req, opts, body)
	if !firstScope.valid() {
		t.Fatalf("first replay scope is invalid: %#v", firstScope)
	}
	cacheCodexReasoningReplayFromCompleted(firstScope, []byte(`{"response":{"output":[{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"}]}}`))

	secondBody, secondScope := applyCodexReasoningReplayCache(codexReplaySessionOnlyContext("client-key-b"), from, req, opts, body)
	if secondScope != firstScope {
		t.Fatalf("replay scope should ignore client API key for the same session: first=%#v second=%#v", firstScope, secondScope)
	}
	if got := gjson.GetBytes(secondBody, "input.0.type").String(); got != "reasoning" {
		t.Fatalf("input.0.type = %q, want same-session replay; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.0.encrypted_content").String(); got != encryptedContent {
		t.Fatalf("injected encrypted_content = %q, want cached value", got)
	}
}

func TestCodexExecutorReasoningReplaySessionKeyUsesClaudeCodeJSONSessionID(t *testing.T) {
	from := sdktranslator.FromString("claude")
	req := cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-a\",\"account_uuid\":\"\",\"session_id\":\"session-json-1\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]
		}`),
	}
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`)

	got := codexReasoningReplaySessionKey(context.Background(), from, req, cliproxyexecutor.Options{SourceFormat: from}, body)
	if got != "claude:session-json-1:agent:main" {
		t.Fatalf("codexReasoningReplaySessionKey() = %q, want claude:session-json-1:agent:main", got)
	}
}

func TestCodexExecutorReasoningReplaySessionKeyIsolatesClaudeCodeAgents(t *testing.T) {
	from := sdktranslator.FromString("claude")
	req := cliproxyexecutor.Request{
		Model:   "local-alias-high",
		Payload: []byte(`{"model":"local-alias","messages":[{"role":"user","content":"next"}]}`),
	}
	body := []byte(`{"model":"gpt-5.4","prompt_cache_key":"shared-client-key","input":[{"type":"message","role":"user","content":"next"}]}`)
	rootHeaders := http.Header{}
	rootHeaders.Set("X-Claude-Code-Session-Id", "session-agents")
	childAHeaders := rootHeaders.Clone()
	childAHeaders.Set("X-Claude-Code-Agent-Id", "agent-a")
	childBHeaders := rootHeaders.Clone()
	childBHeaders.Set("X-Claude-Code-Agent-Id", "agent-b")

	metadata := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "shared-execution-session"}
	root := codexReasoningReplayScopeFromRequest(context.Background(), from, req, cliproxyexecutor.Options{SourceFormat: from, Headers: rootHeaders, Metadata: metadata}, body)
	childA := codexReasoningReplayScopeFromRequest(context.Background(), from, req, cliproxyexecutor.Options{SourceFormat: from, Headers: childAHeaders, Metadata: metadata}, body)
	childB := codexReasoningReplayScopeFromRequest(context.Background(), from, req, cliproxyexecutor.Options{SourceFormat: from, Headers: childBHeaders, Metadata: metadata}, body)
	if root.modelName != "gpt-5.4" || childA.modelName != "gpt-5.4" || childB.modelName != "gpt-5.4" {
		t.Fatalf("replay scopes did not use resolved model: root=%#v a=%#v b=%#v", root, childA, childB)
	}
	if root.sessionKey == childA.sessionKey || childA.sessionKey == childB.sessionKey || root.sessionKey == childB.sessionKey {
		t.Fatalf("agent replay scopes are not isolated: root=%#v a=%#v b=%#v", root, childA, childB)
	}
}

func TestCodexExecutorReasoningReplaySessionKeyRejectsBareClaudeUserID(t *testing.T) {
	from := sdktranslator.FromString("claude")
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"same-user-across-chats"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`)

	got := codexReasoningReplaySessionKey(context.Background(), from, req, cliproxyexecutor.Options{SourceFormat: from}, body)
	if got != "" {
		t.Fatalf("bare metadata.user_id must not become replay session key, got %q", got)
	}
}

func TestCodexExecutorReasoningReplaySessionKeyCanonicalizesSessionHeaderAliases(t *testing.T) {
	legacy := http.Header{"Session_id": []string{"session-alias"}}
	lowercase := http.Header{"session_id": []string{"session-alias"}}
	canonical := http.Header{"Session-Id": []string{"session-alias"}}

	gotLegacy := codexReasoningReplaySessionKeyFromHeaders(legacy)
	gotLowercase := codexReasoningReplaySessionKeyFromHeaders(lowercase)
	gotCanonical := codexReasoningReplaySessionKeyFromHeaders(canonical)

	if gotLegacy != gotLowercase || gotLowercase != gotCanonical {
		t.Fatalf("session header aliases produced different keys: legacy=%q lowercase=%q canonical=%q", gotLegacy, gotLowercase, gotCanonical)
	}
	if gotCanonical != "session-id:session-alias" {
		t.Fatalf("canonical session key = %q, want session-id:session-alias", gotCanonical)
	}
}

func TestCodexExecutorReasoningReplaySessionKeyCanonicalizesWindowHeaderWithPayload(t *testing.T) {
	payload := []byte(`{"client_metadata":{"x-codex-window-id":"window-1"}}`)
	headers := http.Header{"X-Codex-Window-Id": []string{"window-1"}}

	gotPayload := codexReasoningReplaySessionKeyFromPayload(payload)
	gotHeader := codexReasoningReplaySessionKeyFromHeaders(headers)

	if gotPayload != gotHeader {
		t.Fatalf("window replay keys differ: payload=%q header=%q", gotPayload, gotHeader)
	}
	if gotHeader != "window:window-1" {
		t.Fatalf("window replay key = %q, want window:window-1", gotHeader)
	}
}

func TestCodexExecutorReasoningReplayCacheSharesSameSessionAcrossCodexAuths(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	encryptedContent := validCodexReasoningEncryptedContentForTestSeed(12)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_done","type":"reasoning","summary":[],"encrypted_content":"` + encryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	firstAuth := &cliproxyauth.Auth{
		ID: "auth-replay-session-auth-a",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test-a",
		},
	}
	secondAuth := &cliproxyauth.Auth{
		ID: "auth-replay-session-auth-b",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test-b",
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	}

	_, err := executor.Execute(context.Background(), firstAuth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-auth-switch\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	_, err = executor.Execute(context.Background(), secondAuth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-auth-switch\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}, opts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(bodies))
	}
	secondBody := bodies[1]
	if got := gjson.GetBytes(secondBody, "input.0.type").String(); got != "reasoning" {
		t.Fatalf("input.0.type = %q, want same-session replay across auths; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.0.encrypted_content").String(); got != encryptedContent {
		t.Fatalf("injected encrypted_content = %q, want cached value", got)
	}
}

func codexReplaySessionOnlyContext(apiKey string) context.Context {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("userApiKey", apiKey)
	ginCtx.Set("accessProvider", "config-inline")
	ginCtx.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestCodexExecutorReasoningReplayCacheDoesNotInjectNativeResponsesRequest(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	cachedEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(3)
	internalcache.CacheCodexReasoningReplayItem("gpt-5.4", "prompt-cache:native-session", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+cachedEncryptedContent+`"}`))

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-replay-native",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_key":"native-session","input":[{"role":"user","content":"native"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "input.0.type").String(); got == "reasoning" {
		t.Fatalf("native Responses request should not receive cached reasoning; body=%s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want user; body=%s", got, string(gotBody))
	}
}

func TestCodexExecutorReasoningReplayCacheDoesNotStoreNativeResponsesRequest(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	nativeEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[{"id":"rs_native","type":"reasoning","summary":[],"encrypted_content":"` + nativeEncryptedContent + `"}]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-replay-native-store",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_key":"native-store","input":[{"role":"user","content":"native"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if _, ok := internalcache.GetCodexReasoningReplayItem("gpt-5.4", "prompt-cache:native-store"); ok {
		t.Fatal("native Responses request should not populate Codex reasoning replay cache")
	}
}

func TestCodexExecutorReasoningReplayCacheDoesNotDuplicateClaudeClientReasoning(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	cachedEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(5)
	clientEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(6)
	internalcache.CacheCodexReasoningReplayItem("gpt-5.4", "claude:session-2:agent:main", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+cachedEncryptedContent+`"}`))

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-replay-2",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-2\"}"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"client summary","signature":"` + clientEncryptedContent + `"},{"type":"text","text":"answer"}]},{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "input.0.encrypted_content").String(); got != clientEncryptedContent {
		t.Fatalf("client reasoning should be preserved, got %q want %q; body=%s", got, clientEncryptedContent, string(gotBody))
	}
	reasoningCount := 0
	for _, item := range gjson.GetBytes(gotBody, "input").Array() {
		if item.Get("type").String() == "reasoning" {
			reasoningCount++
		}
	}
	if reasoningCount != 1 {
		t.Fatalf("reasoning item count = %d, want 1; body=%s", reasoningCount, string(gotBody))
	}
}

func TestCodexExecutorReasoningReplayCacheInsertsReasoningBeforeAssistantOutputInClaudeHistory(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	cachedEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(7)
	internalcache.CacheCodexReasoningReplayItem("gpt-5.4", "claude:session-history:agent:main", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+cachedEncryptedContent+`"}`))

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-replay-history",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-history\"}"},
			"messages":[
				{"role":"user","content":[{"type":"text","text":"first"}]},
				{"role":"assistant","content":[{"type":"text","text":"answer"}]},
				{"role":"user","content":[{"type":"text","text":"next"}]}
			]
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(gotBody, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want first user message; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.1.type").String(); got != "reasoning" {
		t.Fatalf("input.1.type = %q, want cached reasoning before assistant output; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.1.encrypted_content").String(); got != cachedEncryptedContent {
		t.Fatalf("input.1.encrypted_content = %q, want cached reasoning; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.2.role").String(); got != "assistant" {
		t.Fatalf("input.2.role = %q, want assistant output after cached reasoning; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.3.role").String(); got != "user" {
		t.Fatalf("input.3.role = %q, want final user message; body=%s", got, string(gotBody))
	}
}

func TestCodexExecutorReasoningReplayCacheExecuteStreamStoresFinalDoneForClaude(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	addedEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(7)
	doneEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(8)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","item":{"id":"rs_added","type":"reasoning","status":"in_progress","summary":[],"encrypted_content":"` + addedEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_done","type":"reasoning","summary":[],"encrypted_content":"` + doneEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "auth-replay-stream",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}

	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"stream-session-1\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"stream-session-1\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(bodies))
	}
	secondBody := bodies[1]
	if got := gjson.GetBytes(secondBody, "input.0.encrypted_content").String(); got != doneEncryptedContent {
		t.Fatalf("stream cached encrypted_content = %q, want final done %q; body=%s", got, doneEncryptedContent, string(secondBody))
	}
}

func TestCodexExecutorReasoningReplayCacheClearsOnNonStreamResponseFailedInvalidSignature(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	cachedEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(9)
	internalcache.CacheCodexReasoningReplayItem("gpt-5.4", "claude:session-invalid-nonstream:agent:main", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+cachedEncryptedContent+`"}`))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"message":"Invalid signature in thinking block","type":"invalid_request_error","code":"invalid_request_error"}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), &cliproxyauth.Auth{
		ID: "auth-replay-invalid-nonstream",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-invalid-nonstream\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected invalid signature error")
	}
	if _, ok := internalcache.GetCodexReasoningReplayItem("gpt-5.4", "claude:session-invalid-nonstream:agent:main"); ok {
		t.Fatal("invalid signature response.failed should clear cached replay item")
	}
}

func TestCodexExecutorReasoningReplayCacheClearsOnStreamResponseFailedInvalidSignature(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	cachedEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(10)
	internalcache.CacheCodexReasoningReplayItem("gpt-5.4", "claude:session-invalid-stream:agent:main", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+cachedEncryptedContent+`"}`))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"message":"Invalid signature in thinking block","type":"invalid_request_error","code":"invalid_request_error"}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	streamResult, err := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		ID: "auth-replay-invalid-stream",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-invalid-stream\"}"},"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream setup error: %v", err)
	}

	gotChunkErr := false
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			gotChunkErr = true
		}
	}
	if !gotChunkErr {
		t.Fatal("expected stream chunk error for invalid signature response.failed")
	}
	if _, ok := internalcache.GetCodexReasoningReplayItem("gpt-5.4", "claude:session-invalid-stream:agent:main"); ok {
		t.Fatal("invalid signature response.failed should clear cached replay item")
	}
}

func TestCodexExecutorReasoningReplayCacheReplaysFunctionCallForClaudeToolResult(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	reasoningEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(8)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"` + reasoningEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"weather\"}","status":"in_progress"},"output_index":1}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"weather\"}","status":"completed"},"output_index":1}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "auth-replay-claude-tool",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"claude-session-tool\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"call lookup"}]}],
			"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
		}`),
	}, opts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"claude-session-tool\"}"},
			"messages":[
				{"role":"user","content":[{"type":"text","text":"call lookup"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]}
			],
			"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
		}`),
	}, opts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(bodies))
	}
	secondBody := bodies[1]
	if got := gjson.GetBytes(secondBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want initial user message; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.1.type").String(); got != "reasoning" {
		t.Fatalf("input.1.type = %q, want cached reasoning; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.2.type").String(); got != "function_call" {
		t.Fatalf("input.2.type = %q, want cached function_call; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.2.call_id").String(); got != "call_1" {
		t.Fatalf("input.2.call_id = %q, want call_1; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.3.type").String(); got != "function_call_output" {
		t.Fatalf("input.3.type = %q, want function_call_output after cached call; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.3.call_id").String(); got != "call_1" {
		t.Fatalf("input.3.call_id = %q, want call_1; body=%s", got, string(secondBody))
	}
}

func TestCodexExecutorReasoningReplayCacheRestoresCumulativeToolTurns(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	scope := codexReasoningReplayScope{
		modelName:  "gpt-5.4",
		sessionKey: "claude:session-cumulative-tools:agent:main",
	}
	firstEncrypted := validCodexReasoningEncryptedContentForTestSeed(21)
	secondEncrypted := validCodexReasoningEncryptedContentForTestSeed(22)
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+firstEncrypted+`"},`+
		`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"first\"}"}`+
		`]}}`))
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+secondEncrypted+`"},`+
		`{"type":"function_call","call_id":"call_2","name":"lookup","arguments":"{\"q\":\"second\"}"}`+
		`]}}`))

	body := []byte(`{"model":"gpt-5.4","input":[` +
		`{"type":"message","role":"user","content":"first"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"one"},` +
		`{"type":"message","role":"user","content":"second"},` +
		`{"type":"function_call_output","call_id":"call_2","output":"two"},` +
		`{"type":"message","role":"user","content":"third"}` +
		`]}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"metadata":{"user_id":"{\"session_id\":\"session-cumulative-tools\"}"}}`),
	}
	updated, gotScope := applyCodexReasoningReplayCache(context.Background(), sdktranslator.FromString("claude"), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}, body)
	if gotScope.modelName != scope.modelName || gotScope.sessionKey != scope.sessionKey {
		t.Fatalf("replay scope = %#v, want model/session %#v", gotScope, scope)
	}
	wantTypes := []string{"message", "reasoning", "function_call", "function_call_output", "message", "reasoning", "function_call", "function_call_output", "message"}
	gotItems := gjson.GetBytes(updated, "input").Array()
	if len(gotItems) != len(wantTypes) {
		t.Fatalf("input length = %d, want %d; body=%s", len(gotItems), len(wantTypes), updated)
	}
	for index, wantType := range wantTypes {
		if gotType := gotItems[index].Get("type").String(); gotType != wantType {
			t.Fatalf("input.%d.type = %q, want %q; body=%s", index, gotType, wantType, updated)
		}
	}
	if gotItems[1].Get("encrypted_content").String() != firstEncrypted || gotItems[5].Get("encrypted_content").String() != secondEncrypted {
		t.Fatalf("cumulative reasoning was not restored in turn order: %s", updated)
	}
}

func TestCodexExecutorReasoningReplayCacheRestoresCumulativeAssistantTurns(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	scope := codexReasoningReplayScope{
		modelName:  "gpt-5.4",
		sessionKey: "claude:session-cumulative-messages:agent:main",
	}
	firstEncrypted := validCodexReasoningEncryptedContentForTestSeed(23)
	secondEncrypted := validCodexReasoningEncryptedContentForTestSeed(24)
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+firstEncrypted+`"},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}`+
		`]}}`))
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+secondEncrypted+`"},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]}`+
		`]}}`))

	body := []byte(`{"model":"gpt-5.4","input":[` +
		`{"type":"message","role":"user","content":"first"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]},` +
		`{"type":"message","role":"user","content":"second"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second answer"}]},` +
		`{"type":"message","role":"user","content":"third"}` +
		`]}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"metadata":{"user_id":"{\"session_id\":\"session-cumulative-messages\"}"}}`),
	}
	updated, gotScope := applyCodexReasoningReplayCache(context.Background(), sdktranslator.FromString("claude"), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}, body)
	if gotScope.modelName != scope.modelName || gotScope.sessionKey != scope.sessionKey {
		t.Fatalf("replay scope = %#v, want model/session %#v", gotScope, scope)
	}
	wantTypes := []string{"message", "reasoning", "message", "message", "reasoning", "message", "message"}
	gotItems := gjson.GetBytes(updated, "input").Array()
	if len(gotItems) != len(wantTypes) {
		t.Fatalf("input length = %d, want %d; body=%s", len(gotItems), len(wantTypes), updated)
	}
	for index, wantType := range wantTypes {
		if gotType := gotItems[index].Get("type").String(); gotType != wantType {
			t.Fatalf("input.%d.type = %q, want %q; body=%s", index, gotType, wantType, updated)
		}
	}
	if gotItems[1].Get("encrypted_content").String() != firstEncrypted || gotItems[4].Get("encrypted_content").String() != secondEncrypted {
		t.Fatalf("assistant reasoning was not restored at its original turns: %s", updated)
	}
}

func TestCodexExecutorReasoningReplayCacheSkipsDetachedTurnAfterCompaction(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	scope := codexReasoningReplayScope{
		modelName:  "gpt-5.4",
		sessionKey: "claude:session-compacted:agent:main",
	}
	detachedEncrypted := validCodexReasoningEncryptedContentForTestSeed(25)
	retainedEncrypted := validCodexReasoningEncryptedContentForTestSeed(26)
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+detachedEncrypted+`"},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"removed answer"}]}`+
		`]}}`))
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+retainedEncrypted+`"},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"retained answer"}]}`+
		`]}}`))

	body := []byte(`{"model":"gpt-5.4","input":[` +
		`{"type":"message","role":"user","content":"compacted summary"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"retained answer"}]},` +
		`{"type":"message","role":"user","content":"continue"}` +
		`]}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"metadata":{"user_id":"{\"session_id\":\"session-compacted\"}"}}`),
	}
	updated, _ := applyCodexReasoningReplayCache(context.Background(), sdktranslator.FromString("claude"), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}, body)
	gotItems := gjson.GetBytes(updated, "input").Array()
	if len(gotItems) != 4 || gotItems[1].Get("encrypted_content").String() != retainedEncrypted {
		t.Fatalf("retained turn reasoning was not restored: %s", updated)
	}
	for _, item := range gotItems {
		if item.Get("encrypted_content").String() == detachedEncrypted {
			t.Fatalf("detached reasoning moved into compacted history: %s", updated)
		}
	}
}

func TestCodexExecutorReasoningReplayCacheMatchesNewestDuplicateAssistantAfterCompaction(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	scope := codexReasoningReplayScope{
		modelName:  "gpt-5.4",
		sessionKey: "claude:session-duplicate-compaction:agent:main",
	}
	oldEncrypted := validCodexReasoningEncryptedContentForTestSeed(27)
	newEncrypted := validCodexReasoningEncryptedContentForTestSeed(28)
	for _, encryptedContent := range []string{oldEncrypted, newEncrypted} {
		cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
			`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"},`+
			`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`+
			`]}}`))
	}

	body := []byte(`{"model":"gpt-5.4","input":[` +
		`{"type":"message","role":"user","content":"compacted summary"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]},` +
		`{"type":"message","role":"user","content":"continue"}` +
		`]}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"metadata":{"user_id":"{\"session_id\":\"session-duplicate-compaction\"}"}}`),
	}
	updated, _ := applyCodexReasoningReplayCache(context.Background(), sdktranslator.FromString("claude"), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}, body)
	gotItems := gjson.GetBytes(updated, "input").Array()
	if len(gotItems) != 4 || gotItems[1].Get("encrypted_content").String() != newEncrypted {
		t.Fatalf("newest duplicate assistant turn was not retained: %s", updated)
	}
	for _, item := range gotItems {
		if item.Get("encrypted_content").String() == oldEncrypted {
			t.Fatalf("detached duplicate assistant reasoning was restored: %s", updated)
		}
	}
}

func TestCodexExecutorReasoningReplayCacheUsesRequestPrefixForDuplicateOutOfOrderTurns(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	body := []byte(`{"model":"gpt-5.4","input":[` +
		`{"type":"message","role":"user","content":"first"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]},` +
		`{"type":"message","role":"user","content":"second"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]},` +
		`{"type":"message","role":"user","content":"third"}` +
		`]}`)
	inputItems := gjson.GetBytes(body, "input").Array()
	baseScope := codexReasoningReplayScope{
		modelName:  "gpt-5.4",
		sessionKey: "claude:session-duplicate-prefix:agent:main",
	}
	oldEncrypted := validCodexReasoningEncryptedContentForTestSeed(29)
	newEncrypted := validCodexReasoningEncryptedContentForTestSeed(30)
	newScope := baseScope
	newScope.requestFingerprint = codexReplayInputPrefixFingerprint(inputItems, 3)
	cacheCodexReasoningReplayFromCompleted(newScope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+newEncrypted+`"},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`+
		`]}}`))
	oldScope := baseScope
	oldScope.requestFingerprint = codexReplayInputPrefixFingerprint(inputItems, 1)
	cacheCodexReasoningReplayFromCompleted(oldScope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+oldEncrypted+`"},`+
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}`+
		`]}}`))

	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"metadata":{"user_id":"{\"session_id\":\"session-duplicate-prefix\"}"}}`),
	}
	updated, _ := applyCodexReasoningReplayCache(context.Background(), sdktranslator.FromString("claude"), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}, body)
	gotItems := gjson.GetBytes(updated, "input").Array()
	if len(gotItems) != 7 || gotItems[1].Get("encrypted_content").String() != oldEncrypted || gotItems[4].Get("encrypted_content").String() != newEncrypted {
		t.Fatalf("duplicate out-of-order turns were not matched by request prefix: %s", updated)
	}
}

func TestCodexExecutorReasoningReplayCacheDropsFunctionCallWithoutMatchingOutput(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	encryptedContent := validCodexReasoningEncryptedContentForTestSeed(14)
	scope := codexReasoningReplayScope{
		modelName:  "gpt-5.4",
		sessionKey: "claude:session-dropped-tool:agent:main",
	}
	cacheCodexReasoningReplayFromCompleted(scope, []byte(`{"response":{"output":[`+
		`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"},`+
		`{"type":"function_call","call_id":"call_dropped","name":"TaskCreate","arguments":"{}"}`+
		`]}}`))

	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`)
	req := cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"session-dropped-tool\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]
		}`),
	}

	updated, replayScope := applyCodexReasoningReplayCache(
		context.Background(),
		sdktranslator.FromString("claude"),
		req,
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")},
		body,
	)
	if replayScope.modelName != scope.modelName || replayScope.sessionKey != scope.sessionKey {
		t.Fatalf("replay scope = %#v, want model/session %#v", replayScope, scope)
	}
	if got := gjson.GetBytes(updated, "input.0.role").String(); got != "user" {
		t.Fatalf("input.0.role = %q, want detached turn to be skipped; body=%s", got, string(updated))
	}
	if gjson.GetBytes(updated, `input.#(type=="reasoning")`).Exists() {
		t.Fatalf("detached turn reasoning should not move to the front; body=%s", string(updated))
	}
	if gjson.GetBytes(updated, `input.#(call_id=="call_dropped")`).Exists() {
		t.Fatalf("cached function_call without matching output should not be replayed; body=%s", string(updated))
	}
}

func TestCodexExecutorReasoningReplayCacheMatchesShortenedClaudeToolResultCallID(t *testing.T) {
	internalcache.ClearCodexReasoningReplayCache()
	t.Cleanup(internalcache.ClearCodexReasoningReplayCache)

	longCallID := "call_" + strings.Repeat("a", 62)
	shortCallID := shortenedCodexReplayCallIDForTest(longCallID)
	if len(longCallID) <= 64 || len(shortCallID) > 64 || shortCallID == longCallID {
		t.Fatalf("invalid test setup: long=%q short=%q", longCallID, shortCallID)
	}

	reasoningEncryptedContent := validCodexReasoningEncryptedContentForTestSeed(13)
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		bodies = append(bodies, body)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_long","type":"reasoning","summary":[],"encrypted_content":"` + reasoningEncryptedContent + `"},"output_index":0}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_long","type":"function_call","call_id":"` + longCallID + `","name":"lookup","arguments":"{\"q\":\"weather\"}","status":"completed"},"output_index":1}` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.4","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "auth-replay-claude-short-tool",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"claude-session-short-tool\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"call lookup"}]}],
			"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
		}`),
	}, opts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}

	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-test\",\"account_uuid\":\"\",\"session_id\":\"claude-session-short-tool\"}"},
			"messages":[
				{"role":"user","content":[{"type":"text","text":"call lookup"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + shortCallID + `","content":"sunny"}]}
			],
			"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
		}`),
	}, opts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(bodies))
	}
	secondBody := bodies[1]
	if got := gjson.GetBytes(secondBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want initial user message; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.1.type").String(); got != "reasoning" {
		t.Fatalf("input.1.type = %q, want cached reasoning; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.2.type").String(); got != "function_call" {
		t.Fatalf("input.2.type = %q, want cached function_call; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.2.call_id").String(); got != shortCallID {
		t.Fatalf("input.2.call_id = %q, want shortened call_id %q; body=%s", got, shortCallID, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.3.type").String(); got != "function_call_output" {
		t.Fatalf("input.3.type = %q, want function_call_output after cached call; body=%s", got, string(secondBody))
	}
	if got := gjson.GetBytes(secondBody, "input.3.call_id").String(); got != shortCallID {
		t.Fatalf("input.3.call_id = %q, want shortened call_id %q; body=%s", got, shortCallID, string(secondBody))
	}
}
