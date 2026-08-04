package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type kimiLocalBadRequestError struct{}

func (kimiLocalBadRequestError) Error() string   { return "local validation failed" }
func (kimiLocalBadRequestError) StatusCode() int { return http.StatusBadRequest }

func TestKimiThinkingReplayModelFamily(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{model: "k3", want: "k3"},
		{model: "kimi-k3", want: "k3"},
		{model: "k3-256k", want: "k3"},
		{model: "kimi-k3-256k(high)", want: "k3"},
		{model: "kimi-k2.7-code", want: "k2.7-code"},
		{model: "kimi-k2.7-code-highspeed", want: "k2.7-code-highspeed"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := kimiThinkingReplayModelFamily(tc.model); got != tc.want {
				t.Fatalf("kimiThinkingReplayModelFamily(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

func TestRestoreKimiThinkingReplayContentPreservesCompleteAssistantContent(t *testing.T) {
	cached := []byte(`[
		{"type":"thinking","thinking":"full reasoning","signature":"kimi-signature"},
		{"type":"text","text":"I will inspect the file."},
		{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}
	]`)
	body := []byte(`{"messages":[
		{"role":"user","content":"inspect"},
		{"role":"assistant","content":[
			{"type":"text","text":"I will inspect the file."},
			{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}
		]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
	]}`)

	updated, restored := restoreKimiThinkingReplayContent(body, cached)
	if !restored {
		t.Fatal("expected cached thinking content to be restored")
	}
	got := gjson.GetBytes(updated, "messages.1.content")
	if !kimiJSONEqual([]byte(got.Raw), cached) {
		t.Fatalf("restored content = %s, want complete cached content %s", got.Raw, cached)
	}
}

func TestRestoreKimiThinkingReplayContentDoesNotReplaceExistingThinking(t *testing.T) {
	cached := []byte(`[{"type":"thinking","thinking":"cached","signature":"cached-signature"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]`)
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"current","signature":"current-signature"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]}]}`)

	updated, restored := restoreKimiThinkingReplayContent(body, cached)
	if restored {
		t.Fatalf("existing thinking must not be replaced: %s", updated)
	}
	if !kimiJSONEqual(updated, body) {
		t.Fatalf("request changed despite existing thinking: got %s want %s", updated, body)
	}
}

func TestPrepareKimiThinkingReplayRequestSharesOnlyK3Variants(t *testing.T) {
	internalcache.ClearKimiThinkingReplayCache()
	t.Cleanup(internalcache.ClearKimiThinkingReplayCache)

	const sessionID = "family-switch"
	const cached = `[{"type":"thinking","thinking":"reasoning","signature":"kimi-signature"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]`
	if !internalcache.CacheKimiThinkingReplayBestEffort(context.Background(), "k3", "execution:"+sessionID, []byte(cached)) {
		t.Fatal("failed to seed K3 thinking replay cache")
	}
	if !internalcache.CacheKimiThinkingReplayBestEffort(context.Background(), "k2.7-code", "execution:"+sessionID, []byte(cached)) {
		t.Fatal("failed to seed K2.7 Code thinking replay cache")
	}

	payload := []byte(`{"model":"kimi-k3-256k","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]}]}`)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	prepared, scope := prepareKimiThinkingReplayRequest(context.Background(), cliproxyexecutor.Request{Model: "kimi-k3-256k", Payload: payload}, opts)
	if scope.modelFamily != "k3" {
		t.Fatalf("K3 replay family = %q, want k3", scope.modelFamily)
	}
	if !gjson.GetBytes(prepared.Payload, "messages.0.content.0.signature").Exists() {
		t.Fatalf("K3 variant switch did not restore cached thinking: %s", prepared.Payload)
	}

	k27Payload := []byte(`{"model":"kimi-k2.7-code-highspeed","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]}]}`)
	preparedK27, scopeK27 := prepareKimiThinkingReplayRequest(context.Background(), cliproxyexecutor.Request{Model: "kimi-k2.7-code-highspeed", Payload: k27Payload}, opts)
	if scopeK27.modelFamily != "k2.7-code-highspeed" {
		t.Fatalf("K2.7 replay family = %q, want k2.7-code-highspeed", scopeK27.modelFamily)
	}
	if gjson.GetBytes(preparedK27.Payload, "messages.0.content.0.signature").Exists() {
		t.Fatalf("K2.7 variants must remain isolated: %s", preparedK27.Payload)
	}
}

func TestKimiThinkingReplayScopeIsolatesClaudeCodeCallers(t *testing.T) {
	internalcache.ClearKimiThinkingReplayCache()
	t.Cleanup(internalcache.ClearKimiThinkingReplayCache)

	payload := []byte(`{"model":"kimi-k3","metadata":{"user_id":"{\"session_id\":\"claude-session\"}"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]}]}`)
	req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	callerAContext := testContextWithAPIKey("caller-a")
	callerAScope := kimiThinkingReplayScopeFromRequest(callerAContext, req, opts)
	if !callerAScope.valid() || !strings.Contains(callerAScope.sessionKey, ":claude:claude-session:agent:main") {
		t.Fatalf("caller A scope = %+v, want isolated Claude Code session", callerAScope)
	}
	const cached = `[{"type":"thinking","thinking":"reasoning","signature":"kimi-signature"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]`
	if !internalcache.CacheKimiThinkingReplayBestEffort(callerAContext, callerAScope.modelFamily, callerAScope.sessionKey, []byte(cached)) {
		t.Fatal("failed to seed caller A cache")
	}

	preparedA, _ := prepareKimiThinkingReplayRequest(callerAContext, req, opts)
	if !gjson.GetBytes(preparedA.Payload, "messages.0.content.0.signature").Exists() {
		t.Fatalf("caller A did not receive its replay: %s", preparedA.Payload)
	}
	preparedB, callerBScope := prepareKimiThinkingReplayRequest(testContextWithAPIKey("caller-b"), req, opts)
	if callerBScope.sessionKey == callerAScope.sessionKey {
		t.Fatal("different downstream API keys shared one replay scope")
	}
	if gjson.GetBytes(preparedB.Payload, "messages.0.content.0.signature").Exists() {
		t.Fatalf("caller B received caller A replay: %s", preparedB.Payload)
	}
	_, unauthenticatedScope := prepareKimiThinkingReplayRequest(context.Background(), req, opts)
	if unauthenticatedScope.valid() {
		t.Fatalf("unauthenticated client-controlled session must not enable replay: %+v", unauthenticatedScope)
	}
}

func TestKimiExecutorClaudeNonStreamReplaysThinkingAcrossK3VariantSwitch(t *testing.T) {
	internalcache.ClearKimiThinkingReplayCache()
	t.Cleanup(internalcache.ClearKimiThinkingReplayCache)

	const cachedContent = `[{"type":"thinking","thinking":"full reasoning","signature":"kimi-signature"},{"type":"text","text":"Inspecting."},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]`
	var upstreamBodies [][]byte
	callCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		upstreamBodies = append(upstreamBodies, body)
		callCount++
		response := `{"id":"msg_2","type":"message","role":"assistant","model":"k3","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		if callCount == 1 {
			response = `{"id":"msg_1","type":"message","role":"assistant","model":"k3-256k","content":` + cachedContent + `,"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(response)),
		}, nil
	}))

	executor := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{}, Metadata: map[string]any{"access_token": "test-token"}}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "nonstream-switch",
		},
	}
	firstPayload := []byte(`{"model":"kimi-k3-256k","max_tokens":32,"messages":[{"role":"user","content":"inspect"}]}`)
	opts.OriginalRequest = firstPayload
	if _, errExecute := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k3-256k", Payload: firstPayload}, opts); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}

	secondPayload := []byte(`{"model":"kimi-k3","max_tokens":32,"messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":[{"type":"text","text":"Inspecting."},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`)
	opts.OriginalRequest = secondPayload
	if _, errExecute := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k3", Payload: secondPayload}, opts); errExecute != nil {
		t.Fatalf("second Execute() error = %v", errExecute)
	}
	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(upstreamBodies))
	}
	gotContent := gjson.GetBytes(upstreamBodies[1], "messages.1.content")
	if !kimiJSONEqual([]byte(gotContent.Raw), []byte(cachedContent)) {
		t.Fatalf("second upstream assistant content = %s, want %s", gotContent.Raw, cachedContent)
	}
	if _, found, errGet := internalcache.GetKimiThinkingReplayRequired(context.Background(), "k3", "execution:nonstream-switch"); errGet != nil || found {
		t.Fatalf("unsigned completed turn left stale replay: found %v, error %v", found, errGet)
	}
}

