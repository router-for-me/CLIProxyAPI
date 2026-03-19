package auth

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewHTTPResponseError_BoundsBodyReads(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header: http.Header{
			"Content-Type": []string{"text/plain; charset=utf-8"},
		},
		Body: io.NopCloser(strings.NewReader(strings.Repeat("x", int(httpResponseErrorBodyLimitBytes+1)))),
	}

	err := newHTTPResponseError(resp)
	if err == nil {
		t.Fatalf("newHTTPResponseError() returned nil")
	}
	if got := int64(len(err.Body)); got != httpResponseErrorBodyLimitBytes {
		t.Fatalf("body len = %d, want %d", got, httpResponseErrorBodyLimitBytes)
	}
	if got := err.StatusCode(); got != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", got, http.StatusBadGateway)
	}
	if got := err.Headers().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q, want %q", got, "text/plain; charset=utf-8")
	}
}
