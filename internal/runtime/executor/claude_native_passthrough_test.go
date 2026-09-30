package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type nativeClaudePassthroughTransport func(*http.Request) (*http.Response, error)

func (fn nativeClaudePassthroughTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func nativeClaudePassthroughAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "selected-subscription",
		Provider: "claude",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"access_token":  "sk-ant-oat-selected-subscription",
			"refresh_token": "refresh-selected-subscription",
			"account_uuid":  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{
				"0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}
}

func nativeClaudePassthroughContext(headers http.Header, roundTripper http.RoundTripper) context.Context {
	ctx := cliproxyexecutor.WithNativeClaudeProtocolHeaders(context.Background(), headers)
	return context.WithValue(ctx, "cliproxy.roundtripper", roundTripper)
}

func TestNativeClaudeExecutePreservesRequestAndRawResponseRepresentation(t *testing.T) {
	auth := nativeClaudePassthroughAuth()
	const sessionID = "11111111-2222-4333-8444-555555555555"
	body := []byte("{\n  \"model\": \"claude-future-9\",\n  \"messages\": [{\"role\":\"assistant\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"opaque\",\"signature\":\"signed-value\"}]},{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"tool-1\",\"content\":\"ok\"}]}],\n  \"system\": [{\"type\":\"text\",\"text\":\"x-anthropic-billing-header: cc_version=99.7.1; cc_entrypoint=cli; cch=00000;\"}],\n  \"metadata\": {\"user_id\":\"{\\\"device_id\\\":\\\"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\\\",\\\"account_uuid\\\":\\\"master-account\\\",\\\"session_id\\\":\\\"" + sessionID + "\\\",\\\"future_identity_field\\\":true}\"},\n  \"temperature\": 0.2, \"top_p\": 0.3, \"future_option\": {\"enabled\":true}, \"stream\": false\n}\n")
	nativeHeaders := http.Header{
		"Accept":                       {"application/json"},
		"Accept-Encoding":              {"gzip, deflate, br, zstd, future"},
		"Anthropic-Beta":               {"future-beta-2099-01-01"},
		"Anthropic-Version":            {"2023-06-01"},
		"Content-Type":                 {"application/json"},
		"Cookie":                       {"master-cookie-must-not-pass"},
		"User-Agent":                   {"claude-cli/99.7.1 (external, cli)"},
		"X-Claude-Code-Future-Feature": {"opaque-value"},
		"X-Claude-Code-Session-Id":     {sessionID},
	}
	rawResponse := []byte{0x1f, 0x8b, 0x08, 0x00, 0xde, 0xad, 0xbe, 0xef}
	responseHeaders := http.Header{
		"Content-Encoding":          {"gzip"},
		"Content-Length":            {"8"},
		"Content-Type":              {"application/json"},
		"X-Anthropic-Future-Header": {"kept"},
	}

	var sentBody []byte
	var sentHeaders http.Header
	roundTripper := nativeClaudePassthroughTransport(func(got *http.Request) (*http.Response, error) {
		if got.Method != http.MethodPost || got.URL.String() != nativeClaudeMessagesURL {
			t.Fatalf("upstream target = %s %s, want POST %s", got.Method, got.URL, nativeClaudeMessagesURL)
		}
		var errRead error
		sentBody, errRead = io.ReadAll(got.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		sentHeaders = got.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     responseHeaders.Clone(),
			Body:       io.NopCloser(bytes.NewReader(rawResponse)),
			Request:    got,
		}, nil
	})
	ctx := nativeClaudePassthroughContext(nativeHeaders, roundTripper)

	expectedBody, _, errIdentity := helps.ApplyClaudeCredentialMetadata(body, auth, sessionID)
	if errIdentity != nil {
		t.Fatal(errIdentity)
	}
	expectedBody, errSign := signAnthropicMessagesBody(expectedBody)
	if errSign != nil {
		t.Fatal(errSign)
	}
	response, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "must-not-replace-native-model",
		Payload: body,
	}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if !bytes.Equal(sentBody, expectedBody) {
		t.Fatalf("upstream body changed beyond credential identity/CCH\n got: %s\nwant: %s", sentBody, expectedBody)
	}
	if bytes.Contains(sentBody, []byte("cch=00000")) {
		t.Fatalf("upstream CCH was not re-signed: %s", sentBody)
	}
	if got := sentHeaders.Get("Authorization"); got != "Bearer sk-ant-oat-selected-subscription" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := sentHeaders.Get("Accept-Encoding"); got != nativeHeaders.Get("Accept-Encoding") {
		t.Fatalf("Accept-Encoding = %q, want exact %q", got, nativeHeaders.Get("Accept-Encoding"))
	}
	if got := sentHeaders.Get("X-Claude-Code-Future-Feature"); got != "opaque-value" {
		t.Fatalf("future header = %q, want preserved", got)
	}
	if got := sentHeaders.Get("User-Agent"); got != nativeHeaders.Get("User-Agent") {
		t.Fatalf("User-Agent = %q, want %q", got, nativeHeaders.Get("User-Agent"))
	}
	if got := sentHeaders.Get("Cookie"); got != "" {
		t.Fatalf("master Cookie leaked upstream: %q", got)
	}
	wantHeaders := cliproxyexecutor.NativeClaudeProtocolHeaders(nativeHeaders)
	wantHeaders.Set("Authorization", "Bearer sk-ant-oat-selected-subscription")
	if !reflect.DeepEqual(sentHeaders, wantHeaders) {
		t.Fatalf("upstream headers changed beyond auth substitution\n got: %#v\nwant: %#v", sentHeaders, wantHeaders)
	}
	if !bytes.Equal(response.Payload, rawResponse) {
		t.Fatalf("response payload = %x, want raw %x", response.Payload, rawResponse)
	}
	if !reflect.DeepEqual(response.Headers, responseHeaders) {
		t.Fatalf("response headers = %#v, want %#v", response.Headers, responseHeaders)
	}
}