func TestShouldClearKimiThinkingReplayAfterErrorOnlyForUpstreamRequestRejection(t *testing.T) {
	if shouldClearKimiThinkingReplayAfterError(errors.New("transport failed")) {
		t.Fatal("transport error must not clear valid replay")
	}
	if shouldClearKimiThinkingReplayAfterError(kimiLocalBadRequestError{}) {
		t.Fatal("local bad request must not clear valid replay")
	}
	if shouldClearKimiThinkingReplayAfterError(statusErr{code: http.StatusInternalServerError}) {
		t.Fatal("upstream server error must not clear valid replay")
	}
	if !shouldClearKimiThinkingReplayAfterError(statusErr{code: http.StatusBadRequest}) {
		t.Fatal("upstream bad request should clear applied replay")
	}
	if !shouldClearKimiThinkingReplayAfterError(statusErr{code: http.StatusUnprocessableEntity}) {
		t.Fatal("upstream unprocessable request should clear applied replay")
	}
}

func TestKimiExecutorClaudeErrorClearsAppliedReplay(t *testing.T) {
	internalcache.ClearKimiThinkingReplayCache()
	t.Cleanup(internalcache.ClearKimiThinkingReplayCache)

	const sessionKey = "execution:error-clears-replay"
	const cachedContent = `[{"type":"thinking","thinking":"reasoning","signature":"kimi-signature"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]`
	if !internalcache.CacheKimiThinkingReplayBestEffort(context.Background(), "k3", sessionKey, []byte(cachedContent)) {
		t.Fatal("failed to seed replay cache")
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid thinking signature"}}`)),
		}, nil
	}))
	executor := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{}, Metadata: map[string]any{"access_token": "test-token"}}
	payload := []byte(`{"model":"kimi-k3-256k","max_tokens":32,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`)
	_, errExecute := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k3-256k", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "error-clears-replay",
		},
	})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want upstream rejection")
	}
	if _, found, errGet := internalcache.GetKimiThinkingReplayRequired(context.Background(), "k3", sessionKey); errGet != nil || found {
		t.Fatalf("rejected replay remained cached: found %v, error %v", found, errGet)
	}
}

