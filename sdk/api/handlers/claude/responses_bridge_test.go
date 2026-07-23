package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	responsesBridgeClientModel   = "claude-fable-5-dd-los-6.5-tpg"
	responsesBridgeUpstreamModel = "gpt-5.6-sol"
)

type responsesBridgeCaptureExecutor struct {
	request      coreexecutor.Request
	options      coreexecutor.Options
	executeCalls int
	streamCalls  int
}

func (e *responsesBridgeCaptureExecutor) Identifier() string { return constant.Codex }

func (e *responsesBridgeCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.capture(req, opts)
	e.executeCalls++
	if opts.Alt == constant.ClaudeResponsesCompactBridgeAlt {
		return coreexecutor.Response{
			Payload: []byte(`{"id":"resp_compact_1","object":"response.compaction","output":[{"id":"msg_1","type":"message","status":"completed","role":"user","content":[{"type":"input_text","text":"hello"}]},{"id":"cmp_1","type":"compaction_summary","encrypted_content":"encrypted-state"}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}`),
			Headers: http.Header{"X-Upstream": []string{"compact"}},
		}, nil
	}
	return coreexecutor.Response{
		Payload: []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"gpt-5.6-sol","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`),
		Headers: http.Header{"X-Upstream": []string{"responses"}},
	}, nil
}

func (e *responsesBridgeCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.capture(req, opts)
	e.streamCalls++
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"gpt-5.6-sol\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Headers: http.Header{"X-Upstream": []string{"responses"}}, Chunks: chunks}, nil
}

func (e *responsesBridgeCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesBridgeCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *responsesBridgeCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesBridgeCaptureExecutor) capture(req coreexecutor.Request, opts coreexecutor.Options) {
	e.request = req
	e.options = opts
}

func newResponsesBridgeHandler(t *testing.T) (*ClaudeCodeAPIHandler, *responsesBridgeCaptureExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	executor := &responsesBridgeCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	oauth := &coreauth.Auth{
		ID:       "responses-bridge-auth",
		Provider: constant.Codex,
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token", "account_id": "account-id"},
	}
	if _, errRegister := manager.Register(context.Background(), oauth); errRegister != nil {
		t.Fatalf("register OAuth auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(oauth.ID, oauth.Provider, []*registry.ModelInfo{{ID: responsesBridgeUpstreamModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(oauth.ID)
	})
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, manager)
	return NewClaudeCodeAPIHandler(base), executor
}

func serveClaudeMessages(t *testing.T, handler *ClaudeCodeAPIHandler, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestShouldUseClaudeResponsesBridge(t *testing.T) {
	tests := []struct {
		name          string
		clientModel   string
		upstreamModel string
		wantResponses bool
	}{
		{name: "encoded GPT model", clientModel: responsesBridgeClientModel, upstreamModel: responsesBridgeUpstreamModel, wantResponses: true},
		{name: "plain GPT model", clientModel: responsesBridgeUpstreamModel, upstreamModel: responsesBridgeUpstreamModel, wantResponses: false},
		{name: "native Claude model", clientModel: "claude-sonnet-4-6", upstreamModel: "claude-sonnet-4-6", wantResponses: false},
		{name: "encoded non-GPT model", clientModel: "claude-fable-5-dd-orp-5.2-inimeg", upstreamModel: "gemini-2.5-pro", wantResponses: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldUseClaudeResponsesBridge(tt.clientModel, tt.upstreamModel); got != tt.wantResponses {
				t.Fatalf("shouldUseClaudeResponsesBridge(%q, %q) = %v, want %v", tt.clientModel, tt.upstreamModel, got, tt.wantResponses)
			}
		})
	}
}

func TestIsClaudeCompactRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "summary wording without internal sentinels",
			body: `{"messages":[{"role":"user","content":"hello"},{"role":"user","content":"Your task is to create a detailed summary of the conversation so far. Do not use tools. Preserve the API details."}]}`,
			want: false,
		},
		{
			name: "recent portion array content",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the recent portion of the conversation.\n\nREMINDER: Do NOT call any tools."}]}]}`,
			want: true,
		},
		{
			name: "Claude Code automatic compact prompt",
			body: `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.\n\nREMINDER: Do NOT call any tools."}]}`,
			want: true,
		},
		{
			name: "ordinary summary request",
			body: `{"messages":[{"role":"user","content":"Please create a detailed summary of this conversation."}]}`,
			want: false,
		},
		{
			name: "ordinary request matching compact summary wording",
			body: `{"messages":[{"role":"user","content":"Your task is to create a detailed summary of the conversation so far."}]}`,
			want: false,
		},
		{
			name: "compact text not final",
			body: `{"messages":[{"role":"user","content":"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the conversation so far.\n\nREMINDER: Do NOT call any tools."},{"role":"user","content":"continue"}]}`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClaudeCompactRequest([]byte(tt.body)); got != tt.want {
				t.Fatalf("isClaudeCompactRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClaudeMessagesResponsesBridgeNonStreaming(t *testing.T) {
	handler, executor := newResponsesBridgeHandler(t)
	body := `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`
	recorder := serveClaudeMessages(t, handler, "/v1/messages?source=localhost", body)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := gjson.Get(recorder.Body.String(), "model").String(); got != responsesBridgeClientModel {
		t.Fatalf("response model = %q, want %q; body=%s", got, responsesBridgeClientModel, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Upstream"); got != "responses" {
		t.Fatalf("X-Upstream = %q, want responses", got)
	}

	gotReq, gotOpts := executor.request, executor.options
	if gotReq.Model != responsesBridgeUpstreamModel {
		t.Fatalf("executor model = %q, want %q", gotReq.Model, responsesBridgeUpstreamModel)
	}
	if got := gjson.GetBytes(gotReq.Payload, "model").String(); got != responsesBridgeUpstreamModel {
		t.Fatalf("payload model = %q, want %q; payload=%s", got, responsesBridgeUpstreamModel, gotReq.Payload)
	}
	if gotOpts.Alt != constant.ClaudeResponsesBridgeAlt {
		t.Fatalf("Alt = %q, want %q", gotOpts.Alt, constant.ClaudeResponsesBridgeAlt)
	}
	if gotOpts.SourceFormat != sdktranslator.FormatClaude || gotOpts.ResponseFormat != sdktranslator.FormatClaude {
		t.Fatalf("formats = %q -> %q, want claude -> claude", gotOpts.SourceFormat, gotOpts.ResponseFormat)
	}
	if got := gotOpts.Query.Get("source"); got != "localhost" {
		t.Fatalf("query source = %q, want localhost", got)
	}
	if _, pinned := gotOpts.Metadata[coreexecutor.PinnedAuthMetadataKey]; pinned {
		t.Fatalf("bridge unexpectedly pinned an auth: %#v", gotOpts.Metadata)
	}

	body = `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"user","content":"Your task is to create a detailed summary of the conversation so far."}]}`
	recorder = serveClaudeMessages(t, handler, "/v1/messages", body)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), claudeCompactionCapsulePrefix) {
		t.Fatalf("ordinary summary request returned compaction marker: %s", recorder.Body.String())
	}
	gotOpts = executor.options
	if gotOpts.Alt != constant.ClaudeResponsesBridgeAlt {
		t.Fatalf("Alt = %q, want %q", gotOpts.Alt, constant.ClaudeResponsesBridgeAlt)
	}
}

func TestClaudeMessagesResponsesBridgeStreaming(t *testing.T) {
	handler, executor := newResponsesBridgeHandler(t)
	body := `{"model":"claude-fable-5-dd-los-6.5-tpg","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`
	recorder := serveClaudeMessages(t, handler, "/v1/messages", body)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"model":"`+responsesBridgeClientModel+`"`) {
		t.Fatalf("stream did not restore client model; body=%s", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	gotOpts := executor.options
	if !gotOpts.Stream {
		t.Fatal("executor stream option = false, want true")
	}
	if _, pinned := gotOpts.Metadata[coreexecutor.PinnedAuthMetadataKey]; pinned {
		t.Fatalf("stream bridge unexpectedly pinned an auth: %#v", gotOpts.Metadata)
	}
}

func TestClaudeMessagesResponsesBridgeCompactNonStreaming(t *testing.T) {
	handler, executor := newResponsesBridgeHandler(t)
	body := `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the conversation so far. Preserve the API details.\n\nREMINDER: Do NOT call any tools."}]}`
	recorder := serveClaudeMessages(t, handler, "/v1/messages", body)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	marker := gjson.Get(recorder.Body.String(), "content.0.text").String()
	_, capsule, found, errStrip := stripClaudeCompactionCapsule(marker)
	if errStrip != nil || !found {
		t.Fatalf("decode compact marker: found=%v err=%v marker=%q", found, errStrip, marker)
	}
	if capsule.Model != responsesBridgeUpstreamModel {
		t.Fatalf("capsule model = %q, want %q", capsule.Model, responsesBridgeUpstreamModel)
	}
	if capsule.AuthID != "responses-bridge-auth" {
		t.Fatalf("capsule auth ID = %q, want responses-bridge-auth", capsule.AuthID)
	}
	if got := gjson.Get(recorder.Body.String(), "model").String(); got != responsesBridgeClientModel {
		t.Fatalf("response model = %q, want %q", got, responsesBridgeClientModel)
	}

	gotReq, gotOpts := executor.request, executor.options
	if gotOpts.Alt != constant.ClaudeResponsesCompactBridgeAlt {
		t.Fatalf("Alt = %q, want %q", gotOpts.Alt, constant.ClaudeResponsesCompactBridgeAlt)
	}
	if gotOpts.ResponseFormat != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("ResponseFormat = %q, want %q", gotOpts.ResponseFormat, sdktranslator.FormatOpenAIResponse)
	}
	if got := gjson.GetBytes(gotReq.Payload, "messages.2.content").String(); !strings.Contains(got, "Preserve the API details") {
		t.Fatalf("custom compact instruction was not preserved; payload=%s", gotReq.Payload)
	}
	if _, pinned := gotOpts.Metadata[coreexecutor.PinnedAuthMetadataKey]; pinned {
		t.Fatalf("initial compact request unexpectedly pinned an auth: %#v", gotOpts.Metadata)
	}
}

