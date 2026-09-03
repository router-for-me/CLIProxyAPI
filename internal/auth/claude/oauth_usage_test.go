package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFetchOAuthUsageUsesControlPlaneRequestShape(t *testing.T) {
	var gotReq *http.Request
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotReq = req
		return jsonResponse(req, `{"limits":[{"kind":"session","percent":12}]}`), nil
	})}}

	body, errFetch := auth.FetchOAuthUsage(context.Background(), "access")
	if errFetch != nil {
		t.Fatalf("FetchOAuthUsage: %v", errFetch)
	}
	if !strings.Contains(string(body), `"session"`) {
		t.Fatalf("body = %s, want the raw usage payload", body)
	}
	if gotReq == nil || gotReq.Method != http.MethodGet || gotReq.URL.String() != UsageURL {
		t.Fatalf("request = %#v, want GET %s", gotReq, UsageURL)
	}
	for header, want := range map[string]string{
		"Authorization":  "Bearer access",
		"anthropic-beta": UsageBeta,
		"User-Agent":     "axios/1.15.2",
		"Accept":         "application/json, text/plain, */*",
	} {
		if got := gotReq.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestFetchOAuthUsageReportsUpstreamStatus(t *testing.T) {
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":"secret detail"}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}}

	_, errFetch := auth.FetchOAuthUsage(context.Background(), "expired")
	var statusErr *OAuthStatusError
	if !errors.As(errFetch, &statusErr) || statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %v, want an OAuthStatusError carrying 401", errFetch)
	}
	if strings.Contains(errFetch.Error(), "secret detail") {
		t.Fatalf("error text leaks the upstream body: %v", errFetch)
	}
}
