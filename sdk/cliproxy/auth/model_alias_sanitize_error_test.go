package auth

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type dummyStatusErr struct {
	msg   string
	code  int
	hdr   http.Header
	retry *time.Duration
}

func (e dummyStatusErr) Error() string              { return e.msg }
func (e dummyStatusErr) StatusCode() int            { return e.code }
func (e dummyStatusErr) Headers() http.Header       { return e.hdr }
func (e dummyStatusErr) RetryAfter() *time.Duration { return e.retry }

func TestSanitizeModelLeakError_RewritesUpstreamModel(t *testing.T) {
	err := dummyStatusErr{
		msg:  `{"error":{"message":"model qwen3-coder-flash not found","type":"invalid_request_error"}}`,
		code: 400,
	}
	sanitized := sanitizeModelLeakError(err, "gemini", "qwen3-coder-flash")

	if sanitized == nil {
		t.Fatalf("sanitizeModelLeakError returned nil")
	}
	if strings.Contains(sanitized.Error(), "qwen3-coder-flash") {
		t.Fatalf("expected upstream model to be removed, got: %s", sanitized.Error())
	}
	if !strings.Contains(sanitized.Error(), "gemini") {
		t.Fatalf("expected requested model to be present, got: %s", sanitized.Error())
	}
	if sc, ok := sanitized.(interface{ StatusCode() int }); !ok || sc.StatusCode() != 400 {
		t.Fatalf("expected StatusCode preserved, got: %v", sanitized)
	}
}

func TestSanitizeModelLeakError_RewritesBaseModelWhenSuffix(t *testing.T) {
	err := dummyStatusErr{
		msg:  `upstream says: model=qwen3-coder-flash is unavailable`,
		code: 503,
	}
	sanitized := sanitizeModelLeakError(err, "gemini(8192)", "qwen3-coder-flash(8192)")
	if sanitized == nil {
		t.Fatalf("sanitizeModelLeakError returned nil")
	}
	if strings.Contains(sanitized.Error(), "qwen3-coder-flash") {
		t.Fatalf("expected base upstream model to be removed, got: %s", sanitized.Error())
	}
	if !strings.Contains(sanitized.Error(), "gemini(8192)") {
		t.Fatalf("expected requested model to be present, got: %s", sanitized.Error())
	}
}

func TestSanitizeModelLeakError_PreservesRetryAfterAndHeaders(t *testing.T) {
	d := 2 * time.Second
	hdr := make(http.Header)
	hdr.Set("Retry-After", "2")
	err := dummyStatusErr{
		msg:   "qwen3-coder-flash rate limit",
		code:  429,
		hdr:   hdr,
		retry: &d,
	}
	sanitized := sanitizeModelLeakError(err, "gemini", "qwen3-coder-flash")

	if rap, ok := sanitized.(interface{ RetryAfter() *time.Duration }); !ok || rap.RetryAfter() == nil || rap.RetryAfter().Seconds() != 2 {
		t.Fatalf("expected RetryAfter preserved, got: %#v", sanitized)
	}
	if hp, ok := sanitized.(interface{ Headers() http.Header }); !ok || hp.Headers().Get("Retry-After") != "2" {
		t.Fatalf("expected Headers preserved, got: %#v", sanitized)
	}
}

func TestSanitizeModelLeakError_RedactsURLs(t *testing.T) {
	err := dummyStatusErr{
		msg:  `{"error":{"message":"policy violation: see https://help.aliyun.com/zh/model-studio/error-code#inappropriate-content","type":"invalid_request_error"}}`,
		code: 400,
	}
	sanitized := sanitizeModelLeakError(err, "gemini", "qwen3-coder-flash")
	if strings.Contains(sanitized.Error(), "https://") || strings.Contains(sanitized.Error(), "http://") {
		t.Fatalf("expected URLs to be redacted, got: %s", sanitized.Error())
	}
	if !strings.Contains(sanitized.Error(), "<redacted_url>") {
		t.Fatalf("expected redaction marker, got: %s", sanitized.Error())
	}
}