func TestClaudeMessagesResponsesBridgeCompactStreamingUsesBufferedCompactEndpoint(t *testing.T) {
	handler, executor := newResponsesBridgeHandler(t)
	body := `{"model":"claude-fable-5-dd-los-6.5-tpg","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the conversation so far.\n\nREMINDER: Do NOT call any tools."}]}`
	recorder := serveClaudeMessages(t, handler, "/v1/messages", body)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "event: message_start") || !strings.Contains(recorder.Body.String(), "cpa-responses-compaction") {
		t.Fatalf("unexpected compact SSE: %s", recorder.Body.String())
	}
	if executor.executeCalls != 1 || executor.streamCalls != 0 {
		t.Fatalf("executor calls = execute:%d stream:%d, want 1/0", executor.executeCalls, executor.streamCalls)
	}
}

func TestClaudeMessagesResponsesBridgeRehydratesCompactCapsule(t *testing.T) {
	handler, executor := newResponsesBridgeHandler(t)
	compactBody := `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"user","content":"CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.\n\nYour task is to create a detailed summary of the conversation so far.\n\nREMINDER: Do NOT call any tools."}]}`
	compactRecorder := serveClaudeMessages(t, handler, "/v1/messages", compactBody)
	marker := gjson.Get(compactRecorder.Body.String(), "content.0.text").String()
	if marker == "" {
		t.Fatalf("compact response has no marker: %s", compactRecorder.Body.String())
	}

	followupBody := `{"model":"claude-fable-5-dd-los-6.5-tpg","max_tokens":128,"messages":[{"role":"assistant","content":` + string(mustJSONMarshalForTest(t, marker)) + `},{"role":"user","content":"continue"}]}`
	followupRecorder := serveClaudeMessages(t, handler, "/v1/messages", followupBody)

	if followupRecorder.Code != http.StatusOK {
		t.Fatalf("follow-up status = %d; body=%s", followupRecorder.Code, followupRecorder.Body.String())
	}
	gotReq, gotOpts := executor.request, executor.options
	if gotOpts.Alt != constant.ClaudeResponsesBridgeAlt {
		t.Fatalf("follow-up Alt = %q, want %q", gotOpts.Alt, constant.ClaudeResponsesBridgeAlt)
	}
	if pinned := gotOpts.Metadata[coreexecutor.PinnedAuthMetadataKey]; pinned != "responses-bridge-auth" {
		t.Fatalf("compaction replay pinned auth = %#v, want responses-bridge-auth", pinned)
	}
	if got := gjson.GetBytes(gotReq.Payload, constant.ClaudeResponsesCompactionField+".output.1.type").String(); got != "compaction_summary" {
		t.Fatalf("replay compaction item type = %q; payload=%s", got, gotReq.Payload)
	}
	if got := gjson.GetBytes(gotReq.Payload, "messages.0.content").String(); got != "continue" {
		t.Fatalf("capsule message was not removed; payload=%s", gotReq.Payload)
	}
}

