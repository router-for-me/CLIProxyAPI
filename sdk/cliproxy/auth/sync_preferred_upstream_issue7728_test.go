package auth

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestSyncPreferredUpstreamErrorDoesNotAnnotateForeignBootstrap covers
// issue 7728: when the preferred upstream error already holds a
// *streamBootstrapError built from an earlier credential attempt, a later
// attempt's trusted Retry-After hint must not be grafted onto that earlier
// wrapper. The earlier bootstrap keeps its own error classification and
// headers, so inheriting a foreign credential's hint corrupts the reported
// retry timing.
func TestSyncPreferredUpstreamErrorDoesNotAnnotateForeignBootstrap(t *testing.T) {
	errA := &Error{Code: "upstream_unavailable", Message: "credential A failed", HTTPStatus: http.StatusServiceUnavailable}
	headersA := http.Header{"X-Credential": {"a"}}
	attemptA := markUpstreamExecutionAttempt(errA)

	var bootstrapA *streamBootstrapError
	wrappedA := newStreamBootstrapError(attemptA, headersA)
	if !errors.As(wrappedA, &bootstrapA) || bootstrapA == nil {
		t.Fatalf("newStreamBootstrapError result %T is not a *streamBootstrapError", wrappedA)
	}
	upstreamErr := error(markUpstreamExecutionAttemptWithProvider(bootstrapA, "test-provider"))

	errB := &Error{Code: "quota", Message: "credential B quota exceeded", HTTPStatus: http.StatusTooManyRequests}
	hintB := 30 * time.Minute
	attemptB := markUpstreamExecutionAttempt(withTrustedRetryAfter(errB, &hintB))

	syncPreferredUpstreamError(&upstreamErr, attemptB)

	// The earlier credential's bootstrap must remain preferred ...
	var current *streamBootstrapError
	if !errors.As(upstreamErr, &current) || current != bootstrapA {
		t.Fatalf("preferred upstream error no longer wraps credential A bootstrap")
	}
	// ... and must not have been mutated in place with credential B's hint.
	if trusted := trustedRetryAfterFromError(bootstrapA.cause); trusted != nil {
		t.Fatalf("credential A bootstrap.cause mutated with credential B hint: trusted=%v", *trusted)
	}
	if !errors.Is(bootstrapA.cause, errA) {
		t.Fatalf("credential A bootstrap.cause = %v, want original errA", bootstrapA.cause)
	}
}

// TestSyncPreferredUpstreamErrorRejectsEqualButDistinctCause strengthens the
// issue 7728 regression: two independent attempts may surface equal error
// values (same fields, different objects). A hint carried by the second
// attempt must still not annotate the first attempt's bootstrap.
func TestSyncPreferredUpstreamErrorRejectsEqualButDistinctCause(t *testing.T) {
	errA := &Error{Code: "quota", Message: "same message", HTTPStatus: http.StatusTooManyRequests}
	wrappedA := newStreamBootstrapError(markUpstreamExecutionAttempt(errA), http.Header{"X-Credential": {"a"}})
	var bootstrapA *streamBootstrapError
	if !errors.As(wrappedA, &bootstrapA) || bootstrapA == nil {
		t.Fatalf("newStreamBootstrapError result %T is not a *streamBootstrapError", wrappedA)
	}
	upstreamErr := error(markUpstreamExecutionAttemptWithProvider(bootstrapA, "test-provider"))

	// Credential B fails with an identical (but distinct) *Error value plus
	// a trusted hint.
	errB := &Error{Code: "quota", Message: "same message", HTTPStatus: http.StatusTooManyRequests}
	hintB := 45 * time.Minute
	attemptB := markUpstreamExecutionAttempt(withTrustedRetryAfter(errB, &hintB))

	syncPreferredUpstreamError(&upstreamErr, attemptB)

	if trusted := trustedRetryAfterFromError(bootstrapA.cause); trusted != nil {
		t.Fatalf("equal-but-distinct attempt hint mutated credential A bootstrap: trusted=%v", *trusted)
	}
}

