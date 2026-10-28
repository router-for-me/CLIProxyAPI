package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestResponsesSSEFramerFragmentedMetadataDataField(t *testing.T) {
	payload := `{"type":"response.metadata","metadata":{"marker":"private"}}`
	for _, tc := range []struct {
		name          string
		lineEnding    string
		isCodexClient bool
	}{
		{name: "standard-lf", lineEnding: "\n"},
		{name: "codex-lf", lineEnding: "\n", isCodexClient: true},
		{name: "standard-crlf", lineEnding: "\r\n"},
		{name: "codex-crlf", lineEnding: "\r\n", isCodexClient: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := "event: response.metadata" + tc.lineEnding + "data:"
			frame := []byte(prefix + " " + payload + tc.lineEnding + tc.lineEnding)
			for _, split := range []int{len(prefix), len(prefix) + 1} {
				var output bytes.Buffer
				framer := &responsesSSEFramer{isCodexClient: tc.isCodexClient}
				framer.WriteChunk(&output, frame[:split])
				framer.WriteChunk(&output, frame[split:])
				framer.WriteChunk(&output, []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
				got := output.String()
				if tc.isCodexClient {
					if !strings.Contains(got, prefix+" "+payload) {
						t.Fatalf("split %d: Codex metadata missing or damaged: %q", split, got)
					}
				} else if strings.Contains(got, "response.metadata") || strings.Contains(got, "private") {
					t.Fatalf("split %d: private metadata leaked: %q", split, got)
				}
				if !strings.Contains(got, "response.completed") {
					t.Fatalf("split %d: completion missing: %q", split, got)
				}
			}
		})
	}
}

func TestForwardResponsesStreamEmptyMetadataBeforeFragmentedCompletion(t *testing.T) {
	completed := []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	for split := 1; split <= len("event:"); split++ {
		h, recorder, c, flusher := newResponsesStreamTestHandler(t)
		data := make(chan []byte, 3)
		errs := make(chan *interfaces.ErrorMessage)
		data <- []byte("event: response.metadata\ndata:")
		data <- completed[:split]
		data <- completed[split:]
		close(data)
		close(errs)
		h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
		got := recorder.Body.String()
		if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "event: response.completed") {
			t.Fatalf("split %d: empty metadata consumed completion: %q", split, got)
		}
	}
}

func TestForwardResponsesStreamEmptyMetadataBeforeLeadingSeparator(t *testing.T) {
	for _, prefix := range []string{"\n", "\r\n", ": keepalive\n", "id: event-42\n", "retry: 1000\n", "x-extra: value\n"} {
		h, recorder, c, flusher := newResponsesStreamTestHandler(t)
		data := make(chan []byte, 2)
		errs := make(chan *interfaces.ErrorMessage)
		data <- []byte("event: response.metadata\ndata:")
		data <- []byte(prefix + "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
		close(data)
		close(errs)
		h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
		got := recorder.Body.String()
		if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "response.completed") {
			t.Fatalf("prefix %q: empty metadata consumed completion: %q", prefix, got)
		}
	}
}

func TestForwardResponsesStreamEmptyMetadataBeforeFragmentedFields(t *testing.T) {
	for _, prefix := range []string{"id: event-42\n", "retry: 1000\n", "x-extra: value\n"} {
		field := []byte(prefix + "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
		for split := 1; split < strings.IndexByte(prefix, ':')+1; split++ {
			h, recorder, c, flusher := newResponsesStreamTestHandler(t)
			data := make(chan []byte, 3)
			errs := make(chan *interfaces.ErrorMessage)
			data <- []byte("event: response.metadata\ndata:")
			data <- field[:split]
			data <- field[split:]
			close(data)
			close(errs)
			h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
			got := recorder.Body.String()
			if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "response.completed") {
				t.Fatalf("prefix %q split %d: empty metadata consumed completion: %q", prefix, split, got)
			}
		}
	}
}