func TestPrepareClaudeCompactionReplayUsesNewestCanonicalWindow(t *testing.T) {
	firstMarker := mustClaudeCompactionMarkerForTest(t)
	secondCompactRequest := map[string]any{
		"model": responsesBridgeUpstreamModel,
		"messages": []any{
			map[string]any{"role": "assistant", "content": firstMarker},
			map[string]any{"role": "user", "content": "after first compaction"},
		},
	}
	preparedSecond, firstReplay, errPrepareSecond := prepareClaudeCompactionReplay(mustJSONMarshalForTest(t, secondCompactRequest), responsesBridgeUpstreamModel)
	if errPrepareSecond != nil {
		t.Fatalf("prepare second compaction: %v", errPrepareSecond)
	}
	if firstReplay == nil || !strings.Contains(string(mustJSONMarshalForTest(t, firstReplay.Output)), "encrypted") {
		t.Fatalf("first replay was not recovered: %#v", firstReplay)
	}
	if got := gjson.GetBytes(preparedSecond, "messages.0.content").String(); got != "after first compaction" {
		t.Fatalf("second compact input retained capsule message: %s", preparedSecond)
	}

	secondCompact := []byte(`{"id":"resp_compact_2","object":"response.compaction","output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"new canonical window"}]},{"type":"compaction","encrypted_content":"encrypted-second"}],"usage":{"input_tokens":100,"output_tokens":20}}`)
	_, secondMarker, errBuild := buildClaudeCompactResponse(secondCompact, responsesBridgeClientModel, responsesBridgeUpstreamModel, "responses-bridge-auth")
	if errBuild != nil {
		t.Fatalf("build second compact response: %v", errBuild)
	}
	followup := map[string]any{
		"model": responsesBridgeUpstreamModel,
		"messages": []any{
			map[string]any{"role": "assistant", "content": secondMarker},
			map[string]any{"role": "user", "content": "continue"},
		},
	}
	_, secondReplay, errPrepareFollowup := prepareClaudeCompactionReplay(mustJSONMarshalForTest(t, followup), responsesBridgeUpstreamModel)
	if errPrepareFollowup != nil {
		t.Fatalf("prepare follow-up after second compaction: %v", errPrepareFollowup)
	}
	encodedReplay := string(mustJSONMarshalForTest(t, secondReplay.Output))
	if !strings.Contains(encodedReplay, "encrypted-second") {
		t.Fatalf("new canonical window missing: %s", encodedReplay)
	}
	if strings.Contains(encodedReplay, "encrypted-state") {
		t.Fatalf("prior compacted window accumulated into replacement: %s", encodedReplay)
	}
}

