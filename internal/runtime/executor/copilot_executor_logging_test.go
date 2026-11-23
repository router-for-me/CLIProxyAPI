package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func readCapturedAPIResponse(ginCtx *gin.Context) ([]byte, bool) {
	if rawSnap, okSnap := ginCtx.Get(logging.APIResponseSnapshotterContextKey); okSnap {
		if snapshotter, okType := rawSnap.(logging.APIResponseSnapshotter); okType && snapshotter != nil {
			if data := snapshotter.SnapshotAPIResponse(); len(data) > 0 {
				return data, true
			}
		}
	}
	rawResp, okResp := ginCtx.Get("API_RESPONSE")
	respBytes, okBytes := rawResp.([]byte)
	return respBytes, okResp && okBytes
}

func copilotLoggingTestContext(t *testing.T, rt http.RoundTripper) (context.Context, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = logging.WithResponseHeadersHolder(ctx)
	if rt != nil {
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", rt)
	}
	return ctx, ginCtx
}

func copilotLoggingTestAuth(t *testing.T, model, endpoint string) *cliproxyauth.Auth {
	t.Helper()
	auth := copilotTestAuth(t, "copilot-auth-1", model, endpoint)
	auth.Label = "personal"
	auth.Metadata["access_token"] = "ghu_secret_copilot_token_value_123456"
	return auth
}

func gzipBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, errWrite := zw.Write(raw); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := zw.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	return buf.Bytes()
}

func TestCopilotExecutorLogsNonStreamRequestAndResponse(t *testing.T) {
	const model = "gemini-4-neon"
	const upstreamJSON = `{"id":"chat-1","model":"gemini-4-neon","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`

	compressed := gzipBytes(t, []byte(upstreamJSON))
	var wireBody []byte
	ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var errRead error
		wireBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			return nil, errRead
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":     []string{"application/json"},
				"Content-Encoding": []string{"gzip"},
				"X-Upstream-Trace": []string{"trace-nonstream"},
			},
			Body:    io.NopCloser(bytes.NewReader(compressed)),
			Request: r,
		}, nil
	}))

	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	cfg.Payload.Override = []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: model}},
		Params: map[string]any{"user": "payload-override-sentinel"},
	}}
	e := NewCopilotExecutor(cfg)
	auth := copilotLoggingTestAuth(t, model, copilot.Chat)
	req := cliproxyexecutor.Request{
		Model:   copilot.ModelPrefix + model,
		Payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello copilot"}]}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAIResponse}

	if _, errExec := e.Execute(ctx, auth, req, opts); errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	rawAPIReq, okReq := ginCtx.Get("API_REQUEST")
	apiReqBytes, isReqBytes := rawAPIReq.([]byte)
	if !okReq || !isReqBytes {
		t.Fatalf("API_REQUEST = %#v, want captured bytes", rawAPIReq)
	}
	apiReqText := string(apiReqBytes)
	for _, want := range []string{
		"=== API REQUEST 1 ===",
		"Upstream URL: https://api.individual.githubcopilot.com/chat/completions",
		"HTTP Method: POST",
		"Auth: provider=copilot, auth_id=copilot-auth-1, label=personal, type=oauth",
		"Content-Type: application/json",
		"Authorization: Bearer ",
		"X-Initiator: user",
		`"model":"gemini-4-neon"`,
		`"user":"payload-override-sentinel"`,
		"hello copilot",
		string(wireBody),
	} {
		if !strings.Contains(apiReqText, want) {
			t.Fatalf("API_REQUEST missing %q:\n%s", want, apiReqText)
		}
	}
	if strings.Contains(apiReqText, "ghu_secret_copilot_token_value_123456") {
		t.Fatalf("API_REQUEST leaked raw access token:\n%s", apiReqText)
	}
	if strings.Contains(apiReqText, "<missing>") {
		t.Fatalf("API_REQUEST unexpectedly contained <missing>:\n%s", apiReqText)
	}

	rawAPIResp, okResp := ginCtx.Get("API_RESPONSE")
	apiRespBytes, isRespBytes := rawAPIResp.([]byte)
	if !okResp || !isRespBytes {
		t.Fatalf("API_RESPONSE = %#v, want captured bytes", rawAPIResp)
	}
	apiRespText := string(apiRespBytes)
	for _, want := range []string{
		"=== API RESPONSE 1 ===",
		"Status: 200",
		"Content-Encoding: gzip",
		"X-Upstream-Trace: trace-nonstream",
		upstreamJSON,
	} {
		if !strings.Contains(apiRespText, want) {
			t.Fatalf("API_RESPONSE missing %q:\n%s", want, apiRespText)
		}
	}
	if got := logging.GetResponseHeaders(ctx).Get("X-Upstream-Trace"); got != "trace-nonstream" {
		t.Fatalf("captured response header X-Upstream-Trace = %q, want trace-nonstream", got)
	}
}

