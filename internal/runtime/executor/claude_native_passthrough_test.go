package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

func TestNativeClaudeFastFailuresRetainRequestScopeAndRawResponse(t *testing.T) {
	for _, operation := range []string{"execute", "stream"} {
		for _, testCase := range []struct {
			name      string
			status    int
			betaOnly  bool
			quota     bool
			transport bool
		}{
			{name: "entitlement", status: http.StatusForbidden},
			{name: "fast_credits", status: http.StatusTooManyRequests},
			{name: "beta_only", status: http.StatusServiceUnavailable, betaOnly: true},
			{name: "subscription_quota", status: http.StatusTooManyRequests, quota: true},
			{name: "transport_failure", transport: true},
		} {
			t.Run(operation+"/"+testCase.name, func(t *testing.T) {
				headers := http.Header{"Content-Type": {"application/json"}, "X-Future-Error-Header": {"one", "two"}}
				if testCase.quota {
					headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
				}
				body := []byte{0x1f, 0x8b, 0x08, 0x00, 0xde, 0xad}
				const transportError = "synthetic Fast connection failure"
				var attempts atomic.Int32
				transport := nativeClaudePassthroughTransport(func(request *http.Request) (*http.Response, error) {
					attempts.Add(1)
					if testCase.transport {
						return nil, errors.New(transportError)
					}
					return &http.Response{StatusCode: testCase.status, Header: headers.Clone(), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
				})
				nativeHeaders := http.Header{"Content-Type": {"application/json"}}
				payload := []byte(`{"model":"claude-opus-5","max_tokens":16,"speed":"fast","messages":[]}`)
				if testCase.betaOnly {
					nativeHeaders.Set("Anthropic-Beta", "fast-mode-2026-02-01")
					payload = []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[]}`)
				}
				ctx := nativeClaudePassthroughContext(nativeHeaders, transport)
				executor := NewClaudeExecutor(&config.Config{})
				request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
				var errExecute error
				if operation == "stream" {
					_, errExecute = executor.ExecuteStream(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{Stream: true})
				} else {
					_, errExecute = executor.Execute(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{})
				}
				if errExecute == nil || attempts.Load() != 1 {
					t.Fatalf("Fast failure missing or replayed: err=%v attempts=%d", errExecute, attempts.Load())
				}
				var scope cliproxyexecutor.RequestScopedError
				if !errors.As(errExecute, &scope) || scope.IsRequestScoped() == testCase.quota {
					t.Fatalf("Fast request scope = %v, want request-scoped=%t", errExecute, !testCase.quota)
				}
				var credentialScope interface{ IsCredentialScoped() bool }
				if !errors.As(errExecute, &credentialScope) || credentialScope.IsCredentialScoped() != testCase.quota {
					t.Fatalf("Fast credential scope changed: %v", errExecute)
				}
				if testCase.transport {
					if !strings.Contains(errExecute.Error(), transportError) {
						t.Fatal("Fast transport error lost its cause")
					}
					return
				}
				var direct *claudeNativeDirectResponseError
				if !errors.As(errExecute, &direct) || direct.StatusCode() != testCase.status || !bytes.Equal(direct.ResponseBody(), body) || !reflect.DeepEqual(direct.ResponseHeaders(), headers) {
					t.Fatalf("raw native Fast response changed: %v", errExecute)
				}
			})
		}
	}
}

func TestNativeClaudeOAuthCountRejectsDuplicateIdentityContainers(t *testing.T) {
	for _, payload := range []string{
		`{"model":"claude-opus-5","messages":[],"metadata":{"user_id":"master-first"},"metadata":{"user_id":"master-second"}}`,
		`{"model":"claude-opus-5","messages":[],"metadata":{"user_id":"master-first","user_id":"master-second"}}`,
		`{"model":"claude-opus-5","messages":[],"metadata":{"user_id":"master-first","user\u005fid":"master-second"}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			var attempts atomic.Int32
			transport := nativeClaudePassthroughTransport(func(*http.Request) (*http.Response, error) {
				attempts.Add(1)
				return nil, errors.New("identity request unexpectedly reached upstream")
			})
			ctx := nativeClaudePassthroughContext(http.Header{"Content-Type": {"application/json"}}, transport)
			_, errCount := NewClaudeExecutor(&config.Config{}).CountTokens(ctx, nativeClaudePassthroughAuth(), cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(payload)}, cliproxyexecutor.Options{})
			var status interface{ StatusCode() int }
			var scope cliproxyexecutor.RequestScopedError
			if errCount == nil || !errors.As(errCount, &status) || status.StatusCode() != http.StatusBadRequest || !errors.As(errCount, &scope) || !scope.IsRequestScoped() || attempts.Load() != 0 {
				t.Fatalf("ambiguous count identity not rejected locally: err=%v attempts=%d", errCount, attempts.Load())
			}
		})
	}
}

func TestNativeClaudeFastLocalMetadataFailureRetainsStatus(t *testing.T) {
	for _, operation := range []string{"execute", "stream"} {
		t.Run(operation, func(t *testing.T) {
			var attempts atomic.Int32
			transport := nativeClaudePassthroughTransport(func(*http.Request) (*http.Response, error) {
				attempts.Add(1)
				return nil, errors.New("ambiguous identity unexpectedly reached upstream")
			})
			ctx := nativeClaudePassthroughContext(http.Header{}, transport)
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","speed":"fast","messages":[],"metadata":{"user_id":"one"},"metadata":{"user_id":"two"}}`)}
			executor := NewClaudeExecutor(&config.Config{})
			var errExecute error
			if operation == "stream" {
				_, errExecute = executor.ExecuteStream(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{Stream: true})
			} else {
				_, errExecute = executor.Execute(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{})
			}
			var status interface{ StatusCode() int }
			if !errors.As(errExecute, &status) || status.StatusCode() != http.StatusBadRequest || attempts.Load() != 0 {
				t.Fatalf("native Fast validation masked local request status: err=%v attempts=%d", errExecute, attempts.Load())
			}
		})
	}
}

type nativeClaudeObservedResponseBody struct {
	reader io.Reader
	reads  atomic.Int32
}

func (b *nativeClaudeObservedResponseBody) Read(buffer []byte) (int, error) {
	b.reads.Add(1)
	return b.reader.Read(buffer)
}

func (*nativeClaudeObservedResponseBody) Close() error { return nil }

func TestNativeClaudeUpstreamSuccessObservationOrdering(t *testing.T) {
	for _, operation := range []string{"execute", "stream", "count"} {
		for _, failure := range []string{"none", "http", "body", "transport"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				const rawResponse = "data: native-response\r\n\r\n"
				body := &nativeClaudeObservedResponseBody{reader: strings.NewReader(rawResponse)}
				if failure == "body" {
					body.reader = &nativeClaudeFailedStreamBody{payload: []byte(rawResponse)}
				}
				var observations atomic.Int32
				transport := nativeClaudePassthroughTransport(func(request *http.Request) (*http.Response, error) {
					if failure == "transport" {
						return nil, errors.New("synthetic connection failure")
					}
					status := http.StatusOK
					if failure == "http" {
						status = http.StatusForbidden
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body, Request: request}, nil
				})
				ctx := nativeClaudePassthroughContext(http.Header{"Content-Type": {"application/json"}}, transport)
				ctx = cliproxyexecutor.WithNativeClaudeUpstreamSuccessCallback(ctx, func(_ context.Context, authID string) {
					if authID != "selected-subscription" {
						t.Error("success observation did not identify the selected subscription")
					}
					if operation == "stream" && body.reads.Load() != 0 {
						t.Error("stream success observation waited for the response body")
					}
					if operation == "execute" && body.reads.Load() == 0 {
						t.Error("non-stream success observation preceded the complete response")
					}
					observations.Add(1)
				})
				executor := NewClaudeExecutor(&config.Config{})
				request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","messages":[]}`)}
				var received []byte
				var errExecute error
				switch operation {
				case "stream":
					var stream *cliproxyexecutor.StreamResult
					stream, errExecute = executor.ExecuteStream(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{Stream: true})
					if errExecute == nil {
						for chunk := range stream.Chunks {
							received = append(received, chunk.Payload...)
							if chunk.Err != nil {
								errExecute = chunk.Err
							}
						}
					}
				case "execute":
					var response cliproxyexecutor.Response
					response, errExecute = executor.Execute(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{})
					received = response.Payload
				case "count":
					var response cliproxyexecutor.Response
					response, errExecute = executor.CountTokens(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{})
					received = response.Payload
				}
				wantObservation := int32(0)
				if operation != "count" && (failure == "none" || operation == "stream" && failure == "body") {
					wantObservation = 1
				}
				if observations.Load() != wantObservation || (errExecute == nil) != (failure == "none") {
					t.Fatalf("success observation=%d want=%d error=%v", observations.Load(), wantObservation, errExecute)
				}
				if failure == "none" && string(received) != rawResponse {
					t.Fatal("success observer changed the raw response")
				}
			})
		}
	}
}

