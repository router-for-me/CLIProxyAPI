package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
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

func TestResponsesSSEFramerWaitsForEventFieldAfterData(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}

	framer.WriteChunk(&output, []byte(`data: {"response":{"id":"resp-1","status":"completed"}}`))
	if output.Len() != 0 {
		t.Fatalf("framer emitted data before a following event field arrived: %q", output.String())
	}

	framer.WriteChunk(&output, []byte("event: response.completed"))
	if framer.terminalEvent != "response.completed" {
		t.Fatalf("terminal event = %q, want response.completed", framer.terminalEvent)
	}
	got := output.String()
	if !strings.Contains(got, "data: ") || !strings.Contains(got, "event: response.completed") {
		t.Fatalf("framer did not preserve data-before-event fields in one frame: %q", got)
	}
}

func TestResponsesSSEFramerFlushesMultilineDataWithoutDelimiter(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}
	chunk := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}")
	framer.WriteChunk(&output, chunk)
	framer.Flush(&output)

	if framer.terminalEvent != "response.completed" || !strings.Contains(output.String(), "response.completed") {
		t.Fatalf("multiline data-only terminal frame was dropped: terminal=%q output=%q", framer.terminalEvent, output.String())
	}
}

func TestResponsesSSEFramerUsesPayloadErrorOverCompletedEvent(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{failureEvent: "response.failed"}
	framer.WriteChunk(&output, []byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\nevent: response.completed\n\n"))

	if framer.terminalEvent != "response.failed" || strings.Contains(output.String(), "event: response.completed") {
		t.Fatalf("payload error was overridden by completed event: terminal=%q output=%q", framer.terminalEvent, output.String())
	}
	if strings.Count(output.String(), "event: response.failed") != 1 {
		t.Fatalf("payload error output = %q, want one response.failed", output.String())
	}
}

func TestResponsesSSEFramerUsesErrorEventOverPayloadType(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}
	framer.WriteChunk(&output, []byte("event: error\ndata: {\"type\":\"provider.error\",\"message\":\"failed\"}\n\n"))
	if framer.terminalEvent != "error" {
		t.Fatalf("terminal event = %q, want error", framer.terminalEvent)
	}

	framer = &responsesSSEFramer{}
	framer.WriteChunk(&output, []byte("data: {\"response\":{\"error\":{\"message\":\"failed\"}}}\n\n"))
	if framer.terminalEvent != "error" {
		t.Fatalf("nested response error terminal event = %q, want error", framer.terminalEvent)
	}
}

func TestForwardResponsesStreamSeparatesDataOnlySSEChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}")
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
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

