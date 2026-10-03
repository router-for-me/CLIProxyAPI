package codex

import (
	"bytes"
	"context"
	log "github.com/sirupsen/logrus"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOAuthFailureDoesNotExposeResponseBody(t *testing.T) {
	resetCodexRefreshGroupForTest()
	var logs bytes.Buffer
	old := log.StandardLogger().Out
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	const sentinel = "SYNTHETIC_OAUTH_FAILURE_SECRET_SENTINEL"
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_reused","error_description":"` + sentinel + `"}`)), Request: req}, nil
	})}}
	_, err := auth.RefreshTokensWithRetry(context.Background(), "synthetic-redaction-R0", 1)
	if err == nil {
		t.Fatal("expected OAuth error")
	}
	if strings.Contains(err.Error(), sentinel) || strings.Contains(logs.String(), sentinel) {
		t.Fatal("OAuth error body leaked into returned error or logs")
	}
	if !isNonRetryableRefreshErr(err) {
		t.Fatal("lost refresh_token_reused classification")
	}
}