func TestKimiExecutorClaudeStreamReplaysThinkingAcrossK3VariantSwitch(t *testing.T) {
	internalcache.ClearKimiThinkingReplayCache()
	t.Cleanup(internalcache.ClearKimiThinkingReplayCache)

	const firstStream = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"k3","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"stream reasoning"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"stream-signature"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_stream","name":"Read","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"README.md\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	const secondStream = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"k3-256k","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	var upstreamBodies [][]byte
	callCount := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		upstreamBodies = append(upstreamBodies, body)
		callCount++
		stream := firstStream
		if callCount == 2 {
			stream = secondStream
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(stream)),
		}, nil
	}))

	executor := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{}, Metadata: map[string]any{"access_token": "test-token"}}
	opts := cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "stream-switch",
		},
	}
	firstPayload := []byte(`{"model":"kimi-k3","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"inspect"}]}`)
	opts.OriginalRequest = firstPayload
	firstResult, errExecute := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k3", Payload: firstPayload}, opts)
	if errExecute != nil {
		t.Fatalf("first ExecuteStream() error = %v", errExecute)
	}
	consumeKimiReplayStream(t, firstResult)

	secondPayload := []byte(`{"model":"kimi-k3-256k","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"inspect"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_stream","name":"Read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_stream","content":"ok"}]}]}`)
	opts.OriginalRequest = secondPayload
	secondResult, errExecute := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "kimi-k3-256k", Payload: secondPayload}, opts)
	if errExecute != nil {
		t.Fatalf("second ExecuteStream() error = %v", errExecute)
	}
	consumeKimiReplayStream(t, secondResult)

	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(upstreamBodies))
	}
	content := gjson.GetBytes(upstreamBodies[1], "messages.1.content")
	if got := content.Get("0.thinking").String(); got != "stream reasoning" {
		t.Fatalf("replayed stream thinking = %q, want stream reasoning; content=%s", got, content.Raw)
	}
	if got := content.Get("0.signature").String(); got != "stream-signature" {
		t.Fatalf("replayed stream signature = %q, want stream-signature; content=%s", got, content.Raw)
	}
	if got := content.Get("1.input.path").String(); got != "README.md" {
		t.Fatalf("replayed stream tool input path = %q, want README.md; content=%s", got, content.Raw)
	}
}

func TestKimiThinkingReplayUnknownStreamDeltaPreservesPreviousCache(t *testing.T) {
	internalcache.ClearKimiThinkingReplayCache()
	t.Cleanup(internalcache.ClearKimiThinkingReplayCache)

	const sessionID = "unknown-stream-delta"
	const sessionKey = "execution:" + sessionID
	cached := []byte(`[{"type":"thinking","thinking":"reasoning","signature":"kimi-signature"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]`)
	if !internalcache.CacheKimiThinkingReplayBestEffort(context.Background(), "k3", sessionKey, cached) {
		t.Fatal("failed to seed replay cache")
	}
	payload := []byte(`{"model":"kimi-k3-256k","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"README.md"}}]}]}`)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	_, scope := prepareKimiThinkingReplayRequest(context.Background(), cliproxyexecutor.Request{Model: "kimi-k3-256k", Payload: payload}, opts)
	if !scope.replayApplied {
		t.Fatal("expected seeded replay to be applied")
	}

	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(
		"event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"msg_1","model":"k3"}}` + "\n\n" +
			"event: content_block_start\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
			"event: content_block_delta\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"future_delta","value":"new"}}` + "\n\n" +
			"event: content_block_stop\n" +
			`data: {"type":"content_block_stop","index":0}` + "\n\n" +
			"event: message_stop\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	)}
	close(chunks)
	consumeKimiReplayStream(t, wrapKimiThinkingReplayStream(context.Background(), &cliproxyexecutor.StreamResult{Chunks: chunks}, scope))

	got, found, errGet := internalcache.GetKimiThinkingReplayRequired(context.Background(), "k3", sessionKey)
	if errGet != nil || !found || !kimiJSONEqual(got, cached) {
		t.Fatalf("unknown successful delta changed previous cache: got %s, found %v, error %v", got, found, errGet)
	}
}

func consumeKimiReplayStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()
	if result == nil {
		t.Fatal("stream result is nil")
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}
}
