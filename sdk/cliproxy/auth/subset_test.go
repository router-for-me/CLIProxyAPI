package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func resetSubsetRouting(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetSubsetRouting(false, "", false, "") })
}

func newSubsetTestManager(t *testing.T, ids ...string) (*Manager, map[string]string) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	indexes := make(map[string]string, len(ids))
	for _, id := range ids {
		a := &Auth{
			ID:       id,
			Provider: "gemini",
			Attributes: map[string]string{
				"api_key": "key-" + id,
			},
		}
		idx := a.EnsureIndex()
		if idx == "" {
			t.Fatalf("EnsureIndex returned empty index for %s", id)
		}
		m.auths[id] = a
		indexes[id] = idx
	}
	return m, indexes
}

func subsetOptsWithHeader(values ...string) cliproxyexecutor.Options {
	headers := http.Header{}
	for _, value := range values {
		headers.Add(SubsetHeaderName, value)
	}
	return cliproxyexecutor.Options{Headers: headers}
}

func TestParseSubsetHeaderTrimsDedupesAndIgnoresInvalid(t *testing.T) {
	got := parseSubsetHeader(" abc123 ,ABC123,, def_456 ,bad entry,!!,def_456,x-y.z")
	want := []string{"abc123", "def_456", "x-y.z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSubsetHeader = %v, want %v", got, want)
	}
}

func TestApplySubsetRoutingDisabledKeepsFullPool(t *testing.T) {
	resetSubsetRouting(t)
	SetSubsetRouting(false, SubsetEmptyPolicyReject, false, "")
	m, indexes := newSubsetTestManager(t, "auth-1")

	ctx := context.Background()
	outCtx, err := m.applySubsetRouting(ctx, subsetOptsWithHeader(indexes["auth-1"]))
	if err != nil {
		t.Fatalf("applySubsetRouting error = %v, want nil", err)
	}
	if outCtx != ctx {
		t.Fatalf("applySubsetRouting changed the context while disabled")
	}
	if ids := subsetAllowedAuthIDsFromContext(outCtx); ids != nil {
		t.Fatalf("subset ids = %v, want nil while disabled", ids)
	}
}

func TestApplySubsetRoutingNoHeaderKeepsFullPool(t *testing.T) {
	resetSubsetRouting(t)
	SetSubsetRouting(true, SubsetEmptyPolicyReject, false, "")
	m, _ := newSubsetTestManager(t, "auth-1")

	ctx := context.Background()
	outCtx, err := m.applySubsetRouting(ctx, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("applySubsetRouting error = %v, want nil", err)
	}
	if outCtx != ctx {
		t.Fatalf("applySubsetRouting changed the context without a header")
	}

	// A header holding only malformed entries counts as empty as well.
	outCtx, err = m.applySubsetRouting(ctx, subsetOptsWithHeader(" , !! , bad entry "))
	if err != nil {
		t.Fatalf("applySubsetRouting error = %v, want nil for malformed-only header", err)
	}
	if outCtx != ctx {
		t.Fatalf("applySubsetRouting changed the context for a malformed-only header")
	}
}

func TestApplySubsetRoutingFiltersEligibilityToSubset(t *testing.T) {
	resetSubsetRouting(t)
	SetSubsetRouting(true, SubsetEmptyPolicyFallback, false, "")
	m, indexes := newSubsetTestManager(t, "auth-1", "auth-2", "auth-3")

	opts := subsetOptsWithHeader(indexes["auth-1"] + ", " + indexes["auth-2"])
	outCtx, err := m.applySubsetRouting(context.Background(), opts)
	if err != nil {
		t.Fatalf("applySubsetRouting error = %v, want nil", err)
	}
	ids := subsetAllowedAuthIDsFromContext(outCtx)
	want := map[string]struct{}{"auth-1": {}, "auth-2": {}}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("subset ids = %v, want %v", ids, want)
	}

	eligibility := authSelectionEligibilityForRequest(outCtx, opts)
	if !eligibility.allows(m.auths["auth-1"]) {
		t.Fatalf("eligibility rejected auth-1, want allowed")
	}
	if !eligibility.allows(m.auths["auth-2"]) {
		t.Fatalf("eligibility rejected auth-2, want allowed")
	}
	if eligibility.allows(m.auths["auth-3"]) {
		t.Fatalf("eligibility allowed auth-3 outside the subset")
	}
}

func TestApplySubsetRoutingIgnoresUnknownEntries(t *testing.T) {
	resetSubsetRouting(t)
	SetSubsetRouting(true, SubsetEmptyPolicyReject, false, "")
	m, indexes := newSubsetTestManager(t, "auth-1", "auth-2")

	// Repeated header lines behave like one comma separated list.
	opts := subsetOptsWithHeader(indexes["auth-1"], "ffffffffffffffff")
	outCtx, err := m.applySubsetRouting(context.Background(), opts)
	if err != nil {
		t.Fatalf("applySubsetRouting error = %v, want nil", err)
	}
	ids := subsetAllowedAuthIDsFromContext(outCtx)
	want := map[string]struct{}{"auth-1": {}}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("subset ids = %v, want %v", ids, want)
	}
}

func TestApplySubsetRoutingAllUnknownFallbackUsesFullPool(t *testing.T) {
	resetSubsetRouting(t)
	SetSubsetRouting(true, SubsetEmptyPolicyFallback, false, "")
	m, _ := newSubsetTestManager(t, "auth-1")

	ctx := context.Background()
	outCtx, err := m.applySubsetRouting(ctx, subsetOptsWithHeader("ffffffffffffffff"))
	if err != nil {
		t.Fatalf("applySubsetRouting error = %v, want nil under fallback policy", err)
	}
	if outCtx != ctx {
		t.Fatalf("applySubsetRouting changed the context under fallback policy")
	}
	if ids := subsetAllowedAuthIDsFromContext(outCtx); ids != nil {
		t.Fatalf("subset ids = %v, want nil under fallback policy", ids)
	}
}

func TestApplySubsetRoutingAllUnknownRejectReturns429(t *testing.T) {
	resetSubsetRouting(t)
	SetSubsetRouting(true, SubsetEmptyPolicyReject, false, "")
	m, _ := newSubsetTestManager(t, "auth-1")

	_, err := m.applySubsetRouting(context.Background(), subsetOptsWithHeader("ffffffffffffffff"))
	if err == nil {
		t.Fatalf("applySubsetRouting error = nil, want reject error")
	}
	var authErr *Error
	if !errors.As(err, &authErr) {
		t.Fatalf("applySubsetRouting error type = %T, want *Error", err)
	}
	if authErr.Code != "auth_subset_unavailable" {
		t.Fatalf("error code = %q, want auth_subset_unavailable", authErr.Code)
	}
	if authErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("error status = %d, want %d", authErr.HTTPStatus, http.StatusTooManyRequests)
	}
}

func TestSetSubsetRoutingNormalizesEmptyPolicy(t *testing.T) {
	resetSubsetRouting(t)
	SetSubsetRouting(true, "no-such-policy", false, "")
	if policy := subsetRoutingSnapshot().emptyPolicy; policy != SubsetEmptyPolicyFallback {
		t.Fatalf("empty policy = %q, want %q", policy, SubsetEmptyPolicyFallback)
	}
	SetSubsetRouting(true, " REJECT ", false, "")
	if policy := subsetRoutingSnapshot().emptyPolicy; policy != SubsetEmptyPolicyReject {
		t.Fatalf("empty policy = %q, want %q", policy, SubsetEmptyPolicyReject)
	}
}