func TestCopilotExecutorLogsStreamRequestAndResponse(t *testing.T) {
	tests := []struct {
		name             string
		model            string
		endpoint         string
		sourceFormat     tr.Format
		payload          string
		upstreamSSE      string
		wantURL          string
		wantResponseText []string
	}{
		{
			name:         "responses_to_chat_stream",
			model:        "gemini-4-neon",
			endpoint:     copilot.Chat,
			sourceFormat: tr.FormatOpenAIResponse,
			payload:      `{"input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`,
			upstreamSSE: "data: " + `{"id":"chat-s1","model":"gemini-4-neon","choices":[{"index":0,"delta":{"role":"assistant","content":"pong"}}]}` + "\n\n" +
				"data: " + `{"id":"chat-s1","model":"gemini-4-neon","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n",
			wantURL: "https://api.individual.githubcopilot.com/chat/completions",
			wantResponseText: []string{
				"=== API RESPONSE 1 ===",
				"Status: 200",
				"X-Upstream-Stream: yes",
				`"content":"pong"`,
				"data: [DONE]",
			},
		},
		{
			name:         "claude_to_messages_stream_preserves_event_lines",
			model:        "claude-opus-5",
			endpoint:     copilot.Messages,
			sourceFormat: tr.FormatClaude,
			payload:      `{"max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`,
			upstreamSSE: "event: message_start\n" +
				"data: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\n" +
				"event: content_block_delta\n" +
				"data: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}` + "\n\n" +
				"event: message_stop\n" +
				"data: " + `{"type":"message_stop"}` + "\n\n",
			wantURL: "https://api.individual.githubcopilot.com/v1/messages",
			wantResponseText: []string{
				"=== API RESPONSE 1 ===",
				"Status: 200",
				"X-Upstream-Stream: yes",
				"event: message_start\ndata: ",
				"event: message_stop\ndata: " + `{"type":"message_stop"}`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header: http.Header{
						"Content-Type":      []string{"text/event-stream"},
						"X-Upstream-Stream": []string{"yes"},
					},
					Body:    io.NopCloser(strings.NewReader(tc.upstreamSSE)),
					Request: r,
				}, nil
			}))

			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
			e := NewCopilotExecutor(cfg)
			auth := copilotLoggingTestAuth(t, tc.model, tc.endpoint)
			req := cliproxyexecutor.Request{
				Model:   copilot.ModelPrefix + tc.model,
				Payload: []byte(tc.payload),
			}
			opts := cliproxyexecutor.Options{SourceFormat: tc.sourceFormat, Stream: true}

			res, errStream := e.ExecuteStream(ctx, auth, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
			}

			rawAPIReq, okReq := ginCtx.Get("API_REQUEST")
			apiReqBytes, isReqBytes := rawAPIReq.([]byte)
			if !okReq || !isReqBytes {
				t.Fatalf("API_REQUEST = %#v, want captured bytes", rawAPIReq)
			}
			apiReqText := string(apiReqBytes)
			for _, want := range []string{
				"=== API REQUEST 1 ===",
				"Upstream URL: " + tc.wantURL,
				"Auth: provider=copilot, auth_id=copilot-auth-1, label=personal, type=oauth",
				"Authorization: Bearer ",
			} {
				if !strings.Contains(apiReqText, want) {
					t.Fatalf("API_REQUEST missing %q:\n%s", want, apiReqText)
				}
			}
			if strings.Contains(apiReqText, "ghu_secret_copilot_token_value_123456") {
				t.Fatalf("API_REQUEST leaked raw access token:\n%s", apiReqText)
			}

			apiRespBytes, okResp := readCapturedAPIResponse(ginCtx)
			if !okResp {
				t.Fatal("API_RESPONSE was not captured for stream")
			}
			apiRespText := string(apiRespBytes)
			for _, want := range tc.wantResponseText {
				if !strings.Contains(apiRespText, want) {
					t.Fatalf("API_RESPONSE missing %q:\n%s", want, apiRespText)
				}
			}
		})
	}
}

