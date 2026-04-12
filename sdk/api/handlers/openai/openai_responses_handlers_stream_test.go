package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

func newResponsesStreamTestHandler(t *testing.T) (*OpenAIResponsesAPIHandler, *httptest.ResponseRecorder, *gin.Context, http.Flusher) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	return h, recorder, c, flusher
}

func TestForwardResponsesStreamSeparatesDataOnlySSEChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}")
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil, nil)
	body := recorder.Body.String()
	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), body)
	}

	expectedPart1 := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}"
	if parts[0] != expectedPart1 {
		t.Errorf("unexpected first event.\nGot: %q\nWant: %q", parts[0], expectedPart1)
	}

	expectedPart2 := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"function_call\",\"arguments\":\"{}\"}]}}"
	if parts[1] != expectedPart2 {
		t.Errorf("unexpected second event.\nGot: %q\nWant: %q", parts[1], expectedPart2)
	}
}

func TestForwardResponsesStreamReassemblesSplitSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("event: response.created")
	data <- []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}")
	data <- []byte("\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil, nil)

	got := strings.TrimSuffix(recorder.Body.String(), "\n")
	want := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n"
	if got != want {
		t.Fatalf("unexpected split-event framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestForwardResponsesStreamPreservesValidFullSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	chunk := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	data <- chunk
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil, nil)

	got := strings.TrimSuffix(recorder.Body.String(), "\n")
	if got != string(chunk) {
		t.Fatalf("unexpected full-event framing.\nGot:  %q\nWant: %q", got, string(chunk))
	}
}

func TestForwardResponsesStreamBuffersSplitDataPayloadChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	data <- []byte(",\"response\":{\"id\":\"resp-1\"}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil, nil)

	got := recorder.Body.String()
	want := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n\n"
	if got != want {
		t.Fatalf("unexpected split-data framing.\nGot:  %q\nWant: %q", got, want)
	}
}

func TestResponsesSSENeedsLineBreakSkipsChunksThatAlreadyStartWithNewline(t *testing.T) {
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\n")) {
		t.Fatal("expected no injected newline before newline-only chunk")
	}
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\r\n")) {
		t.Fatal("expected no injected newline before CRLF chunk")
	}
}

func TestForwardResponsesStreamDropsIncompleteTrailingDataChunkOnFlush(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil, nil)

	if got := recorder.Body.String(); got != "\n" {
		t.Fatalf("expected incomplete trailing data to be dropped on flush.\nGot: %q", got)
	}
}

func TestForwardResponsesStreamSynthesizesMissingCompletedEvent(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 4)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp-1\",\"created_at\":123,\"model\":\"gpt-5.4\"}}")
	data <- []byte("data: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"item\":{\"id\":\"msg-1\",\"type\":\"message\",\"status\":\"in_progress\",\"content\":[],\"role\":\"assistant\"},\"output_index\":0}")
	data <- []byte("data: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"item_id\":\"msg-1\",\"output_index\":0,\"delta\":\"hello world\"}")
	data <- []byte("data: {\"type\":\"response.output_text.done\",\"sequence_number\":4,\"item_id\":\"msg-1\",\"output_index\":0,\"text\":\"hello world\"}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil, nil)

	body := recorder.Body.String()
	if !strings.Contains(body, "\"type\":\"response.completed\"") {
		t.Fatalf("expected synthesized response.completed event. Body: %q", body)
	}
	if !strings.Contains(body, "\"hello world\"") {
		t.Fatalf("expected synthesized completion to preserve assistant text. Body: %q", body)
	}

	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	last := parts[len(parts)-1]
	if !strings.HasPrefix(last, "data: ") {
		t.Fatalf("expected last SSE frame to be data-only completion, got %q", last)
	}
	payload := strings.TrimSpace(strings.TrimPrefix(last, "data:"))
	if gjson.Get(payload, "type").String() != "response.completed" {
		t.Fatalf("last payload type = %q, want response.completed", gjson.Get(payload, "type").String())
	}
	if gjson.Get(payload, "response.output.0.content.0.text").String() != "hello world" {
		t.Fatalf("synthetic response.output text = %q, want %q", gjson.Get(payload, "response.output.0.content.0.text").String(), "hello world")
	}
}