func TestNativeClaudeSuccessfulHTTPStatusIsPreserved(t *testing.T) {
	for _, operation := range []string{"execute", "stream", "count"} {
		for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				body := "raw native response"
				if status == http.StatusNoContent {
					body = ""
				}
				transport := nativeClaudePassthroughTransport(func(request *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
				})
				ctx := cliproxyexecutor.WithNativeClaudeResponseStatusHolder(nativeClaudePassthroughContext(http.Header{}, transport))
				request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","messages":[]}`)}
				executor := NewClaudeExecutor(&config.Config{})
				var payload []byte
				var errExecute error
				switch operation {
				case "execute":
					var response cliproxyexecutor.Response
					response, errExecute = executor.Execute(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{})
					payload = response.Payload
				case "count":
					var response cliproxyexecutor.Response
					response, errExecute = executor.CountTokens(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{})
					payload = response.Payload
				case "stream":
					var stream *cliproxyexecutor.StreamResult
					stream, errExecute = executor.ExecuteStream(ctx, nativeClaudePassthroughAuth(), request, cliproxyexecutor.Options{Stream: true})
					if errExecute == nil {
						for chunk := range stream.Chunks {
							payload = append(payload, chunk.Payload...)
							if chunk.Err != nil {
								errExecute = chunk.Err
							}
						}
					}
				}
				if errExecute != nil || string(payload) != body || cliproxyexecutor.NativeClaudeResponseStatusFromContext(ctx) != status {
					t.Fatalf("native success changed status/body: status=%d body=%q error=%v", cliproxyexecutor.NativeClaudeResponseStatusFromContext(ctx), payload, errExecute)
				}
			})
		}
	}
}