func TestCopilotExecutorLogsUpstreamHTTPErrorInFullAndDeferredModes(t *testing.T) {
	const model = "gemini-4-neon"
	const errBody = `{"error":{"message":"rate limited by copilot","type":"rate_limit_error"}}`

	for _, requestLog := range []bool{true, false} {
		mode := "deferred"
		if requestLog {
			mode = "full"
		}
		t.Run(mode, func(t *testing.T) {
			ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header: http.Header{
						"Content-Type":      []string{"application/json"},
						"X-RateLimit-Reset": []string{"60"},
					},
					Body:    io.NopCloser(strings.NewReader(errBody)),
					Request: r,
				}, nil
			}))

			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: requestLog}}
			e := NewCopilotExecutor(cfg)
			auth := copilotLoggingTestAuth(t, model, copilot.Chat)
			req := cliproxyexecutor.Request{
				Model:   copilot.ModelPrefix + model,
				Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
			}
			opts := cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}

			_, errExec := e.Execute(ctx, auth, req, opts)
			if errExec == nil {
				t.Fatal("Execute() error = nil, want 429 error")
			}

			if requestLog {
				rawReq, okReq := ginCtx.Get("API_REQUEST")
				reqBytes, isReqBytes := rawReq.([]byte)
				if !okReq || !isReqBytes || !strings.Contains(string(reqBytes), "https://api.individual.githubcopilot.com/chat/completions") {
					t.Fatalf("API_REQUEST = %#v, want captured upstream request", rawReq)
				}
				rawResp, okResp := ginCtx.Get("API_RESPONSE")
				respBytes, isRespBytes := rawResp.([]byte)
				if !okResp || !isRespBytes {
					t.Fatalf("API_RESPONSE = %#v, want captured bytes", rawResp)
				}
				respText := string(respBytes)
				for _, want := range []string{"Status: 429", "X-RateLimit-Reset: 60", errBody} {
					if !strings.Contains(respText, want) {
						t.Fatalf("API_RESPONSE missing %q:\n%s", want, respText)
					}
				}
				return
			}

			rawDeferredReq, okDeferredReq := ginCtx.Get(logging.DeferredAPIRequestContextKey)
			deferredReqs, okSlice := rawDeferredReq.([]logging.DeferredAPIRequest)
			if !okDeferredReq || !okSlice || len(deferredReqs) != 1 || !strings.Contains(string(deferredReqs[0]()), "/chat/completions") {
				t.Fatalf("DEFERRED_API_REQUEST = %#v, want 1 deferred upstream request", rawDeferredReq)
			}
			rawDeferredResp, okDeferredResp := ginCtx.Get(logging.DeferredAPIResponseContextKey)
			deferredResp, okCapture := rawDeferredResp.(*logging.DeferredAPICapture)
			if !okDeferredResp || !okCapture || deferredResp == nil {
				t.Fatalf("DEFERRED_API_RESPONSE = %#v, want *logging.DeferredAPICapture", rawDeferredResp)
			}
			rendered := string(deferredResp.RenderAPIResponse())
			for _, want := range []string{"=== API RESPONSE 1 ===", "Status: 429", "X-RateLimit-Reset: 60", errBody} {
				if !strings.Contains(rendered, want) {
					t.Fatalf("rendered deferred API response missing %q:\n%s", want, rendered)
				}
			}
		})
	}
}

