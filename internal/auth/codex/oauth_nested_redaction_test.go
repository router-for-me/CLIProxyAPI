package codex

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestOAuthNestedTerminalErrorStopsRetriesWithoutLeaking(t *testing.T) {
	resetCodexRefreshGroupForTest()
	const sentinel = "SYNTHETIC_NESTED_OAUTH_SECRET_SENTINEL"
	var logs bytes.Buffer
	old := log.StandardLogger().Out
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	requests := 0
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"refresh_token_reused","message":"` + sentinel + `","type":"` + sentinel + `"}}`)), Request: req}, nil
	})}}
	_, err := auth.RefreshTokensWithRetry(context.Background(), "synthetic-nested-R0", 3)
	if err == nil {
		t.Fatal("expected OAuth failure")
	}
	if strings.Contains(err.Error(), sentinel) || strings.Contains(logs.String(), sentinel) {
		t.Fatal("nested OAuth response leaked into error or logs")
	}
	if !isNonRetryableRefreshErr(err) {
		t.Error("lost nested refresh_token_reused terminal classification")
	}
	if requests != 1 {
		t.Errorf("terminal error made %d HTTP requests, want 1 with 3 attempts configured", requests)
	}
}
