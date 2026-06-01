package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexOpenAIImageExecuteReturnsOnResponseCompletedBeforeEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": keepalive\n"))
		_, _ = w.Write(codexBuildSSEFrame("", codexImageCompletedPayload("AA==")))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	started := time.Now()
	resp, err := codexTestImageExecutor(server.URL).Execute(ctx, codexTestImageAuth(server.URL), codexTestImageRequest(), codexTestImageOptions())
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Execute() waited %v after response.completed; want quick return before EOF", elapsed)
	}
	if got := gjson.GetBytes(resp.Payload, "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("b64_json = %q, want AA==; payload=%s", got, string(resp.Payload))
	}
}

func TestCodexOpenAIImageExecutePreservesCompletedResponseShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(codexBuildSSEFrame("", []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"image_generation_call","result":"AA==","revised_prompt":"draw revised","output_format":"png","size":"1024x1024","background":"transparent","quality":"high"}}`)))
		_, _ = w.Write(codexBuildSSEFrame("", []byte(`{"type":"response.completed","response":{"created_at":123,"output":[],"tool_usage":{"image_gen":{"input_tokens":1,"output_tokens":2,"total_tokens":3}},"usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}}`)))
	}))
	defer server.Close()

	resp, err := codexTestImageExecutor(server.URL).Execute(context.Background(), codexTestImageAuth(server.URL), codexTestImageRequest(), codexTestImageOptions())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "created").Int(); got != 123 {
		t.Fatalf("created = %d, want 123; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("b64_json = %q, want AA==; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "data.0.revised_prompt").String(); got != "draw revised" {
		t.Fatalf("revised_prompt = %q, want draw revised; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "output_format").String(); got != "png" {
		t.Fatalf("output_format = %q, want png; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "usage.total_tokens").Int(); got != 3 {
		t.Fatalf("usage.total_tokens = %d, want 3; payload=%s", got, string(resp.Payload))
	}
}

func TestCodexOpenAIImageSSEMissingCompletedReturns502(t *testing.T) {
	_, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader(`data: {"type":"response.output_item.done","item":{"type":"image_generation_call","result":"AA=="}}

`), map[int64][]byte{}, &[][]byte{})

	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEMissingCompleted)
	if strings.Contains(err.Error(), "stream error: stream disconnected before completion") {
		t.Fatalf("error used old 504 message: %v", err)
	}
}

func TestCodexOpenAIImageSSEClassifiesUnexpectedEOF(t *testing.T) {
	_, err := codexReadOpenAIImageResponsesSSE(context.Background(), codexImageErrReader{err: io.ErrUnexpectedEOF}, map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEStreamClosed)
}

func TestCodexOpenAIImageSSEClassifiesReadError(t *testing.T) {
	_, err := codexReadOpenAIImageResponsesSSE(context.Background(), codexImageErrReader{err: errors.New("read failed")}, map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEReadError)
}

func TestCodexOpenAIImageSSEClassifiesHTTP2Reset(t *testing.T) {
	_, err := codexReadOpenAIImageResponsesSSE(context.Background(), codexImageErrReader{err: errors.New("stream error: stream ID 13; INTERNAL_ERROR; received from peer")}, map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEH2Reset)
}

func TestCodexOpenAIImageSSEClassifiesContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := codexReadOpenAIImageResponsesSSE(ctx, strings.NewReader(""), map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusGatewayTimeout, codexImageSSEContextTimeout)
}

func TestCodexOpenAIImageSSEClassifiesContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := codexReadOpenAIImageResponsesSSE(ctx, strings.NewReader(""), map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, codexStatusClientClosedRequest, codexImageSSEContextCanceled)
}

func TestCodexOpenAIImageSSEClassifiesUpstreamErrorEventSafely(t *testing.T) {
	raw := `event: error
data: {"type":"Authorization-Bearer-sk-secret-prompt-base64","prompt":"secret prompt","partial_image_b64":"raw-b64","Authorization":"Bearer secret","Cookie":"secret","api_key":"sk-secret","access_token":"secret","refresh_token":"secret"}

`
	_, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader(raw), map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEUpstreamError)

	for _, forbidden := range []string{"secret prompt", "raw-b64", "Authorization", "Cookie", "sk-secret", "access_token", "refresh_token", "Bearer", "prompt", "base64"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked forbidden content %q: %v", forbidden, err)
		}
	}
}