func TestCopilotExecutorLogsResponseErrorsAndGuardsUnattemptedRequests(t *testing.T) {
	const model = "gemini-4-neon"
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	e := NewCopilotExecutor(cfg)

	t.Run("transport_error", func(t *testing.T) {
		dialErr := errors.New("dial tcp: connection refused")
		ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, dialErr
		}))
		auth := copilotLoggingTestAuth(t, model, copilot.Chat)
		req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

		if _, errExec := e.Execute(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}); !errors.Is(errExec, dialErr) {
			t.Fatalf("Execute() error = %v, want %v", errExec, dialErr)
		}
		if _, okReq := ginCtx.Get("API_REQUEST"); !okReq {
			t.Fatal("API_REQUEST was not captured before transport error")
		}
		if captured, _ := ginCtx.Get(logging.APIResponseCapturedContextKey); captured != true {
			t.Fatalf("APIResponseCapturedContextKey = %v, want true on transport error", captured)
		}
		if _, okResp := ginCtx.Get("API_RESPONSE"); okResp {
			t.Fatal("API_RESPONSE should not be fabricated on dial error")
		}
		rawCapture, _ := ginCtx.Get(logging.DeferredAPIResponseContextKey)
		capture, _ := rawCapture.(*logging.DeferredAPICapture)
		if capture == nil || len(capture.ResponseDiagnostics()) != 1 || !strings.Contains(capture.ResponseDiagnostics()[0].Error.Error(), "connection refused") {
			t.Fatalf("ResponseDiagnostics = %#v, want dial error", capture)
		}
	})

	t.Run("invalid_content_encoding", func(t *testing.T) {
		ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Encoding": []string{"bogus-codec"}},
				Body:       io.NopCloser(strings.NewReader("raw")),
				Request:    r,
			}, nil
		}))
		auth := copilotLoggingTestAuth(t, model, copilot.Chat)
		req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

		if _, errExec := e.Execute(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}); errExec == nil {
			t.Fatal("Execute() error = nil, want content encoding error")
		}
		rawResp, okResp := ginCtx.Get("API_RESPONSE")
		respBytes, _ := rawResp.([]byte)
		if !okResp || !strings.Contains(string(respBytes), "Content-Encoding: bogus-codec") {
			t.Fatalf("API_RESPONSE = %q, want Status/Content-Encoding metadata before decode error", string(respBytes))
		}
		rawCapture, _ := ginCtx.Get(logging.DeferredAPIResponseContextKey)
		capture, _ := rawCapture.(*logging.DeferredAPICapture)
		if capture == nil || len(capture.ResponseDiagnostics()) != 1 || !strings.Contains(capture.ResponseDiagnostics()[0].Error.Error(), "unsupported content encoding") {
			t.Fatalf("ResponseDiagnostics = %#v, want decode error", capture)
		}
	})

	t.Run("nonstream_invalid_json_records_body_and_error", func(t *testing.T) {
		ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader("{not-valid-json")),
				Request:    r,
			}, nil
		}))
		auth := copilotLoggingTestAuth(t, model, copilot.Chat)
		req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

		if _, errExec := e.Execute(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}); errExec == nil {
			t.Fatal("Execute() error = nil, want invalid JSON error")
		}
		rawResp, _ := ginCtx.Get("API_RESPONSE")
		if respBytes, _ := rawResp.([]byte); !strings.Contains(string(respBytes), "{not-valid-json") {
			t.Fatalf("API_RESPONSE = %q, want invalid raw body captured", string(respBytes))
		}
		rawCapture, _ := ginCtx.Get(logging.DeferredAPIResponseContextKey)
		capture, _ := rawCapture.(*logging.DeferredAPICapture)
		if capture == nil || len(capture.ResponseDiagnostics()) != 1 || !strings.Contains(capture.ResponseDiagnostics()[0].Error.Error(), "invalid upstream JSON") {
			t.Fatalf("ResponseDiagnostics = %#v, want invalid JSON diagnostic", capture)
		}
	})

	t.Run("nonstream_upstream_error_payload_records_body_and_error", func(t *testing.T) {
		const errPayload = `{"error":{"type":"rate_limit_error","message":"too many requests"}}`
		ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(errPayload)),
				Request:    r,
			}, nil
		}))
		auth := copilotLoggingTestAuth(t, model, copilot.Chat)
		req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

		if _, errExec := e.Execute(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}); errExec == nil {
			t.Fatal("Execute() error = nil, want upstream rate limit error")
		}
		rawResp, _ := ginCtx.Get("API_RESPONSE")
		if respBytes, _ := rawResp.([]byte); !strings.Contains(string(respBytes), errPayload) {
			t.Fatalf("API_RESPONSE = %q, want upstream JSON error payload captured", string(respBytes))
		}
		rawCapture, _ := ginCtx.Get(logging.DeferredAPIResponseContextKey)
		capture, _ := rawCapture.(*logging.DeferredAPICapture)
		if capture == nil || len(capture.ResponseDiagnostics()) != 1 || !strings.Contains(capture.ResponseDiagnostics()[0].Error.Error(), "rate limit") {
			t.Fatalf("ResponseDiagnostics = %#v, want upstream rate limit diagnostic", capture)
		}
	})

	t.Run("stream_upstream_error_event_records_line_and_error", func(t *testing.T) {
		const errSSE = "data: " + `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}` + "\n\n"
		ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(errSSE)),
				Request:    r,
			}, nil
		}))
		auth := copilotLoggingTestAuth(t, model, copilot.Chat)
		req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

		res, errStream := e.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI, Stream: true})
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		var streamErr error
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
		}
		if streamErr == nil {
			t.Fatal("expected stream error event")
		}
		if respBytes, _ := readCapturedAPIResponse(ginCtx); !strings.Contains(string(respBytes), `"rate_limit_error"`) {
			t.Fatalf("API_RESPONSE = %q, want SSE error event captured", string(respBytes))
		}
		rawCapture, _ := ginCtx.Get(logging.DeferredAPIResponseContextKey)
		capture, _ := rawCapture.(*logging.DeferredAPICapture)
		if capture == nil || len(capture.ResponseDiagnostics()) != 1 || !strings.Contains(capture.ResponseDiagnostics()[0].Error.Error(), "rate limit") {
			t.Fatalf("ResponseDiagnostics = %#v, want stream rate limit diagnostic", capture)
		}
	})

	t.Run("stream_truncated_records_lines_and_error", func(t *testing.T) {
		partialSSE := "data: " + `{"id":"chat-t1","model":"gemini-4-neon","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"}}]}` + "\n\n"
		ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(partialSSE)),
				Request:    r,
			}, nil
		}))
		auth := copilotLoggingTestAuth(t, model, copilot.Chat)
		req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

		res, errStream := e.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI, Stream: true})
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		var streamErr error
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
		}
		if streamErr == nil {
			t.Fatal("expected stream truncation error")
		}
		if respBytes, _ := readCapturedAPIResponse(ginCtx); !strings.Contains(string(respBytes), `"content":"partial"`) {
			t.Fatalf("API_RESPONSE = %q, want partial upstream SSE chunk captured", string(respBytes))
		}
		rawCapture, _ := ginCtx.Get(logging.DeferredAPIResponseContextKey)
		capture, _ := rawCapture.(*logging.DeferredAPICapture)
		if capture == nil || len(capture.ResponseDiagnostics()) != 1 || !strings.Contains(capture.ResponseDiagnostics()[0].Error.Error(), "terminal event") {
			t.Fatalf("ResponseDiagnostics = %#v, want stream terminal event diagnostic", capture)
		}
	})

	t.Run("missing_access_token_does_not_log_request", func(t *testing.T) {
		ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("transport should not be invoked without access token")
			return nil, nil
		}))
		auth := copilotLoggingTestAuth(t, model, copilot.Chat)
		delete(auth.Metadata, "access_token")
		req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

		if _, errExec := e.Execute(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}); errExec == nil {
			t.Fatal("Execute() error = nil, want 401 error")
		}
		if _, okReq := ginCtx.Get("API_REQUEST"); okReq {
			t.Fatal("API_REQUEST must not be recorded when credentials are missing before transport")
		}
		if _, okResp := ginCtx.Get("API_RESPONSE"); okResp {
			t.Fatal("API_RESPONSE must not be recorded when credentials are missing before transport")
		}
	})

	t.Run("direct_http_request_does_not_log_api_request", func(t *testing.T) {
		ctx, ginCtx := copilotLoggingTestContext(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    r,
			}, nil
		}))
		auth := copilotLoggingTestAuth(t, model, copilot.Chat)
		httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.individual.githubcopilot.com/models", nil)
		if errNew != nil {
			t.Fatal(errNew)
		}
		resp, errHTTP := e.HttpRequest(ctx, auth, httpReq)
		if errHTTP != nil {
			t.Fatalf("HttpRequest() error = %v", errHTTP)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if _, okReq := ginCtx.Get("API_REQUEST"); okReq {
			t.Fatal("HttpRequest must not record API_REQUEST")
		}
		if _, okResp := ginCtx.Get("API_RESPONSE"); okResp {
			t.Fatal("HttpRequest must not record API_RESPONSE")
		}
	})
}

