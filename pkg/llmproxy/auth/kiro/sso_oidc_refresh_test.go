package kiro

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testClientWithResponse(t *testing.T, status int, body string) *SSOOIDCClient {
	t.Helper()
	return &SSOOIDCClient{
		httpClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: status,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}, nil
			}),
		},
	}
}

func TestRefreshToken_PreservesOriginalRefreshTokenWhenMissing(t *testing.T) {
	c := testClientWithResponse(t, http.StatusOK, `{"accessToken":"new-access","expiresIn":3600}`)

	got, err := c.RefreshToken(context.Background(), "cid", "secret", "original-refresh")
	if err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if got.AccessToken != "new-access" {
		t.Fatalf("AccessToken = %q, want %q", got.AccessToken, "new-access")
	}
	if got.RefreshToken != "original-refresh" {
		t.Fatalf("RefreshToken = %q, want original refresh token fallback", got.RefreshToken)
	}
}

func TestRefreshTokenWithRegion_PreservesOriginalRefreshTokenWhenMissing(t *testing.T) {
	c := testClientWithResponse(t, http.StatusOK, `{"accessToken":"new-access","expiresIn":3600}`)

	got, err := c.RefreshTokenWithRegion(context.Background(), "cid", "secret", "original-refresh", "us-east-1", "https://example.start")
	if err != nil {
		t.Fatalf("RefreshTokenWithRegion error: %v", err)
	}
	if got.AccessToken != "new-access" {
		t.Fatalf("AccessToken = %q, want %q", got.AccessToken, "new-access")
	}
	if got.RefreshToken != "original-refresh" {
		t.Fatalf("RefreshToken = %q, want original refresh token fallback", got.RefreshToken)
	}
}

func TestRefreshToken_ReturnsHelpfulErrorWithResponseBody(t *testing.T) {
	c := testClientWithResponse(t, http.StatusUnauthorized, `{"error":"invalid_grant"}`)

	_, err := c.RefreshToken(context.Background(), "cid", "secret", "refresh")
	if err == nil {
		t.Fatalf("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "status 401") || !strings.Contains(msg, "invalid_grant") {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestRefreshTokenWithRegion_FailsOnMissingAccessToken(t *testing.T) {
	c := testClientWithResponse(t, http.StatusOK, `{"refreshToken":"new-refresh","expiresIn":3600}`)

	_, err := c.RefreshTokenWithRegion(context.Background(), "cid", "secret", "refresh", "us-east-1", "https://example.start")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "missing access token") {
		t.Fatalf("unexpected error: %v", err)
	}
}