func TestForwardResponsesStreamEmptyMetadataBeforeDataOnlyCompletion(t *testing.T) {
	completed := []byte("data: {\"type\":\"response.completed\"}\n\n")
	for split := 1; split <= len("data:"); split++ {
		h, recorder, c, flusher := newResponsesStreamTestHandler(t)
		data := make(chan []byte, 3)
		errs := make(chan *interfaces.ErrorMessage)
		data <- []byte("event: response.metadata\ndata:")
		data <- completed[:split]
		data <- completed[split:]
		close(data)
		close(errs)
		h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
		got := recorder.Body.String()
		if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "response.completed") {
			t.Fatalf("split %d: empty metadata consumed data-only completion: %q", split, got)
		}
	}
}

func TestForwardResponsesStreamEmptyMetadataBeforeDataThenEvent(t *testing.T) {
	created := []byte(`data: {"response":{"id":"resp-1","status":"completed"}}`)
	completed := []byte("event: response.completed\n\n")
	for split := 1; split <= len("event:"); split++ {
		h, recorder, c, flusher := newResponsesStreamTestHandler(t)
		data := make(chan []byte, 4)
		errs := make(chan *interfaces.ErrorMessage)
		data <- []byte("event: response.metadata\ndata:")
		data <- created
		data <- completed[:split]
		data <- completed[split:]
		close(data)
		close(errs)
		h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
		got := recorder.Body.String()
		if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "event: response.completed") || !strings.Contains(got, "resp-1") {
			t.Fatalf("split %d: data-before-event completion lost: %q", split, got)
		}
	}

	h, recorder, c, flusher := newResponsesStreamTestHandler(t)
	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("event: response.metadata\ndata:")
	data <- append(append(bytes.Clone(created), '\n'), completed...)
	close(data)
	close(errs)
	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	got := recorder.Body.String()
	if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "event: response.completed") || !strings.Contains(got, "resp-1") {
		t.Fatalf("combined data-before-event completion lost: %q", got)
	}
}

func TestForwardResponsesStreamEmptyMetadataBeforeDataFieldThenEvent(t *testing.T) {
	for _, field := range []string{"id: event-42", "retry: 1000", ": keepalive", "x-extra: value"} {
		for split := 1; split <= len(field); split++ {
			h, recorder, c, flusher := newResponsesStreamTestHandler(t)
			data := make(chan []byte, 5)
			errs := make(chan *interfaces.ErrorMessage)
			data <- []byte("event: response.metadata\ndata:")
			data <- []byte(`data: {"response":{"id":"resp-1","status":"completed"}}`)
			data <- []byte(field[:split])
			data <- []byte(field[split:])
			data <- []byte("event: response.completed\n\n")
			close(data)
			close(errs)
			h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
			got := recorder.Body.String()
			if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "event: response.completed") || !strings.Contains(got, "resp-1") {
				t.Fatalf("field %q split %d: data-before-event completion lost: %q", field, split, got)
			}
		}
	}
}

func TestForwardResponsesStreamEmptyMetadataBeforeDataAndFragmentedEventAfterLineBreak(t *testing.T) {
	for _, field := range []string{"", "id: event-42\n", ": keepalive\n"} {
		for split := 1; split < len("event:"); split++ {
			h, recorder, c, flusher := newResponsesStreamTestHandler(t)
			data := make(chan []byte, 5)
			errs := make(chan *interfaces.ErrorMessage)
			data <- []byte("event: response.metadata\ndata:")
			data <- []byte("data: {\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n")
			if field != "" {
				data <- []byte(field)
			}
			event := []byte("event: response.completed\n\n")
			data <- event[:split]
			data <- event[split:]
			close(data)
			close(errs)
			h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
			got := recorder.Body.String()
			if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "event: response.completed") || !strings.Contains(got, "resp-1") {
				t.Fatalf("field %q split %d: completion lost: %q", field, split, got)
			}
		}
	}
}

func TestForwardResponsesStreamEmptyMetadataBeforeDataWithFragmentedEvent(t *testing.T) {
	for _, field := range []string{"", "id: event-42\n", ": keepalive\n"} {
		for split := 1; split < len("event:"); split++ {
			h, recorder, c, flusher := newResponsesStreamTestHandler(t)
			data := make(chan []byte, 3)
			errs := make(chan *interfaces.ErrorMessage)
			data <- []byte("event: response.metadata\ndata:")
			event := []byte("event: response.completed\n\n")
			data <- append([]byte("data: {\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n"+field), event[:split]...)
			data <- event[split:]
			close(data)
			close(errs)
			h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
			got := recorder.Body.String()
			if strings.Contains(got, "response.metadata") || strings.Contains(got, "event: error") || !strings.Contains(got, "event: response.completed") || !strings.Contains(got, "resp-1") {
				t.Fatalf("field %q split %d: completion lost: %q", field, split, got)
			}
		}
	}
}