func TestCodexOpenAIImageSSEUpstreamErrorEventExtractsSafeFields(t *testing.T) {
	raw := `event: error
data: {"type":"error","id":"resp_123","error":{"type":"server_error","code":"internal_error","status":500,"param":"image","reason":"generator_failed","message":"do not expose this message"}}

`
	headers := http.Header{
		"Retry-After":             {"17"},
		"X-Openai-Request-Id":     {"req_123"},
		"Authorization":           {"Bearer forbidden"},
		"Cookie":                  {"forbidden"},
		"Set-Cookie":              {"forbidden"},
		"X-Ignored-Unsafe-Header": {"forbidden"},
	}
	_, err := codexReadOpenAIImageResponsesSSEWithHeaders(context.Background(), strings.NewReader(raw), headers, map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEUpstreamError)
	msg := err.Error()

	for _, want := range []string{
		"upstream_event_type=error",
		"upstream_data_type=error",
		"upstream_error_type=server_error",
		"upstream_error_code=internal_error",
		"upstream_error_status=500",
		"upstream_error_param=image",
		"upstream_error_reason=generator_failed",
		"upstream_response_id=resp_123",
		"response_id=resp_123",
		"retry_after=17",
		"upstream_request_id=req_123",
		"error_category=internal_error",
	} {
		assertCodexImageErrContains(t, msg, want)
	}
	for _, forbidden := range []string{"do not expose this message", "Authorization", "Cookie", "Set-Cookie", "Bearer forbidden", "X-Ignored-Unsafe-Header"} {
		assertCodexImageErrNotContains(t, msg, forbidden)
	}
}

func TestCodexOpenAIImageSSEDataTypeErrorExtractsSafeFields(t *testing.T) {
	raw := `data: {"type":"error","response":{"id":"resp_data_error"},"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","status_code":429,"param":"requests","reason":"rate_limit"}}

`
	_, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader(raw), map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEUpstreamError)
	msg := err.Error()
	for _, want := range []string{
		"upstream_data_type=error",
		"upstream_error_type=rate_limit_error",
		"upstream_error_code=rate_limit_exceeded",
		"upstream_error_status=429",
		"upstream_error_param=requests",
		"upstream_error_reason=rate_limit",
		"upstream_response_id=resp_data_error",
		"error_category=rate_limit",
	} {
		assertCodexImageErrContains(t, msg, want)
	}
}

func TestCodexOpenAIImageSSEResponseFailedAndIncompleteSummaries(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wants   []string
	}{
		{
			name:    "failed",
			payload: `{"type":"response.failed","response":{"id":"resp_failed","failed_reason":"tool_generation_failed","error":{"code":"generation_failed","type":"server_error"}}}`,
			wants: []string{
				"upstream_data_type=response.failed",
				"upstream_failed_reason=tool_generation_failed",
				"upstream_error_code=generation_failed",
				"upstream_error_type=server_error",
				"upstream_response_id=resp_failed",
				"error_category=upstream_failed",
			},
		},
		{
			name:    "incomplete",
			payload: `{"type":"response.incomplete","response":{"id":"resp_incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
			wants: []string{
				"upstream_data_type=response.incomplete",
				"upstream_incomplete_reason=max_output_tokens",
				"upstream_response_id=resp_incomplete",
				"error_category=upstream_incomplete",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := "data: " + tt.payload + "\n\n"
			_, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader(raw), map[int64][]byte{}, &[][]byte{})
			assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEUpstreamError)
			msg := err.Error()
			for _, want := range tt.wants {
				assertCodexImageErrContains(t, msg, want)
			}
			assertCodexImageErrNotContains(t, msg, tt.payload)
		})
	}
}

func TestCodexOpenAIImageSSEUpstreamErrorCategories(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		category string
	}{
		{
			name:     "rate limit",
			payload:  `{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","status":429,"reason":"rate_limit"}}`,
			category: "rate_limit",
		},
		{
			name:     "quota",
			payload:  `{"type":"error","error":{"type":"billing_error","code":"insufficient_quota","status":429,"reason":"quota"}}`,
			category: "quota",
		},
		{
			name:     "capacity",
			payload:  `{"type":"error","error":{"type":"server_error","code":"capacity_exceeded","status":503,"reason":"capacity"}}`,
			category: "capacity",
		},
		{
			name:     "overloaded",
			payload:  `{"type":"error","error":{"type":"server_error","code":"model_overloaded","status":503,"reason":"overloaded"}}`,
			category: "overloaded",
		},
		{
			name:     "safety",
			payload:  `{"type":"error","error":{"type":"policy_violation","code":"safety_policy","status":400,"reason":"safety"}}`,
			category: "safety",
		},
		{
			name:     "timeout",
			payload:  `{"type":"error","error":{"type":"server_error","code":"upstream_timeout","status":504,"reason":"timeout"}}`,
			category: "timeout",
		},
		{
			name:     "invalid request",
			payload:  `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request","status":400,"reason":"invalid_request"}}`,
			category: "invalid_request",
		},
		{
			name:     "auth",
			payload:  `{"type":"error","error":{"type":"authentication_error","code":"unauthorized","status":401,"reason":"auth"}}`,
			category: "auth",
		},
		{
			name:     "unknown",
			payload:  `{"type":"error","error":{"type":"mystery","code":"unknown","status":418,"message":"classified only in memory"}}`,
			category: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader("data: "+tt.payload+"\n\n"), map[int64][]byte{}, &[][]byte{})
			assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEUpstreamError)
			msg := err.Error()
			assertCodexImageErrContains(t, msg, "error_category="+tt.category)
			assertCodexImageErrNotContains(t, msg, "classified only in memory")
		})
	}
}