// TestSyncPreferredUpstreamErrorRejectsSharedSentinelCause covers the
// residual case from review: two independent attempts may surface the very
// same sentinel error value. The earlier bootstrap still must not absorb
// the later attempt's hint because the attempts carry different ids.
func TestSyncPreferredUpstreamErrorRejectsSharedSentinelCause(t *testing.T) {
	sentinel := &Error{Code: "quota", Message: "shared sentinel", HTTPStatus: http.StatusTooManyRequests}

	wrappedA := newStreamBootstrapError(markUpstreamExecutionAttempt(sentinel), http.Header{"X-Credential": {"a"}})
	var bootstrapA *streamBootstrapError
	if !errors.As(wrappedA, &bootstrapA) || bootstrapA == nil {
		t.Fatalf("newStreamBootstrapError result %T is not a *streamBootstrapError", wrappedA)
	}
	upstreamErr := error(markUpstreamExecutionAttemptWithProvider(bootstrapA, "test-provider"))

	hintB := 45 * time.Minute
	attemptB := markUpstreamExecutionAttempt(withTrustedRetryAfter(sentinel, &hintB))
	syncPreferredUpstreamError(&upstreamErr, attemptB)

	if trusted := trustedRetryAfterFromError(bootstrapA.cause); trusted != nil {
		t.Fatalf("shared-sentinel attempt hint mutated credential A bootstrap: trusted=%v", *trusted)
	}
}

// TestSyncPreferredUpstreamErrorAnnotatesSameBootstrap covers the intended
// same-attempt annotation path: the preferred bootstrap carries no trusted
// hint yet, then the same attempt's failure resurfaces with a trusted hint
// (as produced by annotateTrustedRetryAfterByHint after the result was
// recorded), and the stored bootstrap must be annotated in place.
func TestSyncPreferredUpstreamErrorAnnotatesSameBootstrap(t *testing.T) {
	errA := &Error{Code: "quota", Message: "credential A quota", HTTPStatus: http.StatusTooManyRequests}
	hintA := 20 * time.Minute

	// First recording: bootstrap wraps the bare attempt error, no hint yet.
	markedA := markUpstreamExecutionAttempt(errA)
	wrappedA := newStreamBootstrapError(markedA, http.Header{"X-Credential": {"a"}})
	var bootstrapA *streamBootstrapError
	if !errors.As(wrappedA, &bootstrapA) || bootstrapA == nil {
		t.Fatalf("newStreamBootstrapError result %T is not a *streamBootstrapError", wrappedA)
	}
	upstreamErr := error(markUpstreamExecutionAttemptWithProvider(bootstrapA, "test-provider"))

	// The same attempt resurfaces through newStreamBootstrapError after its
	// hint was attached. annotateTrustedRetryAfterByHint rebuilds the
	// attempt marker while keeping the same attempt id, mirroring the
	// conductor stream bootstrap flow.
	hinted := withTrustedRetryAfter(errA, &hintA)
	annotated := newStreamBootstrapError(markUpstreamExecutionAttemptWithProviderAndID(hinted, "", bootstrapA.attemptID), nil)
	syncPreferredUpstreamError(&upstreamErr, annotated)

	var current *streamBootstrapError
	if !errors.As(upstreamErr, &current) || current != bootstrapA {
		t.Fatalf("preferred upstream error no longer wraps the same bootstrap")
	}
	trusted := trustedRetryAfterFromError(bootstrapA.cause)
	if trusted == nil {
		t.Fatalf("same-attempt hint was not applied to bootstrap.cause")
	}
	if *trusted > hintA || *trusted < hintA-time.Second {
		t.Fatalf("bootstrap.cause trusted hint = %v, want ~%v", *trusted, hintA)
	}
}