func TestResponsesSSEFramerPreservesEmptyEvent(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}
	framer.WriteChunk(&output, []byte("event: custom\ndata:"))
	if output.Len() != 0 {
		t.Fatalf("empty data field was emitted before completion: %q", output.String())
	}
	framer.WriteChunk(&output, []byte("\n\n"))
	if got, want := output.String(), "event: custom\ndata:\n\n"; got != want {
		t.Fatalf("empty data frame = %q, want %q", got, want)
	}
}

func TestResponsesSSEFramerMetadataWithFieldPrefixInJSONString(t *testing.T) {
	for _, marker := range []string{"data: private", "event: private", "id: private", "retry: private", ": private", `escaped " data: private`, `escaped \ event: private`} {
		for _, isCodexClient := range []bool{false, true} {
			encodedMarker, errMarshal := json.Marshal(marker)
			if errMarshal != nil {
				t.Fatalf("marshal marker: %v", errMarshal)
			}
			payload := `{"type":"response.metadata","metadata":{"marker":` + string(encodedMarker) + `}}`
			frame := []byte("data: " + payload + "\n\n")
			field := strings.Index(string(frame), strings.TrimPrefix(marker, `escaped " `))
			if field < 0 {
				field = strings.Index(string(frame), "event: private")
			}
			if field < 0 {
				t.Fatalf("marker %q not found in %q", marker, frame)
			}
			var output bytes.Buffer
			framer := &responsesSSEFramer{isCodexClient: isCodexClient}
			framer.WriteChunk(&output, frame[:field])
			framer.WriteChunk(&output, frame[field:])
			framer.WriteChunk(&output, []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
			got := output.String()
			if isCodexClient {
				if !strings.Contains(got, payload) {
					t.Fatalf("marker %q: Codex payload corrupted: %q", marker, got)
				}
			} else if strings.Contains(got, "response.metadata") || strings.Contains(got, "private") {
				t.Fatalf("marker %q: metadata leaked: %q", marker, got)
			}
			if !strings.Contains(got, "response.completed") {
				t.Fatalf("marker %q: completion missing: %q", marker, got)
			}
		}
	}
}

func TestResponsesSSEFramerFiltersMalformedPrivatePayload(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}
	framer.WriteChunk(&output, []byte("data: {\"type\":\"response.metadata\",\"metadata\":{\"marker\":\"private\"}\n\n"))
	framer.WriteChunk(&output, []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
	if got := output.String(); strings.Contains(got, "response.metadata") || strings.Contains(got, "private") {
		t.Fatalf("malformed private payload leaked: %q", got)
	}
}

func TestResponsesSSEFramerKeepsEmptyCommentInMultilineData(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}
	for _, chunk := range []string{
		`data: {"type"`,
		":",
		`data: :"response.metadata","metadata":{"marker":"private"}}` + "\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
	} {
		framer.WriteChunk(&output, []byte(chunk))
	}
	got := output.String()
	if strings.Contains(got, "response.metadata") || strings.Contains(got, "private") || !strings.Contains(got, "response.completed") {
		t.Fatalf("metadata with empty comment leaked: %q", got)
	}
}

func TestSSEFrameAccumulatorKeepsEmptyCommentInMultilineData(t *testing.T) {
	acc := &sseFrameAccumulator{}
	var frames [][]byte
	for _, chunk := range []string{
		`data: {"type"`,
		":",
		`data: :"response.completed","response":{"output":[]}}` + "\n\n",
	} {
		frames = append(frames, acc.AddChunk([]byte(chunk))...)
	}
	if len(frames) != 1 {
		t.Fatalf("frames = %q, want one", frames)
	}
	payload, ok := responsesSSEDataPayload(frames[0])
	if !ok || !json.Valid(payload) || gjson.GetBytes(payload, "type").String() != "response.completed" {
		t.Fatalf("empty comment corrupted image data: %q", frames[0])
	}
}