type blockingCopilotStreamBody struct {
	firstChunk []byte
	lateChunk  []byte
	lateErr    error

	mu          sync.Mutex
	reads       int
	blockedOnce sync.Once
	blocked     chan struct{}
	release     chan struct{}
	closedOnce  sync.Once
	closed      chan struct{}
}

func (b *blockingCopilotStreamBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	call := b.reads
	b.reads++
	b.mu.Unlock()

	switch call {
	case 0:
		n := copy(p, b.firstChunk)
		return n, nil
	case 1:
		b.blockedOnce.Do(func() { close(b.blocked) })
		<-b.release
		if len(b.lateChunk) > 0 {
			n := copy(p, b.lateChunk)
			return n, nil
		}
		return 0, b.lateErr
	default:
		return 0, b.lateErr
	}
}

func (b *blockingCopilotStreamBody) Close() error {
	b.closedOnce.Do(func() { close(b.closed) })
	return nil
}

func TestCopilotExecutorStreamLoggingIsolatesCanceledRequestOnGinReuse(t *testing.T) {
	const model = "gemini-4-neon"
	const firstSSE = "data: " + `{"id":"chat-r1","model":"gemini-4-neon","choices":[{"index":0,"delta":{"role":"assistant","content":"req1-first"}}]}` + "\n\n"
	const lateSSE = "data: " + `{"id":"chat-r1","model":"gemini-4-neon","choices":[{"index":0,"delta":{"content":"STALE_REQ1_CHUNK"}}]}` + "\n\n"
	const req2JSON = `{"id":"chat-r2","model":"gemini-4-neon","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"req2-clean"},"finish_reason":"stop"}]}`

	modes := []string{"full_memory_backed", "full_file_backed", "deferred_context_capture", "deferred_gin_fallback"}
	lateEvents := []struct {
		name      string
		lateChunk []byte
		lateErr   error
	}{
		{name: "late_chunk", lateChunk: []byte(lateSSE), lateErr: io.EOF},
		{name: "late_read_error", lateErr: errors.New("late read error from req1")},
	}

	for _, mode := range modes {
		for _, event := range lateEvents {
			t.Run(mode+"/"+event.name, func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

				requestLog := mode == "full_memory_backed" || mode == "full_file_backed"
				cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: requestLog}}
				e := NewCopilotExecutor(cfg)
				auth := copilotLoggingTestAuth(t, model, copilot.Chat)

				var source1 *logging.FileBodySource
				if mode == "full_file_backed" {
					var errSource error
					source1, errSource = logging.NewFileBodySourceInDir(t.TempDir(), "api-response-1")
					if errSource != nil {
						t.Fatal(errSource)
					}
					t.Cleanup(func() { _ = source1.Cleanup() })
					ginCtx.Set(logging.APIResponseSourceContextKey, source1)
				}
				var capture1 *logging.DeferredAPICapture
				if mode == "deferred_context_capture" {
					capture1 = logging.NewDeferredAPICapture()
					ginCtx.Set(logging.DeferredAPIResponseContextKey, capture1)
				}

				body1 := &blockingCopilotStreamBody{
					firstChunk: []byte(firstSSE),
					lateChunk:  event.lateChunk,
					lateErr:    event.lateErr,
					blocked:    make(chan struct{}),
					release:    make(chan struct{}),
					closed:     make(chan struct{}),
				}
				ctx1, cancel1 := context.WithCancel(context.WithValue(context.Background(), "gin", ginCtx))
				if capture1 != nil {
					ctx1 = logging.WithDeferredAPICapture(ctx1, capture1)
				}
				ctx1 = context.WithValue(ctx1, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       body1,
						Request:    r,
					}, nil
				}))

				res1, errStream := e.ExecuteStream(ctx1, auth, cliproxyexecutor.Request{
					Model:   copilot.ModelPrefix + model,
					Payload: []byte(`{"messages":[{"role":"user","content":"req1"}]}`),
				}, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI, Stream: true})
				if errStream != nil {
					t.Fatalf("ExecuteStream(req1) error = %v", errStream)
				}

				// Consume the first chunk and wait until the scanner goroutine blocks on the second Read.
				chunk1, okChunk := <-res1.Chunks
				if !okChunk || chunk1.Err != nil {
					t.Fatalf("first chunk = (%#v, %v), want valid chunk", chunk1, okChunk)
				}
				<-body1.blocked

				// Simulate client disconnect followed by Finalize and Gin pooling reuse for request 2.
				cancel1()
				if rawSnap, okSnap := ginCtx.Get(logging.APIResponseSnapshotterContextKey); okSnap {
					if closer, okCloser := rawSnap.(interface{ Close() }); okCloser && closer != nil {
						closer.Close()
					}
				}
				if capture1 != nil {
					capture1.Close()
				}
				if source1 != nil {
					if errCleanup := source1.Cleanup(); errCleanup != nil {
						t.Fatal(errCleanup)
					}
				}
				ginCtx.Keys = nil
				ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

				var source2 *logging.FileBodySource
				if mode == "full_file_backed" {
					var errSource error
					source2, errSource = logging.NewFileBodySourceInDir(t.TempDir(), "api-response-2")
					if errSource != nil {
						t.Fatal(errSource)
					}
					t.Cleanup(func() { _ = source2.Cleanup() })
					ginCtx.Set(logging.APIResponseSourceContextKey, source2)
				}
				var capture2 *logging.DeferredAPICapture
				if mode == "deferred_context_capture" {
					capture2 = logging.NewDeferredAPICapture()
					ginCtx.Set(logging.DeferredAPIResponseContextKey, capture2)
				}

				ctx2 := context.WithValue(context.Background(), "gin", ginCtx)
				if capture2 != nil {
					ctx2 = logging.WithDeferredAPICapture(ctx2, capture2)
				}
				ctx2 = context.WithValue(ctx2, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(req2JSON)),
						Request:    r,
					}, nil
				}))

				if _, errExec2 := e.Execute(ctx2, auth, cliproxyexecutor.Request{
					Model:   copilot.ModelPrefix + model,
					Payload: []byte(`{"messages":[{"role":"user","content":"req2"}]}`),
				}, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}); errExec2 != nil {
					t.Fatalf("Execute(req2) error = %v", errExec2)
				}

				// Release request 1's blocked reader so it delivers the late SSE chunk or read error.
				close(body1.release)
				<-body1.closed

				switch mode {
				case "full_memory_backed":
					respBytes, okResp := readCapturedAPIResponse(ginCtx)
					if !okResp || !strings.Contains(string(respBytes), "req2-clean") {
						t.Fatalf("req2 API_RESPONSE = %q, want req2-clean", string(respBytes))
					}
					if strings.Contains(string(respBytes), "STALE_REQ1_CHUNK") {
						t.Fatalf("req2 API_RESPONSE polluted by stale req1 chunk:\n%s", string(respBytes))
					}
				case "full_file_backed":
					respBytes, errBytes := source2.Bytes()
					if errBytes != nil {
						t.Fatal(errBytes)
					}
					if !strings.Contains(string(respBytes), "req2-clean") {
						t.Fatalf("req2 file API_RESPONSE = %q, want req2-clean", string(respBytes))
					}
					if strings.Contains(string(respBytes), "STALE_REQ1_CHUNK") {
						t.Fatalf("req2 file API_RESPONSE polluted by stale req1 chunk:\n%s", string(respBytes))
					}
				case "deferred_context_capture", "deferred_gin_fallback":
					rawCap, okCap := ginCtx.Get(logging.DeferredAPIResponseContextKey)
					cap2, okType := rawCap.(*logging.DeferredAPICapture)
					if !okCap || !okType || cap2 == nil {
						t.Fatalf("req2 DEFERRED_API_RESPONSE = %#v, want *logging.DeferredAPICapture", rawCap)
					}
					rendered := string(cap2.RenderAPIResponse())
					if !strings.Contains(rendered, "req2-clean") {
						t.Fatalf("req2 deferred API_RESPONSE = %q, want req2-clean", rendered)
					}
					if strings.Contains(rendered, "STALE_REQ1_CHUNK") {
						t.Fatalf("req2 deferred API_RESPONSE polluted by stale req1 chunk:\n%s", rendered)
					}
				}

				if rawCap, okCap := ginCtx.Get(logging.DeferredAPIResponseContextKey); okCap {
					if cap2, okType := rawCap.(*logging.DeferredAPICapture); okType && cap2 != nil {
						if diags := cap2.ResponseDiagnostics(); len(diags) != 0 {
							t.Fatalf("req2 ResponseDiagnostics polluted by stale req1 error: %#v", diags)
						}
					}
				}
			})
		}
	}
}