func TestCodexOpenAIImageSSEUpstreamErrorCountsImagesAndPartials(t *testing.T) {
	raw := `data: {"type":"response.output_item.done","item":{"type":"image_generation_call","result":"AA=="}}

data: {"type":"response.image_generation_call.partial_image","partial_image_index":0,"partial_image_b64":"secret-b64"}

data: {"type":"error","error":{"code":"capacity","reason":"capacity"}}

`
	_, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader(raw), map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEUpstreamError)
	msg := err.Error()
	assertCodexImageErrContains(t, msg, "image_count=1")
	assertCodexImageErrContains(t, msg, "partial_image_count=1")
	assertCodexImageErrNotContains(t, msg, "secret-b64")
}

func TestCodexOpenAIImageSSEUpstreamErrorDoesNotLeakSensitivePayload(t *testing.T) {
	raw := `event: error
data: {"type":"error","id":"resp_safe","error":{"type":"rate_limit_error","code":"rate_limit","status":429,"message":"prompt raw user content base64 token Authorization Cookie api_key sk-secret access_token refresh_token id_token"},"prompt":"secret prompt","partial_image_b64":"raw-b64","user_content":"raw user content","image_content":"raw image content","Authorization":"Bearer secret","Cookie":"secret","api_key":"sk-secret","access_token":"secret","refresh_token":"secret","id_token":"secret"}

`
	_, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader(raw), map[int64][]byte{}, &[][]byte{})
	assertCodexImageStreamStatus(t, err, http.StatusBadGateway, codexImageSSEUpstreamError)
	msg := err.Error()
	for _, forbidden := range []string{
		"secret prompt",
		"raw-b64",
		"raw user content",
		"raw image content",
		"Authorization",
		"Cookie",
		"sk-secret",
		"access_token",
		"refresh_token",
		"id_token",
		"Bearer",
		"prompt",
		"base64",
		"user content",
		"image content",
	} {
		assertCodexImageErrNotContains(t, msg, forbidden)
	}
	assertCodexImageErrContains(t, msg, "upstream_response_id=resp_safe")
	assertCodexImageErrContains(t, msg, "error_category=rate_limit")
}

func TestCodexOpenAIImageExecuteSuccessDoesNotIncludeUpstreamErrorSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Retry-After", "11")
		w.Header().Set("X-Request-Id", "req_success")
		_, _ = w.Write(codexBuildSSEFrame("", codexImageCompletedPayload("AA==")))
	}))
	defer server.Close()

	resp, err := codexTestImageExecutor(server.URL).Execute(context.Background(), codexTestImageAuth(server.URL), codexTestImageRequest(), codexTestImageOptions())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	payload := string(resp.Payload)
	for _, forbidden := range []string{"upstream_error", "error_category", "retry_after", "req_success"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("success payload leaked upstream error summary %q: %s", forbidden, payload)
		}
	}
}

func TestCodexOpenAIImageHTTPClientDisableHTTP2(t *testing.T) {
	proxyURL, errParse := url.Parse("http://proxy.example.com:8080")
	if errParse != nil {
		t.Fatalf("url.Parse returned error: %v", errParse)
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", &http.Transport{
		ForceAttemptHTTP2: true,
		Proxy:             http.ProxyURL(proxyURL),
	})
	executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{DisableHTTP2: true}})

	client := executor.newCodexOpenAIImageHTTPClient(ctx, nil)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = true, want false")
	}
	if transport.TLSNextProto == nil || len(transport.TLSNextProto) != 0 {
		t.Fatalf("TLSNextProto = %#v, want empty map", transport.TLSNextProto)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig = nil")
	}
	if got := transport.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("NextProtos = %v, want [http/1.1]", got)
	}
	if transport.Proxy == nil {
		t.Fatal("Proxy function was not preserved")
	}
}

