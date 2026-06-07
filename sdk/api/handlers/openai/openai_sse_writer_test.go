package openai

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func writeOpenAIChatSSEChunkString(t *testing.T, chunk string) string {
	t.Helper()

	var buf bytes.Buffer
	writeOpenAIChatSSEChunk(&buf, []byte(chunk))
	return buf.String()
}

func newOpenAIStreamTestHandler(t *testing.T) (*OpenAIAPIHandler, *httptest.ResponseRecorder, *gin.Context, http.Flusher) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	return h, recorder, c, flusher
}

func TestWriteOpenAIChatSSEChunk_WrapsJSONPayload(t *testing.T) {
	t.Parallel()

	got := writeOpenAIChatSSEChunkString(t, `{"id":"chunk"}`)
	want := "data: {\"id\":\"chunk\"}\n\n"
	if got != want {
		t.Fatalf("unexpected SSE output.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestWriteOpenAIChatSSEChunk_PreservesDataLine(t *testing.T) {
	t.Parallel()

	got := writeOpenAIChatSSEChunkString(t, `data: {"id":"chunk"}`)
	want := "data: {\"id\":\"chunk\"}\n\n"
	if got != want {
		t.Fatalf("unexpected SSE output.\nGot:  %q\nWant: %q", got, want)
	}
	if strings.Contains(got, "data: data:") {
		t.Fatalf("unexpected nested data wrapper in %q", got)
	}
}

func TestWriteOpenAIChatSSEChunk_WrapsDone(t *testing.T) {
	t.Parallel()

	got := writeOpenAIChatSSEChunkString(t, `[DONE]`)
	want := "data: [DONE]\n\n"
	if got != want {
		t.Fatalf("unexpected SSE output.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestWriteOpenAIChatSSEChunk_PreservesDataDone(t *testing.T) {
	t.Parallel()

	got := writeOpenAIChatSSEChunkString(t, `data: [DONE]`)
	want := "data: [DONE]\n\n"
	if got != want {
		t.Fatalf("unexpected SSE output.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestWriteOpenAIChatSSEChunk_PreservesEventFrame(t *testing.T) {
	t.Parallel()

	got := writeOpenAIChatSSEChunkString(t, "event: error\ndata: {\"error\":\"x\"}")
	want := "event: error\ndata: {\"error\":\"x\"}\n\n"
	if got != want {
		t.Fatalf("unexpected SSE output.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestWriteOpenAIChatSSEChunk_RepairsNestedDataLine(t *testing.T) {
	t.Parallel()

	got := writeOpenAIChatSSEChunkString(t, `data: data: {"id":"chunk"}`)
	want := "data: {\"id\":\"chunk\"}\n\n"
	if got != want {
		t.Fatalf("unexpected SSE output.\nGot:  %q\nWant: %q", got, want)
	}
	if strings.Contains(got, "data: data:") {
		t.Fatalf("unexpected nested data wrapper in %q", got)
	}
}

func TestWriteOpenAIChatSSEChunk_DoesNotRepairInvalidNestedData(t *testing.T) {
	t.Parallel()

	got := writeOpenAIChatSSEChunkString(t, `data: data: hello`)
	want := "data: data: hello\n\n"
	if got != want {
		t.Fatalf("unexpected SSE output.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestHandleStreamResult_PreservesPreFramedSSEDataLine(t *testing.T) {
	h, recorder, c, flusher := newOpenAIStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"id":"chunk","choices":[{"delta":{"content":"hi"}}]}`)
	close(data)
	close(errs)

	h.handleStreamResult(c, flusher, func(error) {}, data, errs)

	body := recorder.Body.String()
	if strings.Contains(body, "data: data:") {
		t.Fatalf("unexpected nested data wrapper in %q", body)
	}

	want := "data: {\"id\":\"chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	if body != want {
		t.Fatalf("unexpected streamed body.\nGot:  %q\nWant: %q", body, want)
	}
}
