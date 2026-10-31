package codex

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestOAuthTransportPreservesCancellationClassification(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		resetCodexRefreshGroupForTest()
		auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) { return nil, cause })}}
		_, err := auth.RefreshTokens(context.Background(), "synthetic-context-R0")
		if !errors.Is(err, cause) {
			t.Fatalf("lost cancellation class %v", cause)
		}
	}
}
