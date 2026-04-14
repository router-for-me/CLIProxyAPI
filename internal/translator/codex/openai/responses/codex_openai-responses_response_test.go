package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToOpenAIResponses_WrapsCompletedAsSSEFrame(t *testing.T) {
	chunks := ConvertCodexResponseToOpenAIResponses(nil, "", nil, nil, []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}"), nil)
	if len(chunks) != 1 {
		t.Fatalf("chunks len = %d, want 1", len(chunks))
	}
	chunk := string(chunks[0])
	if !strings.HasPrefix(chunk, "event: response.completed\n") {
		t.Fatalf("missing event prefix: %q", chunk)
	}
	if !strings.HasSuffix(chunk, "\n\n") {
		t.Fatalf("missing SSE delimiter: %q", chunk)
	}
	if got := gjson.GetBytes([]byte(chunk[strings.Index(chunk, "data: ")+6:len(chunk)-2]), "type").String(); got != "response.completed" {
		t.Fatalf("payload type = %q, want response.completed", got)
	}
}

func TestConvertCodexResponseToOpenAIResponses_PreservesFullSSEFrame(t *testing.T) {
	raw := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")
	chunks := ConvertCodexResponseToOpenAIResponses(nil, "", nil, nil, raw, nil)
	if len(chunks) != 1 {
		t.Fatalf("chunks len = %d, want 1", len(chunks))
	}
	if string(chunks[0]) != string(raw) {
		t.Fatalf("unexpected chunk: %q", string(chunks[0]))
	}
}