func TestPrepareClaudeCompactionReplayIgnoresQuotedMarkerConstants(t *testing.T) {
	marker := mustClaudeCompactionMarkerForTest(t)
	sourceExcerpt := `const (
	claudeCompactionCapsulePrefix = "<cpa-responses-compaction>"
	claudeCompactionCapsuleSuffix = "</cpa-responses-compaction>"
)`
	body := map[string]any{
		"model": responsesBridgeClientModel,
		"messages": []any{
			map[string]any{"role": "user", "content": "Compacted conversation\n\n" + marker + "\n\nContinue."},
			map[string]any{"role": "assistant", "content": sourceExcerpt},
			map[string]any{"role": "user", "content": "continue"},
		},
	}
	rawJSON := mustJSONMarshalForTest(t, body)

	updated, replay, errPrepare := prepareClaudeCompactionReplay(rawJSON, responsesBridgeUpstreamModel)
	if errPrepare != nil {
		t.Fatalf("prepare replay: %v", errPrepare)
	}
	if replay == nil || replay.AuthID != "responses-bridge-auth" {
		t.Fatalf("replay capsule = %#v, want auth ID responses-bridge-auth", replay)
	}
	if got := gjson.GetBytes(updated, "messages.1.content").String(); got != sourceExcerpt {
		t.Fatalf("source excerpt changed:\ngot:  %q\nwant: %q", got, sourceExcerpt)
	}
	if got := gjson.GetBytes(updated, "messages.0.content").String(); strings.Contains(got, claudeCompactionCapsulePrefix) || !strings.Contains(got, "Compacted conversation") || !strings.Contains(got, "Continue.") {
		t.Fatalf("capsule wrapper was not preserved semantically: %q", got)
	}
}

func mustClaudeCompactionMarkerForTest(t *testing.T) string {
	t.Helper()
	capsule := &claudeCompactionCapsule{
		Version: claudeCompactionCapsuleVersion,
		Model:   responsesBridgeUpstreamModel,
		AuthID:  "responses-bridge-auth",
		Output: []json.RawMessage{
			json.RawMessage(`{"type":"compaction_summary","encrypted_content":"encrypted-state"}`),
		},
	}
	marker, errEncode := encodeClaudeCompactionCapsule(capsule)
	if errEncode != nil {
		t.Fatalf("encode capsule: %v", errEncode)
	}
	return marker
}

func TestStripClaudeCompactionCapsuleRejectsInvalidMarkers(t *testing.T) {
	marker := mustClaudeCompactionMarkerForTest(t)
	tests := []struct {
		name    string
		text    string
		wantErr string
	}{
		{name: "malformed", text: "before\n\n" + claudeCompactionCapsulePrefix + `"not-base64"` + claudeCompactionCapsuleSuffix + "\n\nafter", wantErr: "decode compaction capsule"},
		{name: "multiple", text: marker + "\n\n" + marker, wantErr: "multiple compaction capsules"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, errStrip := stripClaudeCompactionCapsule(tt.text); errStrip == nil || !strings.Contains(errStrip.Error(), tt.wantErr) {
				t.Fatalf("strip error = %v, want %q", errStrip, tt.wantErr)
			}
		})
	}
}

func mustJSONMarshalForTest(t *testing.T, value any) []byte {
	t.Helper()
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal test value: %v", errMarshal)
	}
	return encoded
}

func TestRewriteClaudeBridgeResponseModelLeavesOtherEventsAlone(t *testing.T) {
	input := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
	if got := rewriteClaudeBridgeResponseModel(input, responsesBridgeClientModel); string(got) != string(input) {
		t.Fatalf("non-message_start event changed:\ngot=%s\nwant=%s", got, input)
	}
}
