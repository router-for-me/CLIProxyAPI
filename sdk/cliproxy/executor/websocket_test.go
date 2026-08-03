package executor

import (
	"fmt"
	"net/http"
	"testing"
)

func TestUpstreamWebsocketReplayRequiredError(t *testing.T) {
	err := NewUpstreamWebsocketReplayRequiredError()
	if !IsUpstreamWebsocketReplayRequired(err) {
		t.Fatal("replay error was not recognized")
	}
	if !IsUpstreamWebsocketReplayRequired(fmt.Errorf("wrapped: %w", err)) {
		t.Fatal("wrapped replay error was not recognized")
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusUpgradeRequired {
		t.Fatalf("replay error = %T %v, want status 426", err, err)
	}
	requestErr, ok := err.(RequestScopedError)
	if !ok || !requestErr.IsRequestScoped() {
		t.Fatalf("replay error = %T, want request scoped", err)
	}
}
