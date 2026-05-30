package auth

import (
	"net/http"
	"testing"
	"time"
)

func hdr(pairs map[string]string) http.Header {
	h := http.Header{}
	for k, v := range pairs {
		h.Set(k, v)
	}
	return h
}

func TestClaudeUsageParkUntil(t *testing.T) {
	now := time.Unix(1780084000, 0)
	fiveHReset := "1780084200" // now + 200s
	sevenDReset := "1780221600"

	// Below threshold -> no park.
	if got := claudeUsageParkUntil(hdr(map[string]string{
		hdrUnified5hStatus:      "allowed",
		hdrUnified5hUtilization: "0.25",
		hdrUnified5hReset:       fiveHReset,
	}), 0.85, now); !got.IsZero() {
		t.Fatalf("below threshold: expected zero, got %v", got)
	}

	// At/over threshold -> park until 5h reset.
	if got := claudeUsageParkUntil(hdr(map[string]string{
		hdrUnified5hStatus:      "allowed_warning",
		hdrUnified5hUtilization: "0.90",
		hdrUnified5hReset:       fiveHReset,
	}), 0.85, now); !got.Equal(time.Unix(1780084200, 0)) {
		t.Fatalf("over threshold: expected 5h reset, got %v", got)
	}

	// Hard rejected parks regardless of threshold (even threshold 0).
	if got := claudeUsageParkUntil(hdr(map[string]string{
		hdrUnified5hStatus:      "rejected",
		hdrUnified5hUtilization: "1.0",
		hdrUnified5hReset:       fiveHReset,
	}), 0, now); !got.Equal(time.Unix(1780084200, 0)) {
		t.Fatalf("rejected: expected park even with threshold 0, got %v", got)
	}

	// When 7d window is also breached, the later reset wins.
	if got := claudeUsageParkUntil(hdr(map[string]string{
		hdrUnified5hStatus:      "allowed_warning",
		hdrUnified5hUtilization: "0.90",
		hdrUnified5hReset:       fiveHReset,
		hdrUnified7dStatus:      "allowed_warning",
		hdrUnified7dUtilization: "0.88",
		hdrUnified7dReset:       sevenDReset,
	}), 0.85, now); !got.Equal(time.Unix(1780221600, 0)) {
		t.Fatalf("7d breach: expected latest (7d) reset, got %v", got)
	}

	// threshold 0 disables proactive avoidance when status is allowed.
	if got := claudeUsageParkUntil(hdr(map[string]string{
		hdrUnified5hStatus:      "allowed",
		hdrUnified5hUtilization: "0.99",
		hdrUnified5hReset:       fiveHReset,
	}), 0, now); !got.IsZero() {
		t.Fatalf("threshold 0 + allowed: expected zero, got %v", got)
	}
}

func TestRateLimitedUsageWindowGate(t *testing.T) {
	defer SetRateLimitDefaults(0, 0, 0, 0)
	SetRateLimitDefaults(0, 0, 0, 0) // RPM/TPM/concurrency/RPH all off
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: "a1", Provider: "claude"}
	m.auths["a1"] = a
	now := time.Unix(1780084000, 0)

	// No park -> not limited even with all numeric limits disabled.
	if _, limited := m.rateLimited("a1", now); limited {
		t.Fatalf("expected not limited before park")
	}
	// Park until reset -> limited until then, then free.
	a.rate.parkUntil(time.Unix(1780084200, 0))
	if until, limited := m.rateLimited("a1", now); !limited || !until.Equal(time.Unix(1780084200, 0)) {
		t.Fatalf("expected limited until 5h reset, got until=%v limited=%v", until, limited)
	}
	if _, limited := m.rateLimited("a1", time.Unix(1780084201, 0)); limited {
		t.Fatalf("expected free after the window reset")
	}
}
