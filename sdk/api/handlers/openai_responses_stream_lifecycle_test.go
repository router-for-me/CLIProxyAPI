package handlers

import (
	"strings"
	"testing"
)

func TestOpenAIResponsesStreamLifecycle_SyntheticCompletionChunkHasSSEDelimiter(t *testing.T) {
	l := &openAIResponsesStreamLifecycle{}
	chunk := l.SyntheticCompletionChunk()
	if !strings.HasSuffix(string(chunk), "\n\n") {
		t.Fatalf("synthetic completion chunk missing delimiter: %q", string(chunk))
	}
}
