package qwen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type rewriteTransport struct {
	target string
	base   http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"
	newReq.URL.Host = strings.TrimPrefix(t.target, "http://")
	return t.base.RoundTrip(newReq)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: map[string][]string{
			"Content-Type": {"application/json"},
		},
		Body:   io.NopCloser(strings.NewReader(body)),
		Status: strconv.Itoa(status) + " " + http.StatusText(status),
	}
}

func TestInitiateDeviceFlow(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := DeviceFlow{
			DeviceCode:      "dev-code",
			UserCode:        "user-code",
			VerificationURI: "http://qwen.ai/verify",
			ExpiresIn:       600,
			Interval:        5,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &rewriteTransport{
			target: ts.URL,
			base:   http.DefaultTransport,
		},
	}

	auth := NewQwenAuth(nil, client)
	resp, err := auth.InitiateDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("InitiateDeviceFlow failed: %v", err)
	}

	if resp.DeviceCode != "dev-code" {
		t.Errorf("got device code %q, want dev-code", resp.DeviceCode)
	}
}

func TestRefreshTokens(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := QwenTokenResponse{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresIn:    3600,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := &http.Client{
		Transport: &rewriteTransport{
			target: ts.URL,
			base:   http.DefaultTransport,
		},
	}

	auth := NewQwenAuth(nil, client)
	resp, err := auth.RefreshTokens(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("RefreshTokens failed: %v", err)
	}

	if resp.AccessToken != "new-access" {
		t.Errorf("got access token %q, want new-access", resp.AccessToken)
	}
}

func TestPollForTokenUsesInjectedHTTPClient(t *testing.T) {
	defaultTransport := http.DefaultTransport
	defer func() {
		http.DefaultTransport = defaultTransport
	}()
	defaultCalled := 0
	http.DefaultTransport = roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		defaultCalled++
		return jsonResponse(http.StatusOK, `{"access_token":"default-access","token_type":"Bearer","expires_in":3600}`), nil
	})

	customCalled := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customCalled++
		_ = r
		w.Header().Set("Content-Type", "application/json")
		resp := QwenTokenResponse{
			AccessToken:  "custom-access",
			RefreshToken: "custom-refresh",
			ExpiresIn:    3600,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	auth := NewQwenAuth(nil, &http.Client{
		Transport: &rewriteTransport{
			target: ts.URL,
			base:   defaultTransport,
		},
	})
	resp, err := auth.PollForToken("device-code", "code-verifier")
	if err != nil {
		t.Fatalf("PollForToken failed: %v", err)
	}

	if customCalled != 1 {
		t.Fatalf("expected custom client to be used exactly once, got %d", customCalled)
	}
	if defaultCalled != 0 {
		t.Fatalf("did not expect default transport to be used, got %d", defaultCalled)
	}
	if resp.AccessToken != "custom-access" {
		t.Fatalf("got access token %q, want %q", resp.AccessToken, "custom-access")
	}
}

func TestQwenTokenStorageSaveTokenToFileRejectsTraversalPath(t *testing.T) {
	t.Parallel()

	ts := &QwenTokenStorage{AccessToken: "token"}
	err := ts.SaveTokenToFile("../qwen.json")
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
	if !strings.Contains(err.Error(), "auth file path is invalid") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}
