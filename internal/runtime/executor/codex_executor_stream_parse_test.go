package executor

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadCodexCompletedEvent_FindsCompletedEvent(t *testing.T) {
	body := strings.NewReader("data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n")
	var chunks [][]byte

	event, err := readCodexCompletedEvent(body, func(chunk []byte) {
		chunks = append(chunks, append([]byte(nil), chunk...))
	})
	if err != nil {
		t.Fatalf("readCodexCompletedEvent error = %v", err)
	}
	if got := string(event); got != `{"type":"response.completed","response":{"id":"resp_1"}}` {
		t.Fatalf("event = %s", got)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks len = %d, want 2", len(chunks))
	}
}

func TestReadCodexCompletedEvent_ReturnsTimeoutWhenMissingCompleted(t *testing.T) {
	body := strings.NewReader("data: {\"type\":\"response.created\"}\n")

	_, err := readCodexCompletedEvent(body, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("err type = %T, want statusErr", err)
	}
	if se.code != 408 {
		t.Fatalf("status code = %d, want 408", se.code)
	}
}

type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("boom")
}

func TestReadCodexCompletedEvent_PropagatesScannerError(t *testing.T) {
	_, err := readCodexCompletedEvent(failingReader{}, nil)
	if err == nil {
		t.Fatal("expected scanner error, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadCodexCompletedEvent_NilBody(t *testing.T) {
	_, err := readCodexCompletedEvent(io.Reader(nil), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("err type = %T, want statusErr", err)
	}
	if se.code != 408 {
		t.Fatalf("status code = %d, want 408", se.code)
	}
}
