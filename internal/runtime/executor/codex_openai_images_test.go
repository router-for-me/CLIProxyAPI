package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
