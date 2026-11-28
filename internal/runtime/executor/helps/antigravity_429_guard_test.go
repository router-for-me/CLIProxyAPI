package helps

import (
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestFingerprintAntigravityRequestIgnoresVolatileEnvelopeFields(t *testing.T) {
	payloadA := []byte(`{
		"project": "proj-a",
		"requestId": "agent/1/100/t1/2",
		"request": {
			"sessionId": "-111",
			"systemInstruction": {"parts": [{"text": "sys"}]},
			"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
			"generationConfig": {"temperature": 0.2}
		}
	}`)
	payloadB := []byte(`{
		"project": "proj-b",
		"requestId": "agent/2/999/t2/5",
		"request": {
			"sessionId": "-999",
			"systemInstruction": {"parts": [{"text": "sys"}]},
			"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
			"generationConfig": {"temperature": 0.2}
		}
	}`)
	payloadDiff := []byte(`{
		"project": "proj-a",
		"requestId": "agent/1/100/t1/2",
		"request": {
			"systemInstruction": {"parts": [{"text": "different-sys"}]},
			"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
			"generationConfig": {"temperature": 0.2}
		}
	}`)

	keyA := FingerprintAntigravityRequest("auth-1", "gemini-2.5-pro", payloadA)
	keyB := FingerprintAntigravityRequest("auth-1", "gemini-2.5-pro", payloadB)
	if keyA != keyB {
		t.Fatalf("expected identical fingerprint for identical semantic content, got %q vs %q", keyA, keyB)
	}

	keyDiffContent := FingerprintAntigravityRequest("auth-1", "gemini-2.5-pro", payloadDiff)
	if keyA == keyDiffContent {
		t.Fatalf("expected different fingerprint when systemInstruction changes")
	}

	keyDiffAuth := FingerprintAntigravityRequest("auth-2", "gemini-2.5-pro", payloadA)
	if keyA == keyDiffAuth {
		t.Fatalf("expected different fingerprint when authID changes")
	}
}

func TestAntigravity429GuardBackoffAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	guard := NewAntigravity429Guard(func() time.Time { return now })
	key := "test-key"

	if _, _, blocked := guard.Check(key); blocked {
		t.Fatalf("expected unblocked initially")
	}

	// Strike 1: recorded without blocking yet (allows 1 retry)
	d1, s1 := guard.Remember(key, nil)
	if d1 != 0 || s1 != 1 {
		t.Fatalf("strike 1 = (%v, %d), want (0, 1)", d1, s1)
	}
	if _, _, blocked := guard.Check(key); blocked {
		t.Fatalf("expected unblocked after strike 1")
	}

	// Strike 2: 2m default block
	d2, s2 := guard.Remember(key, nil)
	if d2 != 2*time.Minute || s2 != 2 {
		t.Fatalf("strike 2 = (%v, %d), want (2m, 2)", d2, s2)
	}
	if rem, strikes, blocked := guard.Check(key); !blocked || rem != 2*time.Minute || strikes != 2 {
		t.Fatalf("Check after strike 2 = (%v, %d, %v), want (2m, 2, true)", rem, strikes, blocked)
	}

	// Advance 2m -> expired
	now = now.Add(2 * time.Minute)
	if _, _, blocked := guard.Check(key); blocked {
		t.Fatalf("expected block to expire at 2m")
	}

	// Strike 3 within strike window: 4m block
	d3, s3 := guard.Remember(key, nil)
	if d3 != 4*time.Minute || s3 != 3 {
		t.Fatalf("strike 3 = (%v, %d), want (4m, 3)", d3, s3)
	}

	// Explicit retryAfter overrides default and caps at 30m
	longRetry := 90 * time.Minute
	d4, s4 := guard.Remember(key, &longRetry)
	if d4 != 30*time.Minute || s4 != 4 {
		t.Fatalf("strike 4 with 90m retryAfter = (%v, %d), want (30m, 4)", d4, s4)
	}
}

func TestBuildAntigravityRefusalReplayError(t *testing.T) {
	err := BuildAntigravityRefusalReplayError(1500*time.Millisecond, 2)
	var statusErr cliproxyexecutor.StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("expected 429 StatusError, got %v", err)
	}
	var reqScoped cliproxyexecutor.RequestScopedError
	if !errors.As(err, &reqScoped) || !reqScoped.IsRequestScoped() {
		t.Fatalf("expected RequestScopedError=true, got %v", err)
	}
}
