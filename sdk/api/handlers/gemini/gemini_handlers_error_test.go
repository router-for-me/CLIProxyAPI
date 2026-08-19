package gemini

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

// TestPendingGeminiStreamErrorUsesBufferedError ensures a buffered upstream
// error is surfaced when the stream closes without data. Regression guard for
// the handleStreamGenerateContent branch that previously committed HTTP 200
// SSE headers for a failed empty stream.
func TestPendingGeminiStreamErrorUsesBufferedError(t *testing.T) {
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: 500, Error: errors.New("empty_completion: stream closed without data")}

	errMsg := pendingGeminiStreamError(errs)
	if errMsg == nil {
		t.Fatal("expected pending stream error")
	}
	if errMsg.StatusCode != 500 {
		t.Fatalf("unexpected status code: %d", errMsg.StatusCode)
	}
}

// TestPendingGeminiStreamErrorWithoutErrorCommitsSuccess ensures a cleanly
// closed stream with no buffered error is treated as a normal completion.
func TestPendingGeminiStreamErrorWithoutErrorCommitsSuccess(t *testing.T) {
	errs := make(chan *interfaces.ErrorMessage, 1)

	errMsg := pendingGeminiStreamError(errs)
	if errMsg != nil {
		t.Fatalf("expected success, got error: %v", errMsg.Error)
	}
}
