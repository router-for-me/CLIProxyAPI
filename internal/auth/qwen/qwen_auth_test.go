package qwen

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPollForTokenUsesConfiguredHTTPClient(t *testing.T) {
	called := false
	qa := &QwenAuth{
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			called = true
			if req.URL.String() != QwenOAuthTokenEndpoint {
				t.Fatalf("unexpected request url: %s", req.URL.String())
			}
			if req.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", req.Method)
			}
			if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
				t.Fatalf("unexpected content type: %s", got)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("failed to read request body: %v", err)
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Fatalf("failed to parse request body: %v", err)
			}
			if values.Get("device_code") != "device-code" {
				t.Fatalf("unexpected device_code: %s", values.Get("device_code"))
			}
			if values.Get("code_verifier") != "code-verifier" {
				t.Fatalf("unexpected code_verifier: %s", values.Get("code_verifier"))
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"token","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}

	token, err := qa.PollForToken(context.Background(), "device-code", "code-verifier")
	if err != nil {
		t.Fatalf("PollForToken returned error: %v", err)
	}
	if !called {
		t.Fatal("expected configured HTTP client transport to be used")
	}
	if token == nil || token.AccessToken != "token" || token.RefreshToken != "refresh" {
		t.Fatalf("unexpected token response: %+v", token)
	}
}
