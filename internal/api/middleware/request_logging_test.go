package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestShouldSkipMethodForRequestLogging(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
		skip bool
	}{
		{
			name: "nil request",
			req:  nil,
			skip: true,
		},
		{
			name: "post request should not skip",
			req: &http.Request{
				Method: http.MethodPost,
				URL:    &url.URL{Path: "/v1/responses"},
			},
			skip: false,
		},
		{
			name: "plain get should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/models"},
				Header: http.Header{},
			},
			skip: true,
		},
		{
			name: "responses websocket upgrade should not skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{"Upgrade": []string{"websocket"}},
			},
			skip: false,
		},
		{
			name: "responses get without upgrade should skip",
			req: &http.Request{
				Method: http.MethodGet,
				URL:    &url.URL{Path: "/v1/responses"},
				Header: http.Header{},
			},
			skip: true,
		},
	}

	for i := range tests {
		got := shouldSkipMethodForRequestLogging(tests[i].req)
		if got != tests[i].skip {
			t.Fatalf("%s: got skip=%t, want %t", tests[i].name, got, tests[i].skip)
		}
	}
}

func TestShouldCaptureRequestBody(t *testing.T) {
	tests := []struct {
		name          string
		loggerEnabled bool
		req           *http.Request
		want          bool
	}{
		{
			name:          "logger enabled always captures",
			loggerEnabled: true,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "nil request",
			loggerEnabled: false,
			req:           nil,
			want:          false,
		},
		{
			name:          "small known size json in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("{}")),
				ContentLength: 2,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: true,
		},
		{
			name:          "large known size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: maxErrorOnlyCapturedRequestBodyBytes + 1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "unknown size skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: -1,
				Header:        http.Header{"Content-Type": []string{"application/json"}},
			},
			want: false,
		},
		{
			name:          "multipart skipped in error-only mode",
			loggerEnabled: false,
			req: &http.Request{
				Body:          io.NopCloser(strings.NewReader("x")),
				ContentLength: 1,
				Header:        http.Header{"Content-Type": []string{"multipart/form-data; boundary=abc"}},
			},
			want: false,
		},
	}

	for i := range tests {
		got := shouldCaptureRequestBody(tests[i].loggerEnabled, tests[i].req)
		if got != tests[i].want {
			t.Fatalf("%s: got %t, want %t", tests[i].name, got, tests[i].want)
		}
	}
}

func TestShouldLogRequest(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "management v0", path: "/v0/management/status", want: false},
		{name: "management root", path: "/management/health", want: false},
		{name: "api auth root", path: "/api/auth", want: false},
		{name: "api auth sub", path: "/api/auth/login", want: false},
		{name: "root auth", path: "/auth", want: false},
		{name: "root auth sub", path: "/auth/callback", want: false},
		{name: "explicit callback suffix", path: "/anthropic/callback", want: false},
		{name: "provider api allowed", path: "/api/provider/openai/v1/chat/completions", want: true},
		{name: "api non provider denied", path: "/api/other", want: false},
		{name: "public route", path: "/v1/models", want: true},
	}

	for i := range tests {
		got := shouldLogRequest(tests[i].path)
		if got != tests[i].want {
			t.Fatalf("%s: path=%s got %t want %t", tests[i].name, tests[i].path, got, tests[i].want)
		}
	}
}

func TestCaptureRequestInfoMasksSensitiveHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?key=secret-query", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer super-secret-token")
	req.Header.Set("X-Goog-Api-Key", "goog-secret")
	req.Header.Set("X-Api-Key", "anthropic-secret")
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	info, err := captureRequestInfo(c, true)
	if err != nil {
		t.Fatalf("captureRequestInfo error: %v", err)
	}
	if got := info.Headers["Authorization"][0]; got == "Bearer super-secret-token" {
		t.Fatalf("authorization header was not masked: %q", got)
	}
	if got := info.Headers["X-Goog-Api-Key"][0]; got == "goog-secret" {
		t.Fatalf("X-Goog-Api-Key was not masked: %q", got)
	}
	if got := info.Headers["X-Api-Key"][0]; got == "anthropic-secret" {
		t.Fatalf("X-Api-Key was not masked: %q", got)
	}
	if strings.Contains(info.URL, "secret-query") {
		t.Fatalf("query string was not masked in URL: %q", info.URL)
	}
}