func TestCodexOpenAIImageHTTPClientKeepsDefaultWhenHTTP2Enabled(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{})

	client := executor.newCodexOpenAIImageHTTPClient(context.Background(), nil)

	if client.Transport != nil {
		t.Fatalf("Transport = %T, want nil default transport", client.Transport)
	}
}

func TestCodexImageCompletedWithoutOutputReasonUsesToolStatus(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"image_generation_call","status":"failed"}]}}`)

	if got := codexImageCompletedWithoutOutputReason(payload); got != "failed" {
		t.Fatalf("reason = %q, want failed", got)
	}
}

func TestCodexOpenAIImageSSESupportsLargeDataLine(t *testing.T) {
	largeResult := strings.Repeat("A", 70*1024)
	data, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader("data: "+string(codexImageCompletedPayload(largeResult))+"\n\n"), map[int64][]byte{}, &[][]byte{})
	if err != nil {
		t.Fatalf("codexReadOpenAIImageResponsesSSE() error = %v", err)
	}
	if got := gjson.GetBytes(data, "response.output.0.result").String(); len(got) != len(largeResult) {
		t.Fatalf("result length = %d, want %d", len(got), len(largeResult))
	}
}

func TestCodexOpenAIImageSSESupportsMultilineData(t *testing.T) {
	payload := `data: {"type":"response.completed",
data: "response":{"created_at":123,"output":[{"type":"image_generation_call","result":"AA=="}]}}

`
	data, err := codexReadOpenAIImageResponsesSSE(context.Background(), strings.NewReader(payload), map[int64][]byte{}, &[][]byte{})
	if err != nil {
		t.Fatalf("codexReadOpenAIImageResponsesSSE() error = %v", err)
	}
	if got := gjson.GetBytes(data, "type").String(); got != "response.completed" {
		t.Fatalf("type = %q, want response.completed; data=%s", got, string(data))
	}
}

type codexImageErrReader struct {
	err error
}

func (r codexImageErrReader) Read(_ []byte) (int, error) {
	return 0, r.err
}

func codexTestImageExecutor(_ string) *CodexExecutor {
	return NewCodexExecutor(&config.Config{})
}

func codexTestImageAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": baseURL,
			"api_key":  "test",
		},
	}
}

func codexTestImageRequest() cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   codexDefaultImageToolModel,
		Payload: []byte(`{"model":"gpt-image-2","prompt":"draw","response_format":"b64_json"}`),
	}
}

func codexTestImageOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(codexOpenAIImageSourceFormat),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: codexImagesGenerationsPath,
		},
	}
}

func codexImageCompletedPayload(result string) []byte {
	return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"created_at":123,"output":[{"type":"image_generation_call","result":%q,"output_format":"png"}],"tool_usage":{"image_gen":{"input_tokens":1,"output_tokens":2,"total_tokens":3}},"usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}}`, result))
}

func assertCodexImageStreamStatus(t *testing.T, err error, wantStatus int, wantClassification string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error does not expose StatusCode(): %T %v", err, err)
	}
	if got := status.StatusCode(); got != wantStatus {
		t.Fatalf("StatusCode() = %d, want %d; error=%v", got, wantStatus, err)
	}
	if !strings.Contains(err.Error(), "classification="+wantClassification) {
		t.Fatalf("error missing classification %q: %v", wantClassification, err)
	}
}

func assertCodexImageErrContains(t *testing.T, msg string, want string) {
	t.Helper()
	if !strings.Contains(msg, want) {
		t.Fatalf("error missing %q: %s", want, msg)
	}
}

func assertCodexImageErrNotContains(t *testing.T, msg string, forbidden string) {
	t.Helper()
	if strings.Contains(msg, forbidden) {
		t.Fatalf("error leaked forbidden content %q: %s", forbidden, msg)
	}
}

func TestCodexImageSafeSummaryValueRejectsStatusLikeSecrets(t *testing.T) {
	if got := codexImageSafeSummaryValue("status-" + strconv.Itoa(http.StatusTooManyRequests)); got != "status-429" {
		t.Fatalf("safe summary = %q, want status-429", got)
	}
}
