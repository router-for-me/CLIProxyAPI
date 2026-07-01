package executor

import (
	"net/http"
	"strings"
	"testing"
)

// The Antigravity (Gemini Code Assist) upstream returns an opaque
// "User location is not supported for the API use." 400 when the request's
// egress IP is in an unsupported region (common for datacenter/hosting IPs even
// when geolocated to a supported country). CLIProxyAPI should surface an
// actionable hint pointing at the per-auth proxy-url remedy.
func TestAntigravityRegionBlockedHint(t *testing.T) {
	body := []byte(`{"error":{"code":400,"message":"User location is not supported for the API use.","status":"FAILED_PRECONDITION"}}`)

	if h := antigravityRegionBlockedHint(http.StatusBadRequest, body); h == "" {
		t.Fatal("expected a hint for a region-blocked 400")
	}
	if h := antigravityRegionBlockedHint(http.StatusTooManyRequests, body); h != "" {
		t.Fatalf("expected no hint for non-400 status, got %q", h)
	}
	if h := antigravityRegionBlockedHint(http.StatusBadRequest, []byte(`{"error":{"message":"quota exceeded"}}`)); h != "" {
		t.Fatalf("expected no hint for an unrelated 400, got %q", h)
	}
}

func TestNewAntigravityStatusErr_AppendsRegionHint(t *testing.T) {
	body := []byte(`{"error":{"message":"User location is not supported for the API use.","status":"FAILED_PRECONDITION"}}`)
	err := newAntigravityStatusErr(http.StatusBadRequest, body)

	if !strings.Contains(err.msg, "User location is not supported") {
		t.Fatalf("original upstream message must be preserved, got: %s", err.msg)
	}
	if !strings.Contains(strings.ToLower(err.msg), "proxy-url") {
		t.Fatalf("expected an actionable proxy-url hint to be appended, got: %s", err.msg)
	}
	if err.code != http.StatusBadRequest {
		t.Fatalf("status code must be preserved, got %d", err.code)
	}
}

// A non-region 400 must pass through unchanged (no hint noise).
func TestNewAntigravityStatusErr_UnrelatedErrorUnchanged(t *testing.T) {
	body := []byte(`{"error":{"message":"invalid argument"}}`)
	err := newAntigravityStatusErr(http.StatusBadRequest, body)
	if err.msg != string(body) {
		t.Fatalf("unrelated 400 body must pass through verbatim, got: %s", err.msg)
	}
}