func TestNativeClaudeCountTokensPreservesBodyHeadersAndRawError(t *testing.T) {
	auth := nativeClaudePassthroughAuth()
	body := []byte("{ \"future_count_shape\": true, \"messages\": [], \"model\": \"claude-future-9\" }\n")
	nativeHeaders := http.Header{
		"Accept":                  {"application/json"},
		"Accept-Encoding":         {"br"},
		"Anthropic-Beta":          {"token-counting-future"},
		"Content-Type":            {"application/json"},
		"X-Claude-Code-Prompt-Id": {"prompt-future"},
	}
	rawError := []byte{0xce, 0xb2, 0x01, 0x02, 0x03}
	errorHeaders := http.Header{
		"Content-Encoding":                          {"br"},
		"Content-Length":                            {"5"},
		"Content-Type":                              {"application/json"},
		"Anthropic-Ratelimit-Unified-7d-Status":     {"rejected"},
		"Anthropic-Ratelimit-Unified-7d-Reset":      {"2099-01-01T00:00:00Z"},
		"X-Anthropic-Future-Rate-Limit-Explanation": {"opaque"},
	}
	var sentBody []byte
	roundTripper := nativeClaudePassthroughTransport(func(got *http.Request) (*http.Response, error) {
		if got.URL.String() != nativeClaudeCountTokensURL {
			t.Fatalf("upstream URL = %s, want %s", got.URL, nativeClaudeCountTokensURL)
		}
		if got.Header.Get("Accept-Encoding") != "br" {
			t.Fatalf("Accept-Encoding = %q, want br", got.Header.Get("Accept-Encoding"))
		}
		if got.Header.Get("X-Claude-Code-Prompt-Id") != "prompt-future" {
			t.Fatalf("future prompt header was not forwarded: %#v", got.Header)
		}
		sentBody, _ = io.ReadAll(got.Body)
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     errorHeaders.Clone(),
			Body:       io.NopCloser(bytes.NewReader(rawError)),
			Request:    got,
		}, nil
	})
	ctx := nativeClaudePassthroughContext(nativeHeaders, roundTripper)
	_, errCount := NewClaudeExecutor(&config.Config{}).CountTokens(ctx, auth, cliproxyexecutor.Request{
		Model:   "ignored-routing-model",
		Payload: body,
	}, cliproxyexecutor.Options{})
	if !bytes.Equal(sentBody, body) {
		t.Fatalf("count_tokens body = %q, want exact %q", sentBody, body)
	}
	var direct *claudeNativeDirectResponseError
	if !errors.As(errCount, &direct) || direct == nil {
		t.Fatalf("CountTokens() error = %T %v, want native direct response", errCount, errCount)
	}
	if direct.StatusCode() != http.StatusTooManyRequests || !direct.IsCredentialScoped() || !direct.DirectResponse() {
		t.Fatalf("direct error classification = status %d credential=%v direct=%v", direct.StatusCode(), direct.IsCredentialScoped(), direct.DirectResponse())
	}
	if !bytes.Equal(direct.ResponseBody(), rawError) {
		t.Fatalf("direct error body = %x, want raw %x", direct.ResponseBody(), rawError)
	}
	if !reflect.DeepEqual(direct.ResponseHeaders(), errorHeaders) {
		t.Fatalf("direct error headers = %#v, want %#v", direct.ResponseHeaders(), errorHeaders)
	}
}

