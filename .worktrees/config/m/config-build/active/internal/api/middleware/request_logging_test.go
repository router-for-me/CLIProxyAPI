package middleware

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
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