func TestForwardResponsesStreamRecoversTransportErrorFrame(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 5)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp-1\",\"created_at\":123,\"model\":\"gpt-5.4\"}}")
	data <- []byte("data: {\"type\":\"response.output_item.added\",\"sequence_number\":2,\"item\":{\"id\":\"msg-1\",\"type\":\"message\",\"status\":\"in_progress\",\"content\":[],\"role\":\"assistant\"},\"output_index\":0}")
	data <- []byte("data: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"item_id\":\"msg-1\",\"output_index\":0,\"delta\":\"partial answer\"}")
	data <- []byte("event: error\n")
	data <- []byte("data: {\"type\":\"error\",\"code\":\"internal_server_error\",\"message\":\"stream error: stream ID 219; INTERNAL_ERROR; received from peer\",\"sequence_number\":4}\n\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil, nil)

	body := recorder.Body.String()
	if strings.Contains(body, "event: error") {
		t.Fatalf("expected error event to be replaced by completion. Body: %q", body)
	}
	if strings.Contains(body, "\"type\":\"error\"") {
		t.Fatalf("expected upstream transport error to be suppressed. Body: %q", body)
	}
	if !strings.Contains(body, "\"type\":\"response.completed\"") {
		t.Fatalf("expected recovered response.completed event. Body: %q", body)
	}

	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	last := parts[len(parts)-1]
	payload := strings.TrimSpace(strings.TrimPrefix(last, "data:"))
	if gjson.Get(payload, "type").String() != "response.completed" {
		t.Fatalf("last payload type = %q, want response.completed", gjson.Get(payload, "type").String())
	}
	if gjson.Get(payload, "response.output.0.content.0.text").String() != "partial answer" {
		t.Fatalf("recovered response.output text = %q, want %q", gjson.Get(payload, "response.output.0.content.0.text").String(), "partial answer")
	}
}

func TestForwardResponsesStreamPatchesEmptyCompletedOutput(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.output_item.done\",\"sequence_number\":1,\"item\":{\"id\":\"msg-1\",\"type\":\"message\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\",\"annotations\":[],\"logprobs\":[]}],\"role\":\"assistant\"},\"output_index\":0}")
	data <- []byte("data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp-1\",\"created_at\":123,\"model\":\"gpt-5.4\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil, nil)

	body := recorder.Body.String()
	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	last := parts[len(parts)-1]
	payload := strings.TrimSpace(strings.TrimPrefix(last, "data:"))
	if gjson.Get(payload, "response.output.0.content.0.text").String() != "ok" {
		t.Fatalf("patched response.output text = %q, want %q. Body: %q", gjson.Get(payload, "response.output.0.content.0.text").String(), "ok", body)
	}
}

func TestForwardResponsesStreamCancelsOnDownstreamWriteError(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	writeErr := errors.New("broken pipe")
	c.Writer = &failingResponsesWriter{
		ResponseWriter: c.Writer,
		failAfter:      1,
		err:            writeErr,
	}
	flusher = c.Writer.(http.Flusher)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	data <- []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"later\"}\n\n")
	close(data)
	close(errs)

	var cancelErr error
	h.forwardResponsesStream(c, flusher, func(err error) { cancelErr = err }, data, errs, nil, nil)

	if !errors.Is(cancelErr, writeErr) {
		t.Fatalf("cancel error = %v, want %v", cancelErr, writeErr)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"type\":\"response.created\"") {
		t.Fatalf("expected first chunk to be written before failure. Body: %q", body)
	}
	if strings.Contains(body, "later") {
		t.Fatalf("expected stream to stop after downstream write failure. Body: %q", body)
	}
}

type failingResponsesWriter struct {
	gin.ResponseWriter
	failAfter int
	err       error
}

func (w *failingResponsesWriter) Write(data []byte) (int, error) {
	if w.failAfter == 0 {
		return 0, w.err
	}
	w.failAfter--
	return w.ResponseWriter.Write(data)
}

func (w *failingResponsesWriter) WriteString(data string) (int, error) {
	if w.failAfter == 0 {
		return 0, w.err
	}
	w.failAfter--
	return w.ResponseWriter.WriteString(data)
}