func TestForwardResponsesStreamRepairsEmptyCompletedOutputFromDoneItems(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs-1","summary":[]}}`)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{\"cmd\":\"pwd\"}","status":"completed"}}`)
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("expected 3 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	payload := strings.TrimPrefix(parts[2], "data: ")
	output := gjson.Get(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected repaired completed output with 2 items, got %s", output.Raw)
	}
	if got := gjson.Get(payload, "response.output.1.name").String(); got != "shell" {
		t.Fatalf("expected function_call name to be preserved, got %q in %s", got, payload)
	}
	if got := gjson.Get(payload, "response.output.1.arguments").String(); got != `{"cmd":"pwd"}` {
		t.Fatalf("expected function_call arguments to be preserved, got %q in %s", got, payload)
	}
}

func TestForwardResponsesStreamRepairsMixedIndexedAndUnindexedDoneItems(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{}","status":"completed"}}`)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`)
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("expected 3 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	payload := strings.TrimPrefix(parts[2], "data: ")
	output := gjson.Get(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected repaired completed output with 2 items, got %s", output.Raw)
	}
	if got := gjson.Get(payload, "response.output.0.name").String(); got != "shell" {
		t.Fatalf("expected indexed function_call to be preserved first, got %q in %s", got, payload)
	}
	if got := gjson.Get(payload, "response.output.1.id").String(); got != "msg-1" {
		t.Fatalf("expected unindexed message to be appended, got %q in %s", got, payload)
	}
}

func TestForwardResponsesStreamRepairsMultilineCompletedOutputAsSSEDataLines(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","arguments":"{}"}}`)
	data <- []byte("data: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	completedFrame := []byte(parts[1])
	for _, line := range strings.Split(parts[1], "\n") {
		if line != "" && !strings.HasPrefix(line, "data: ") {
			t.Fatalf("expected every completed payload line to be an SSE data line, got %q in %q", line, parts[1])
		}
	}

	payload, ok := responsesSSEDataPayload(completedFrame)
	if !ok {
		t.Fatalf("expected completed frame to contain data payload: %q", parts[1])
	}
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected repaired completed output with 1 item, got %s from %q", output.Raw, payload)
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

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	wantPrefix := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("unexpected split-event framing.\nGot:        %q\nWant prefix: %q", got, wantPrefix)
	}
	if !strings.Contains(got, "event: error") {
		t.Fatalf("unterminated framing test stream did not end with an error: %q", got)
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

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	if !strings.HasPrefix(got, string(chunk)) {
		t.Fatalf("unexpected full-event framing.\nGot:        %q\nWant prefix: %q", got, string(chunk))
	}
	if !strings.Contains(got, "event: error") {
		t.Fatalf("unterminated framing test stream did not end with an error: %q", got)
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

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	wantPrefix := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("unexpected split-data framing.\nGot:        %q\nWant prefix: %q", got, wantPrefix)
	}
	if !strings.Contains(got, "event: error") {
		t.Fatalf("unterminated framing test stream did not end with an error: %q", got)
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

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	if strings.Contains(got, `data: {"type":"response.created"`) {
		t.Fatalf("incomplete trailing data was not dropped on flush: %q", got)
	}
	if !strings.Contains(got, "event: error") {
		t.Fatalf("unterminated framing test stream did not end with an error: %q", got)
	}
}

func TestForwardResponsesStreamErrorEventPreservesNestedError(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)
	c.Request.Header.Set("User-Agent", "codex_vscode/0.153.4 (Ubuntu 22.4.0; x86_64)")

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)

	go func() {
		data <- []byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n")
		data <- []byte("event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":1}\n\n")
		close(data)

		errText := `{"error":{"type":"invalid_request","code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk. If this seems wrong, try rephrasing your request. To get authorized for security work, join the Trusted Access for Cyber program: https://chatgpt.com/cyber","param":null}}`
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New(errText),
		}
		close(errs)
	}()

	framer := &responsesSSEFramer{}
	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)

	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\n") {
		t.Fatalf("expected event: error in output, got: %q", body)
	}

	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(parts) < 3 {
		t.Fatalf("expected at least 3 SSE events, got %d. Body: %q", len(parts), body)
	}

	lastPart := strings.TrimSpace(parts[len(parts)-1])
	if !strings.HasPrefix(lastPart, "event: error\ndata: ") {
		t.Fatalf("last event is not error event: %q", lastPart)
	}

	jsonPayload := strings.TrimSpace(strings.TrimPrefix(lastPart, "event: error\ndata: "))
	var payload struct {
		Type           string         `json:"type"`
		Error          map[string]any `json:"error"`
		SequenceNumber int            `json:"sequence_number"`
	}
	if errUnmarshal := json.Unmarshal([]byte(jsonPayload), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal error event payload: %v (raw: %s)", errUnmarshal, jsonPayload)
	}
	if payload.Type != "error" {
		t.Fatalf("payload.Type = %q, want error", payload.Type)
	}
	if payload.SequenceNumber != 2 {
		t.Fatalf("payload.SequenceNumber = %d, want 2", payload.SequenceNumber)
	}
	if payload.Error["type"] != "invalid_request" {
		t.Fatalf("payload.Error.type = %v, want invalid_request", payload.Error["type"])
	}
	if payload.Error["code"] != "cyber_policy" {
		t.Fatalf("payload.Error.code = %v, want cyber_policy", payload.Error["code"])
	}
	if param, exists := payload.Error["param"]; !exists || param != nil {
		t.Fatalf("payload.Error.param = %v, want null/nil", param)
	}
}

func TestResponsesSSEFramerRepairErrorPayloadCalculatesCorrectSequenceNumber(t *testing.T) {
	// Case 1: First frame is an error without sequence_number -> should be 0
	framer1 := &responsesSSEFramer{}
	var out1 bytes.Buffer
	framer1.WriteChunk(&out1, []byte("event: error\ndata: {\"error\":{\"code\":\"bad_request\"}}\n\n"))
	var p1 struct {
		Type           string         `json:"type"`
		SequenceNumber int            `json:"sequence_number"`
		Error          map[string]any `json:"error"`
	}
	payload1 := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out1.String()), "event: error\ndata: "))
	if err := json.Unmarshal([]byte(payload1), &p1); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if p1.SequenceNumber != 0 {
		t.Fatalf("first frame error sequence_number = %d, want 0", p1.SequenceNumber)
	}

	// Case 2: Two data frames then an error frame without sequence_number -> should be 2
	framer2 := &responsesSSEFramer{}
	var out2 bytes.Buffer
	framer2.WriteChunk(&out2, []byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n"))
	framer2.WriteChunk(&out2, []byte("event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":1}\n\n"))
	out2.Reset()
	framer2.WriteChunk(&out2, []byte("event: error\ndata: {\"error\":{\"code\":\"cyber_policy\"}}\n\n"))
	var p2 struct {
		Type           string         `json:"type"`
		SequenceNumber int            `json:"sequence_number"`
		Error          map[string]any `json:"error"`
	}
	payload2 := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out2.String()), "event: error\ndata: "))
	if err := json.Unmarshal([]byte(payload2), &p2); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if p2.SequenceNumber != 2 {
		t.Fatalf("third frame error sequence_number = %d, want 2", p2.SequenceNumber)
	}
}