type continuousCopilotStreamBody struct {
	chunk      []byte
	maxReads   int
	reads      int
	closedOnce sync.Once
	closed     chan struct{}
}

func (b *continuousCopilotStreamBody) Read(p []byte) (int, error) {
	if b.reads >= b.maxReads {
		return 0, errors.New("req1 stream ended with read error")
	}
	b.reads++
	n := copy(p, b.chunk)
	return n, nil
}

func (b *continuousCopilotStreamBody) Close() error {
	b.closedOnce.Do(func() { close(b.closed) })
	return nil
}

func TestCopilotExecutorStreamLoggingConcurrentMidCallbackGinReuse(t *testing.T) {
	const model = "gemini-4-neon"
	const req1Chunk = "data: " + `{"id":"chat-r1","model":"gemini-4-neon","choices":[{"index":0,"delta":{"role":"assistant","content":"STALE_MID_CALLBACK"}}]}` + "\n\n"
	const req2JSON = `{"id":"chat-r2","model":"gemini-4-neon","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"req2-isolated"},"finish_reason":"stop"}]}`

	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
	e := NewCopilotExecutor(cfg)
	auth := copilotLoggingTestAuth(t, model, copilot.Chat)

	for range 20 {
		gin.SetMode(gin.TestMode)
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		body1 := &continuousCopilotStreamBody{
			chunk:    []byte(req1Chunk),
			maxReads: 200,
			closed:   make(chan struct{}),
		}
		ctx1, cancel1 := context.WithCancel(context.WithValue(context.Background(), "gin", ginCtx))
		ctx1 = context.WithValue(ctx1, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       body1,
				Request:    r,
			}, nil
		}))

		res1, errStream := e.ExecuteStream(ctx1, auth, cliproxyexecutor.Request{
			Model:   copilot.ModelPrefix + model,
			Payload: []byte(`{"messages":[{"role":"user","content":"req1"}]}`),
		}, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI, Stream: true})
		if errStream != nil {
			t.Fatalf("ExecuteStream(req1) error = %v", errStream)
		}

		// Read one chunk so the background scanner goroutine is actively running while we cancel and reuse ginCtx.
		if chunk1, okChunk := <-res1.Chunks; !okChunk || chunk1.Err != nil {
			t.Fatalf("first chunk = (%#v, %v), want valid chunk", chunk1, okChunk)
		}

		// Concurrently snapshot, close, reset ginCtx, and run request 2 without waiting for req1's goroutine.
		if rawSnap, okSnap := ginCtx.Get(logging.APIResponseSnapshotterContextKey); okSnap {
			if snapshotter, okType := rawSnap.(logging.APIResponseSnapshotter); okType && snapshotter != nil {
				_ = snapshotter.SnapshotAPIResponse()
				snapshotter.Close()
			}
		}
		if rawCap, okCap := ginCtx.Get(logging.DeferredAPIResponseContextKey); okCap {
			if closer, okCloser := rawCap.(interface{ Close() }); okCloser && closer != nil {
				closer.Close()
			}
		}
		cancel1()
		ginCtx.Keys = nil
		ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		ctx2 := context.WithValue(context.Background(), "gin", ginCtx)
		ctx2 = context.WithValue(ctx2, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(req2JSON)),
				Request:    r,
			}, nil
		}))
		if _, errExec2 := e.Execute(ctx2, auth, cliproxyexecutor.Request{
			Model:   copilot.ModelPrefix + model,
			Payload: []byte(`{"messages":[{"role":"user","content":"req2"}]}`),
		}, cliproxyexecutor.Options{SourceFormat: tr.FormatOpenAI}); errExec2 != nil {
			t.Fatalf("Execute(req2) error = %v", errExec2)
		}

		<-body1.closed

		respBytes, okResp := readCapturedAPIResponse(ginCtx)
		if !okResp || !strings.Contains(string(respBytes), "req2-isolated") {
			t.Fatalf("req2 API_RESPONSE = %q, want req2-isolated", string(respBytes))
		}
		if strings.Contains(string(respBytes), "STALE_MID_CALLBACK") {
			t.Fatalf("req2 API_RESPONSE polluted during mid-callback reuse:\n%s", string(respBytes))
		}
		if rawCap, okCap := ginCtx.Get(logging.DeferredAPIResponseContextKey); okCap {
			if cap2, okType := rawCap.(*logging.DeferredAPICapture); okType && cap2 != nil {
				if diags := cap2.ResponseDiagnostics(); len(diags) != 0 {
					t.Fatalf("req2 ResponseDiagnostics polluted during mid-callback reuse: %#v", diags)
				}
			}
		}
	}
}