type nativeClaudeFailedStreamBody struct {
	payload []byte
}

func (b *nativeClaudeFailedStreamBody) Read(buffer []byte) (int, error) {
	if len(b.payload) > 0 {
		count := copy(buffer, b.payload)
		b.payload = b.payload[count:]
		return count, nil
	}
	return 0, io.ErrUnexpectedEOF
}

func (*nativeClaudeFailedStreamBody) Close() error { return nil }

func TestNativeClaudeFastStreamReadFailureRetainsRequestScope(t *testing.T) {
	const rawPrefix = "event: content_block_delta\r\ndata: opaque-fragment"
	transport := nativeClaudePassthroughTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       &nativeClaudeFailedStreamBody{payload: []byte(rawPrefix)},
			Request:    request,
		}, nil
	})
	ctx := nativeClaudePassthroughContext(http.Header{"Content-Type": {"application/json"}}, transport)
	stream, errStream := NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx, nativeClaudePassthroughAuth(), cliproxyexecutor.Request{
		Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","speed":"fast","stream":true,"messages":[]}`),
	}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatal(errStream)
	}
	var received []byte
	var terminal error
	for chunk := range stream.Chunks {
		received = append(received, chunk.Payload...)
		if chunk.Err != nil {
			terminal = chunk.Err
		}
	}
	var scope cliproxyexecutor.RequestScopedError
	if string(received) != rawPrefix || !errors.Is(terminal, io.ErrUnexpectedEOF) || !errors.As(terminal, &scope) || !scope.IsRequestScoped() {
		t.Fatalf("Fast stream failure changed bytes or availability scope: payload=%q err=%v", received, terminal)
	}
}

