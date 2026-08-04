package auth

import (
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestRequestTerminatedErrorSkipsCreditsFallback(t *testing.T) {
	errTerminated := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusTooManyRequests}
	if !isRequestTerminatedError(errTerminated) {
		t.Fatal("isRequestTerminatedError() = false")
	}
	if shouldAttemptAntigravityCreditsFallback(&Manager{}, errTerminated, []string{"antigravity"}) {
		t.Fatal("terminated request must not use Antigravity credits fallback")
	}
}