func TestNativeClaudeCountTokensStripsOnlyPresentMetadataIdentity(t *testing.T) {
	auth := nativeClaudePassthroughAuth()
	const sessionID = "44444444-5555-4666-8777-888888888888"
	body := []byte(`{"model":"claude-future-9","messages":[],"metadata":{"user_id":"{\"device_id\":\"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\",\"account_uuid\":\"master-account\",\"session_id\":\"` + sessionID + `\",\"future_extra\":true}","future_metadata":{"keep":true}},"future_count_shape":true}`)
	nativeHeaders := http.Header{
		"Accept":                   {"application/json"},
		"Accept-Encoding":          {"identity"},
		"Content-Type":             {"application/json"},
		"X-Claude-Code-Session-Id": {sessionID},
	}
	var sentBody []byte
	roundTripper := nativeClaudePassthroughTransport(func(got *http.Request) (*http.Response, error) {
		sentBody, _ = io.ReadAll(got.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"input_tokens":7}`)),
			Request:    got,
		}, nil
	})
	ctx := nativeClaudePassthroughContext(nativeHeaders, roundTripper)
	response, errCount := NewClaudeExecutor(&config.Config{}).CountTokens(ctx, auth, cliproxyexecutor.Request{
		Model:   "ignored-routing-model",
		Payload: body,
	}, cliproxyexecutor.Options{})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if bytes.Contains(sentBody, []byte("master-account")) || gjson.GetBytes(sentBody, "metadata.user_id").Exists() {
		t.Fatalf("master credential identity leaked: %s", sentBody)
	}
	if !gjson.GetBytes(sentBody, "metadata.future_metadata.keep").Bool() || !gjson.GetBytes(sentBody, "future_count_shape").Bool() {
		t.Fatalf("unknown count metadata was not preserved: %s", sentBody)
	}
	if got := string(response.Payload); got != `{"input_tokens":7}` {
		t.Fatalf("response payload = %q, want raw count response", got)
	}
}

type nativeClaudeScriptedBody struct {
	chunks [][]byte
	closed *atomic.Bool
}

func (body *nativeClaudeScriptedBody) Read(dst []byte) (int, error) {
	if len(body.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(dst, body.chunks[0])
	body.chunks = body.chunks[1:]
	return n, nil
}

func (body *nativeClaudeScriptedBody) Close() error {
	body.closed.Store(true)
	return nil
}

func TestNativeClaudeStreamRelaysOddSSEReadsExactly(t *testing.T) {
	auth := nativeClaudePassthroughAuth()
	const sessionID = "22222222-3333-4444-8555-666666666666"
	requestBody := []byte(`{"model":"claude-future-9","messages":[{"role":"user","content":"go"}],"metadata":{"user_id":"{\"device_id\":\"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\",\"account_uuid\":\"master\",\"session_id\":\"` + sessionID + `\"}"},"stream":true}`)
	nativeHeaders := http.Header{
		"Accept":                   {"text/event-stream"},
		"Accept-Encoding":          {"identity"},
		"Content-Type":             {"application/json"},
		"X-Claude-Code-Session-Id": {sessionID},
		"X-Future-Stream-Control":  {"keep"},
	}
	chunks := [][]byte{
		[]byte(": keep-alive\r\n"),
		[]byte("event: message_start\r"),
		[]byte("\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\r\n\r\n"),
		[]byte("event: custom\r\ndata: first line\r\n"),
		[]byte("data: second line\r\n\r\n"),
		[]byte("event: content_block_delta\r\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"event: error\"}}\r\n\r\n"),
		[]byte("event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n"),
	}
	var closed atomic.Bool
	roundTripper := nativeClaudePassthroughTransport(func(got *http.Request) (*http.Response, error) {
		if got.Header.Get("X-Future-Stream-Control") != "keep" || got.Header.Get("Accept-Encoding") != "identity" {
			t.Fatalf("stream headers changed: %#v", got.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":              {"text/event-stream"},
				"X-Future-Response-Control": {"keep"},
			},
			Body:    &nativeClaudeScriptedBody{chunks: append([][]byte(nil), chunks...), closed: &closed},
			Request: got,
		}, nil
	})
	ctx := nativeClaudePassthroughContext(nativeHeaders, roundTripper)
	result, errStream := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "must-not-replace-native-model",
		Payload: requestBody,
	}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	if got := result.Headers.Get("X-Future-Response-Control"); got != "keep" {
		t.Fatalf("response header = %q, want keep", got)
	}
	var gotChunks [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		gotChunks = append(gotChunks, bytes.Clone(chunk.Payload))
	}
	if !reflect.DeepEqual(gotChunks, chunks) {
		t.Fatalf("stream chunks changed\n got: %#v\nwant: %#v", gotChunks, chunks)
	}
	if !closed.Load() {
		t.Fatal("upstream stream body was not closed")
	}
}

func TestNativeClaudeStreamSplitErrorAndFollowingFrameAreBothTransparent(t *testing.T) {
	auth := nativeClaudePassthroughAuth()
	const sessionID = "33333333-4444-4555-8666-777777777777"
	requestBody := []byte(`{"model":"claude-future-9","messages":[{"role":"user","content":"go"}],"metadata":{"user_id":"{\"device_id\":\"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\",\"account_uuid\":\"master\",\"session_id\":\"` + sessionID + `\"}"},"stream":true}`)
	nativeHeaders := http.Header{
		"Accept":                   {"text/event-stream"},
		"Accept-Encoding":          {"identity"},
		"Content-Type":             {"application/json"},
		"X-Claude-Code-Session-Id": {sessionID},
	}
	chunks := [][]byte{
		[]byte("event: err"),
		[]byte("or\r\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"weekly limit\",\"status_code\":429}}\r\n"),
		[]byte("\r\nevent: message_stop\r\n"),
		[]byte("data: {\"type\":\"message_stop\"}\r\n\r\n"),
	}
	responseHeaders := http.Header{
		"Content-Type":                          {"text/event-stream"},
		"Anthropic-Ratelimit-Unified-7d-Status": {"rejected"},
	}
	var closed atomic.Bool
	roundTripper := nativeClaudePassthroughTransport(func(got *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     responseHeaders.Clone(),
			Body: &nativeClaudeScriptedBody{
				chunks: append([][]byte(nil), chunks...),
				closed: &closed,
			},
			Request: got,
		}, nil
	})
	ctx := nativeClaudePassthroughContext(nativeHeaders, roundTripper)
	result, errStream := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "ignored-routing-model",
		Payload: requestBody,
	}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var gotChunks [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("transparent stream returned side-band error: %v", chunk.Err)
		}
		gotChunks = append(gotChunks, bytes.Clone(chunk.Payload))
	}
	if !reflect.DeepEqual(gotChunks, chunks) {
		t.Fatalf("split error stream changed\n got: %#v\nwant: %#v", gotChunks, chunks)
	}
	if !closed.Load() {
		t.Fatal("upstream stream body was not closed")
	}
}
