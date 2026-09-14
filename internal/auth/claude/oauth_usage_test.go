package claude

import (
	"bytes"
	"compress/gzip"
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

// A non-2xx reply keeps its status even when its body can't be read: over
// the cap, or in an encoding the decoder doesn't know.
func TestFetchOAuthUsageKeepsTheStatusWhenTheErrorBodyIsUnreadable(t *testing.T) {
	cases := map[string]*http.Response{
		"oversize": {
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, (2<<20)+1))),
			Header:     make(http.Header),
		},
		"unknown encoding": {
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("not really zstd")),
			Header:     http.Header{"Content-Encoding": []string{"zstd"}},
		},
	}
	for name, resp := range cases {
		auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp.Request = req
			return resp, nil
		})}}
		_, errFetch := auth.FetchOAuthUsage(context.Background(), "access")
		var statusErr *OAuthStatusError
		if !errors.As(errFetch, &statusErr) || statusErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("%s: error = %v, want an OAuthStatusError carrying 429", name, errFetch)
		}
	}
}

func TestFetchOAuthUsageBoundsTheBodyBeforeAndAfterDecompression(t *testing.T) {
	// 3 MiB of zeros gzips to a few KB: small on the wire, over the cap decoded.
	var bomb bytes.Buffer
	gz := gzip.NewWriter(&bomb)
	if _, errWrite := gz.Write(make([]byte, 3<<20)); errWrite != nil {
		t.Fatalf("gzip write: %v", errWrite)
	}
	if errClose := gz.Close(); errClose != nil {
		t.Fatalf("gzip close: %v", errClose)
	}
	cases := map[string]*http.Response{
		"plain oversize": {
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(make([]byte, (2<<20)+1))),
			Header:     make(http.Header),
		},
		"gzip bomb": {
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(bomb.Bytes())),
			Header:     http.Header{"Content-Encoding": []string{"gzip"}},
		},
	}
	for name, resp := range cases {
		auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp.Request = req
			return resp, nil
		})}}
		_, errFetch := auth.FetchOAuthUsage(context.Background(), "access")
		if errFetch == nil || !strings.Contains(errFetch.Error(), "exceeds") {
			t.Fatalf("%s: error = %v, want a size-bound error", name, errFetch)
		}
	}
}