func TestForwardResponsesStreamErrorEventPreservesExplicitSequenceNumber(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)
	c.Request.Header.Set("User-Agent", "codex_vscode/0.153.4")

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)

	go func() {
		close(data)
		errText := `{"error":{"type":"invalid_request","code":"custom"},"sequence_number":9}`
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New(errText),
		}
		close(errs)
	}()

	framer := &responsesSSEFramer{}
	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)

	body := recorder.Body.String()
	var p struct {
		Type           string         `json:"type"`
		SequenceNumber int            `json:"sequence_number"`
		Error          map[string]any `json:"error"`
	}
	lastPart := strings.TrimSpace(strings.Split(strings.TrimSpace(body), "\n\n")[0])
	payload := strings.TrimSpace(strings.TrimPrefix(lastPart, "event: error\ndata: "))
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("unmarshal error payload: %v (raw: %s)", err, payload)
	}
	if p.SequenceNumber != 9 {
		t.Fatalf("explicit sequence_number = %d, want 9", p.SequenceNumber)
	}
}

func TestResponsesStreamErrorTextSanitizesNestedSensitiveFieldsAndDotKeys(t *testing.T) {
	inputJSON := `{"error":{"metadata":{"message":"Bearer test-secret"},"vendor.detail":"Bearer test-secret","access_token":"test-secret","normal_key":"safe_value"},"sequence_number":3}`
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(inputJSON),
	}
	out := responsesStreamErrorText(errMsg, http.StatusBadRequest)

	var parsed struct {
		Error          map[string]any `json:"error"`
		SequenceNumber int            `json:"sequence_number"`
	}
	if errUnmarshal := json.Unmarshal([]byte(out), &parsed); errUnmarshal != nil {
		t.Fatalf("unmarshal error text output: %v (raw: %s)", errUnmarshal, out)
	}

	if parsed.SequenceNumber != 3 {
		t.Fatalf("sequence_number = %d, want 3", parsed.SequenceNumber)
	}
	if parsed.Error["access_token"] != "[REDACTED]" {
		t.Fatalf("access_token was not redacted: %v", parsed.Error["access_token"])
	}
	if parsed.Error["vendor.detail"] != "Bearer [REDACTED]" {
		t.Fatalf("vendor.detail was not properly redacted without path corruption: %v", parsed.Error["vendor.detail"])
	}
	meta, ok := parsed.Error["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata is not a map: %v", parsed.Error["metadata"])
	}
	if meta["message"] != "Bearer [REDACTED]" {
		t.Fatalf("nested metadata.message was not redacted: %v", meta["message"])
	}
	if parsed.Error["normal_key"] != "safe_value" {
		t.Fatalf("normal_key = %v, want safe_value", parsed.Error["normal_key"])
	}
}

func TestResponsesStreamErrorTextPreservesTokenCountersAndLargeInts(t *testing.T) {
	inputJSON := `{"error":{"input_tokens":42,"token_limit":8192,"request_id":9007199254740993,"access_token":"secret123"}}`
	errMsg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(inputJSON),
	}
	out := responsesStreamErrorText(errMsg, http.StatusBadRequest)

	// Verify exact raw representation doesn't lose precision for 9007199254740993
	if !strings.Contains(out, "9007199254740993") {
		t.Fatalf("large integer precision was lost: %s", out)
	}
	if strings.Contains(out, "9007199254740992") {
		t.Fatalf("large integer was corrupted by float64 conversion: %s", out)
	}

	var parsed struct {
		Error map[string]any `json:"error"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if errUnmarshal := dec.Decode(&parsed); errUnmarshal != nil {
		t.Fatalf("unmarshal error: %v", errUnmarshal)
	}

	if parsed.Error["access_token"] != "[REDACTED]" {
		t.Fatalf("access_token should be redacted: %v", parsed.Error["access_token"])
	}
	if parsed.Error["input_tokens"] != json.Number("42") {
		t.Fatalf("input_tokens should remain number 42, got %v", parsed.Error["input_tokens"])
	}
	if parsed.Error["token_limit"] != json.Number("8192") {
		t.Fatalf("token_limit should remain number 8192, got %v", parsed.Error["token_limit"])
	}
}