func TestNativeClaudeAPIKeyPreservesProtocolWithoutSubscriptionIdentity(t *testing.T) {
	const sessionID = "11111111-2222-4333-8444-555555555555"
	auth := &cliproxyauth.Auth{
		ID:       "final-api-key-backup",
		Provider: "claude",
		Attributes: map[string]string{
			"auth_kind": "api_key",
			"api_key":   "sk-ant-api03-selected-backup",
		},
	}
	identity := `"user_id":"{\"device_id\":\"master-device\",\"account_uuid\":\"master-account\",\"session_id\":\"` + sessionID + `\"}"`
	for _, operation := range []string{"execute", "stream", "count"} {
		for _, metadata := range []struct {
			name  string
			value string
		}{
			{name: "identity-only", value: `,"metadata":{` + identity + `}`},
			{name: "unknown-siblings", value: `,"metadata":{` + identity + `,"future_metadata":{"keep":true}}`},
			{name: "no-metadata"},
		} {
			t.Run(operation+"/"+metadata.name, func(t *testing.T) {
				body := []byte("{\n  \"model\":\"claude-future-9\",\"messages\":[{\"role\":\"user\",\"content\":\"go\"}],\"system\":[{\"type\":\"text\",\"text\":\"x-anthropic-billing-header: cc_version=99.7.1; cc_entrypoint=cli; cch=00000;\"}],\"future_option\":{\"keep\":true},\"stream\":" + fmt.Sprint(operation == "stream") + metadata.value + "\n}\n")
				expectedBody := body
				if metadata.value != "" {
					var errDelete error
					expectedBody, errDelete = sjson.DeleteBytes(expectedBody, "metadata.user_id")
					if errDelete != nil {
						t.Fatal(errDelete)
					}
					if metadata.name == "identity-only" {
						expectedBody, errDelete = sjson.DeleteBytes(expectedBody, "metadata")
						if errDelete != nil {
							t.Fatal(errDelete)
						}
					}
				}
				expectedBody, errSign := signAnthropicMessagesBody(expectedBody)
				if errSign != nil {
					t.Fatal(errSign)
				}
				nativeHeaders := http.Header{
					"Authorization":                {"Bearer master-token"},
					"X-Api-Key":                    {"master-key"},
					"Anthropic-Organization-Id":    {"master-organization"},
					"Accept-Encoding":              {"gzip, br, future"},
					"Anthropic-Beta":               {"native-future-beta, oauth-2025-04-20,future-with-oauth-2025-04-20-suffix", "oauth-2025-04-20", "repeat,repeat,oauth-2025-04-20,future-last"},
					"Content-Type":                 {"application/json"},
					"X-Claude-Code-Session-Id":     {sessionID},
					"X-Claude-Code-Future-Feature": {"keep"},
				}
				wantBetaValues := []string{"native-future-beta,future-with-oauth-2025-04-20-suffix", "repeat,repeat,future-last"}
				if metadata.name == "identity-only" {
					nativeHeaders.Set("Anthropic-Beta", " oauth-2025-04-20 ")
					nativeHeaders.Del("X-Claude-Code-Session-Id")
					wantBetaValues = nil
				} else if metadata.name == "no-metadata" {
					nativeHeaders.Del("Anthropic-Beta")
					wantBetaValues = nil
				}
				rawResponse := []byte{0x1f, 0x8b, 0x08, 0x00, 0xde, 0xad, 0xbe, 0xef}
				responseHeaders := http.Header{
					"Content-Encoding":          {"gzip"},
					"Content-Type":              {"application/json"},
					"X-Anthropic-Future-Header": {"keep"},
				}
				chunks := [][]byte{[]byte(": keep-alive\r"), []byte("\ndata: opaque\r\n\r\n"), []byte("event: err"), []byte("or\ndata: unchanged\n\n")}
				var closed atomic.Bool
				roundTripper := nativeClaudePassthroughTransport(func(got *http.Request) (*http.Response, error) {
					wantURL := nativeClaudeMessagesURL
					if operation == "count" {
						wantURL = nativeClaudeCountTokensURL
					}
					if got.Method != http.MethodPost || got.URL.String() != wantURL {
						t.Fatalf("upstream target = %s %s, want POST %s", got.Method, got.URL, wantURL)
					}
					sentBody, errRead := io.ReadAll(got.Body)
					if errRead != nil {
						t.Fatal(errRead)
					}
					if !bytes.Equal(sentBody, expectedBody) {
						t.Fatalf("body changed beyond identity removal/CCH\n got: %s\nwant: %s", sentBody, expectedBody)
					}
					wantHeaders := cliproxyexecutor.NativeClaudeProtocolHeaders(nativeHeaders)
					wantHeaders.Del("Anthropic-Beta")
					if len(wantBetaValues) > 0 {
						wantHeaders["Anthropic-Beta"] = wantBetaValues
					}
					wantHeaders.Set("X-Api-Key", "sk-ant-api03-selected-backup")
					wantHeaders.Set("X-Claude-Code-Session-Id", sessionID)
					if !reflect.DeepEqual(got.Header, wantHeaders) {
						t.Fatalf("headers changed beyond selected API key\n got: %#v\nwant: %#v", got.Header, wantHeaders)
					}
					var responseBody io.ReadCloser = io.NopCloser(bytes.NewReader(rawResponse))
					if operation == "stream" {
						responseBody = &nativeClaudeScriptedBody{chunks: append([][]byte(nil), chunks...), closed: &closed}
					}
					return &http.Response{StatusCode: http.StatusOK, Header: responseHeaders.Clone(), Body: responseBody, Request: got}, nil
				})
				ctx := nativeClaudePassthroughContext(nativeHeaders, roundTripper)
				executor := NewClaudeExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "must-not-replace-native-model", Payload: body}
				if operation == "stream" {
					result, errStream := executor.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{Stream: true})
					if errStream != nil {
						t.Fatal(errStream)
					}
					if !reflect.DeepEqual(result.Headers, responseHeaders) {
						t.Fatalf("stream headers = %#v, want %#v", result.Headers, responseHeaders)
					}
					var gotChunks [][]byte
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						gotChunks = append(gotChunks, bytes.Clone(chunk.Payload))
					}
					if !reflect.DeepEqual(gotChunks, chunks) || !closed.Load() {
						t.Fatalf("stream changed or body not closed: chunks=%q closed=%v", gotChunks, closed.Load())
					}
					return
				}
				var response cliproxyexecutor.Response
				var errExecute error
				if operation == "count" {
					response, errExecute = executor.CountTokens(ctx, auth, req, cliproxyexecutor.Options{})
				} else {
					response, errExecute = executor.Execute(ctx, auth, req, cliproxyexecutor.Options{})
				}
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				if !bytes.Equal(response.Payload, rawResponse) || !reflect.DeepEqual(response.Headers, responseHeaders) {
					t.Fatalf("raw response changed: payload=%x headers=%#v", response.Payload, response.Headers)
				}
			})
		}
	}
}

