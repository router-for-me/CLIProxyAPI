package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOAuthExchangeFailureDoesNotExposeResponseBody(t *testing.T) {
	const sentinel = "SYNTHETIC_OAUTH_FAILURE_SECRET_SENTINEL"
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"` + sentinel + `","error_description":"` + sentinel + `"}`)), Request: req}, nil
	})}}
	_, err := auth.ExchangeCodeForTokens(context.Background(), "synthetic-code", &PKCECodes{CodeVerifier: "synthetic-verifier"})
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("exchange error must exist and omit response body")
	}
}

func TestOAuthTransportFailureDoesNotExposeUnderlyingError(t *testing.T) {
	resetCodexRefreshGroupForTest()
	const sentinel = "SYNTHETIC_OAUTH_TRANSPORT_SECRET_SENTINEL"
	auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) { return nil, errors.New(sentinel) })}}
	_, err := auth.RefreshTokens(context.Background(), "synthetic-transport-R0")
	if err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatal("underlying transport error leaked")
	}
}
