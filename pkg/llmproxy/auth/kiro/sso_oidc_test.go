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

func TestRefreshToken_IncludesGrantTypeAndExtensionHeaders(t *testing.T) {
	t.Parallel()

	client := &SSOOIDCClient{
		httpClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				bodyStr := string(body)
				for _, token := range []string{
					`"grantType":"refresh_token"`,
					`"grant_type":"refresh_token"`,
					`"refreshToken":"rt-1"`,
					`"refresh_token":"rt-1"`,
				} {
					if !strings.Contains(bodyStr, token) {
						t.Fatalf("expected payload to contain %s, got %s", token, bodyStr)
					}
				}

				for key, want := range map[string]string{
					"Content-Type":     "application/json",
					"x-amz-user-agent": idcAmzUserAgent,
					"User-Agent":       "node",
					"Connection":       "keep-alive",
					"Accept-Language":  "*",
					"sec-fetch-mode":   "cors",
				} {
					if got := req.Header.Get(key); got != want {
						t.Fatalf("header %s = %q, want %q", key, got, want)
					}
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"accessToken":"a","refreshToken":"b","expiresIn":3600}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	got, err := client.RefreshToken(context.Background(), "cid", "sec", "rt-1")
	if err != nil {
		t.Fatalf("RefreshToken returned error: %v", err)
	}
	if got == nil || got.AccessToken != "a" {
		t.Fatalf("unexpected token data: %#v", got)
	}
}

func TestRefreshTokenWithRegion_UsesRegionHostAndGrantType(t *testing.T) {
	t.Parallel()

	client := &SSOOIDCClient{
		httpClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				bodyStr := string(body)
				if !strings.Contains(bodyStr, `"grantType":"refresh_token"`) {
					t.Fatalf("expected grantType in payload, got %s", bodyStr)
				}
				if !strings.Contains(bodyStr, `"grant_type":"refresh_token"`) {
					t.Fatalf("expected grant_type in payload, got %s", bodyStr)
				}

				if got := req.Header.Get("Host"); got != "oidc.eu-west-1.amazonaws.com" {
					t.Fatalf("Host header = %q, want oidc.eu-west-1.amazonaws.com", got)
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"accessToken":"a2","refreshToken":"b2","expiresIn":1800}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	got, err := client.RefreshTokenWithRegion(context.Background(), "cid", "sec", "rt-2", "eu-west-1", "https://view.awsapps.com/start")
	if err != nil {
		t.Fatalf("RefreshTokenWithRegion returned error: %v", err)
	}
	if got == nil || got.AccessToken != "a2" {
		t.Fatalf("unexpected token data: %#v", got)
	}
}