func TestNativeClaudeAPIKeyRejectsDuplicateIdentityContainers(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "final-api-key-backup",
		Provider: "claude",
		Attributes: map[string]string{
			"auth_kind": "api_key",
			"api_key":   "sk-ant-api03-selected-backup",
		},
	}
	for _, operation := range []string{"execute", "stream", "count"} {
		for _, test := range []struct {
			name string
			body string
		}{
			{name: "metadata", body: `{"model":"claude-future-9","messages":[],"metadata":{"user_id":"master-first"},"metadata":{"user_id":"master-last"}}`},
			{name: "user-id", body: `{"model":"claude-future-9","messages":[],"metadata":{"user_id":"master-first","user_id":"master-last"}}`},
			{name: "escaped-user-id", body: `{"model":"claude-future-9","messages":[],"metadata":{"user_id":"master-first","user_\u0069d":"master-last"}}`},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				var calls int
				roundTripper := nativeClaudePassthroughTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{}`)), Request: req}, nil
				})
				ctx := nativeClaudePassthroughContext(http.Header{"Content-Type": {"application/json"}}, roundTripper)
				executor := NewClaudeExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "claude-future-9", Payload: []byte(test.body)}
				var errExecute error
				switch operation {
				case "stream":
					var stream *cliproxyexecutor.StreamResult
					stream, errExecute = executor.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{Stream: true})
					if stream != nil {
						for range stream.Chunks {
						}
					}
				case "count":
					_, errExecute = executor.CountTokens(ctx, auth, req, cliproxyexecutor.Options{})
				default:
					_, errExecute = executor.Execute(ctx, auth, req, cliproxyexecutor.Options{})
				}
				var requestErr cliproxyexecutor.RequestScopedError
				if !errors.As(errExecute, &requestErr) || !requestErr.IsRequestScoped() {
					t.Fatalf("error = %T %v, want request-scoped duplicate identity rejection (upstream calls=%d)", errExecute, errExecute, calls)
				}
				var statusErr interface{ StatusCode() int }
				if !errors.As(errExecute, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
					t.Fatalf("error = %T %v, want HTTP 400", errExecute, errExecute)
				}
				if calls != 0 {
					t.Fatalf("duplicate identity reached upstream %d times", calls)
				}
			})
		}
	}
}

func TestNativeClaudeExecutePreservesRequestAndRawResponseRepresentation(t *testing.T) {
	auth := nativeClaudePassthroughAuth()
	const sessionID = "11111111-2222-4333-8444-555555555555"
	body := []byte("{\n  \"model\": \"claude-future-9\",\n  \"messages\": [{\"role\":\"assistant\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"opaque\",\"signature\":\"signed-value\"}]},{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"tool-1\",\"content\":\"ok\"}]}],\n  \"system\": [{\"type\":\"text\",\"text\":\"x-anthropic-billing-header: cc_version=99.7.1; cc_entrypoint=cli; cch=00000;\"}],\n  \"metadata\": {\"user_id\":\"{\\\"device_id\\\":\\\"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\\\",\\\"account_uuid\\\":\\\"master-account\\\",\\\"session_id\\\":\\\"" + sessionID + "\\\",\\\"future_identity_field\\\":true}\"},\n  \"temperature\": 0.2, \"top_p\": 0.3, \"future_option\": {\"enabled\":true}, \"stream\": false\n}\n")
	nativeHeaders := http.Header{
		"Accept":                       {"application/json"},
		"Accept-Encoding":              {"gzip, deflate, br, zstd, future"},
		"Anthropic-Beta":               {"oauth-2025-04-20,future-beta-2099-01-01"},
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
