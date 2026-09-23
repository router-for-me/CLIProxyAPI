package helps

import (
	"strings"
	"testing"
)

func TestWriteAttemptResponseStopsAtCap(t *testing.T) {
	attempt := &upstreamAttempt{response: &strings.Builder{}}
	chunk := []byte(strings.Repeat("x", 4096))

	// Write well past the cap.
	for i := 0; i < (maxAttemptResponseLogBytes/len(chunk))+8; i++ {
		writeAttemptResponse(nil, attempt, chunk)
	}

	if got := attempt.response.Len(); got > maxAttemptResponseLogBytes {
		t.Fatalf("buffered %d bytes, want at most %d", got, maxAttemptResponseLogBytes)
	}
	if !attempt.bodyTruncated {
		t.Fatalf("expected bodyTruncated to be set once the cap was reached")
	}
}

func TestWriteAttemptResponseKeepsSmallBodiesIntact(t *testing.T) {
	attempt := &upstreamAttempt{response: &strings.Builder{}}
	writeAttemptResponse(nil, attempt, []byte("small body"))

	if attempt.response.String() != "small body" {
		t.Fatalf("buffer = %q, want %q", attempt.response.String(), "small body")
	}
	if attempt.bodyTruncated {
		t.Fatalf("a small body must not be marked truncated")
	}
}
