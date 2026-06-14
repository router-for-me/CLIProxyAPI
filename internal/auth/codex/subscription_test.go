package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func jsonResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}

func TestParseSubscriptionTime(t *testing.T) {
	rfc := "2030-01-02T03:04:05Z"
	wantRFC, _ := time.Parse(time.RFC3339, rfc)

	cases := []struct {
		name  string
		value any
		ok    bool
		want  time.Time
	}{
		{name: "rfc3339 string", value: rfc, ok: true, want: wantRFC.UTC()},
		{name: "unix seconds", value: "1893553445", ok: true, want: time.Unix(1893553445, 0).UTC()},
		{name: "unix millis normalized to seconds", value: "1893553445000", ok: true, want: time.Unix(1893553445, 0).UTC()},
		{name: "numeric float seconds", value: float64(1893553445), ok: true, want: time.Unix(1893553445, 0).UTC()},
		{name: "empty", value: "", ok: false},
		{name: "garbage", value: "not-a-time", ok: false},
		{name: "nil", value: nil, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseSubscriptionTime(tc.value)
			if ok != tc.ok {
				t.Fatalf("ok=%v, want %v", ok, tc.ok)
			}
			if tc.ok && !got.Equal(tc.want) {
				t.Fatalf("time=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeSubscriptionScalar(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "trimmed string", value: "  hello  ", want: "hello"},
		{name: "json number", value: json.Number("42"), want: "42"},
		{name: "integral float", value: float64(7), want: "7"},
		{name: "fractional float", value: float64(7.5), want: "7.5"},
		{name: "int", value: 9, want: "9"},
		{name: "int64", value: int64(11), want: "11"},
		{name: "bool", value: true, want: "true"},
		{name: "nil", value: nil, want: ""},
		{name: "unsupported", value: []string{"x"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSubscriptionScalar(tc.value); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFirstJSONScalarPrefersEarlierKeys(t *testing.T) {
	obj := map[string]any{"id": "second", "account_id": "first"}
	if got := firstJSONScalar(obj, "account_id", "id"); got != "first" {
		t.Fatalf("got %q, want first", got)
	}
	if got := firstJSONScalar(obj, "missing", "id"); got != "second" {
		t.Fatalf("got %q, want second", got)
	}
	if got := firstJSONScalar(nil, "id"); got != "" {
		t.Fatalf("nil obj: got %q, want empty", got)
	}
}

func TestSubscriptionMissingOrExpired(t *testing.T) {
	future := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)

	if subscriptionMissingOrExpired(future) {
		t.Fatalf("future expiry should not be expired")
	}
	if !subscriptionMissingOrExpired(past) {
		t.Fatalf("past expiry should be expired")
	}
	if !subscriptionMissingOrExpired("") {
		t.Fatalf("missing expiry should be treated as expired")
	}
}

func TestUpdateSubscriptionExpiredMetadata(t *testing.T) {
	future := time.Now().UTC().Add(72 * time.Hour).Format(time.RFC3339)
	past := time.Now().UTC().Add(-72 * time.Hour).Format(time.RFC3339)

	t.Run("active sets expired false", func(t *testing.T) {
		meta := map[string]any{}
		if !updateSubscriptionExpiredMetadata(meta, future) {
			t.Fatalf("expected changed=true on first write")
		}
		if v, _ := meta["subscription_expired"].(bool); v {
			t.Fatalf("subscription_expired=%v, want false", meta["subscription_expired"])
		}
		// Idempotent: second write with same value reports no change.
		if updateSubscriptionExpiredMetadata(meta, future) {
			t.Fatalf("expected changed=false when value unchanged")
		}
	})

	t.Run("past sets expired true", func(t *testing.T) {
		meta := map[string]any{}
		if !updateSubscriptionExpiredMetadata(meta, past) {
			t.Fatalf("expected changed=true")
		}
		if v, _ := meta["subscription_expired"].(bool); !v {
			t.Fatalf("subscription_expired=%v, want true", meta["subscription_expired"])
		}
	})

	t.Run("unparseable leaves metadata untouched", func(t *testing.T) {
		meta := map[string]any{}
		if updateSubscriptionExpiredMetadata(meta, "nope") {
			t.Fatalf("expected changed=false for unparseable expiry")
		}
		if _, ok := meta["subscription_expired"]; ok {
			t.Fatalf("subscription_expired should not be set for unparseable expiry")
		}
	})
}

func TestSetStringAndBoolMetadata(t *testing.T) {
	meta := map[string]any{}
	if !setStringMetadata(meta, "k", "  v  ") {
		t.Fatalf("expected changed=true on first set")
	}
	if got := stringMetadata(meta, "k"); got != "v" {
		t.Fatalf("stringMetadata=%q, want v", got)
	}
	if setStringMetadata(meta, "k", "v") {
		t.Fatalf("expected changed=false on identical set")
	}
	if setStringMetadata(meta, "blank", "   ") {
		t.Fatalf("expected changed=false for blank value")
	}
	if _, ok := meta["blank"]; ok {
		t.Fatalf("blank key should not be written")
	}

	if !setBoolMetadata(meta, "flag", true) {
		t.Fatalf("expected changed=true on first bool set")
	}
	if setBoolMetadata(meta, "flag", true) {
		t.Fatalf("expected changed=false on identical bool set")
	}
	if !setBoolMetadata(meta, "flag", false) {
		t.Fatalf("expected changed=true when bool flips")
	}
}

func TestFirstNonEmptyStringAndIsDigits(t *testing.T) {
	if got := firstNonEmptyString("", "  ", "x", "y"); got != "x" {
		t.Fatalf("got %q, want x", got)
	}
	if got := firstNonEmptyString("", "   "); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if !isDigits("12345") {
		t.Fatalf("12345 should be digits")
	}
	if isDigits("12a45") || isDigits("") {
		t.Fatalf("non-digit / empty should be false")
	}
}

func TestParseAccountsCheckSnapshot(t *testing.T) {
	t.Run("selects preferred account and reads entitlement", func(t *testing.T) {
		payload := map[string]any{
			"accounts": []any{
				map[string]any{
					"account":     map[string]any{"account_id": "acc-1", "plan_type": "plus"},
					"entitlement": map[string]any{"subscription_plan": "pro", "expires_at": "2030-01-01T00:00:00Z"},
				},
				map[string]any{
					"account":     map[string]any{"account_id": "acc-2"},
					"entitlement": map[string]any{"subscription_plan": "team", "expires_at": "2031-01-01T00:00:00Z"},
				},
			},
		}
		snap := parseAccountsCheckSnapshot(payload, "acc-2")
		if snap == nil {
			t.Fatalf("expected snapshot")
		}
		if snap.AccountID != "acc-2" {
			t.Fatalf("AccountID=%q, want acc-2", snap.AccountID)
		}
		if snap.PlanType != "team" {
			t.Fatalf("PlanType=%q, want team", snap.PlanType)
		}
		if snap.ActiveUntil != "2031-01-01T00:00:00Z" {
			t.Fatalf("ActiveUntil=%q, want 2031-01-01T00:00:00Z", snap.ActiveUntil)
		}
	})

	t.Run("defaults to first record when preferred missing", func(t *testing.T) {
		payload := map[string]any{
			"accounts": []any{
				map[string]any{"account_id": "only", "plan_type": "plus", "expires_at": "2030-06-01T00:00:00Z"},
			},
		}
		snap := parseAccountsCheckSnapshot(payload, "does-not-exist")
		if snap == nil || snap.AccountID != "only" || snap.PlanType != "plus" {
			t.Fatalf("unexpected snapshot: %#v", snap)
		}
	})

	t.Run("no records returns nil", func(t *testing.T) {
		if snap := parseAccountsCheckSnapshot(map[string]any{}, ""); snap != nil {
			t.Fatalf("expected nil, got %#v", snap)
		}
	})

	t.Run("selects object-keyed account by its map key", func(t *testing.T) {
		// accounts is keyed by account id; the values do not repeat account_id,
		// so selection must fall back to the map key.
		payload := map[string]any{
			"accounts": map[string]any{
				"acc-1": map[string]any{"entitlement": map[string]any{"subscription_plan": "free", "expires_at": "2030-01-01T00:00:00Z"}},
				"acc-2": map[string]any{"entitlement": map[string]any{"subscription_plan": "pro", "expires_at": "2031-01-01T00:00:00Z"}},
			},
		}
		snap := parseAccountsCheckSnapshot(payload, "acc-2")
		if snap == nil {
			t.Fatalf("expected snapshot")
		}
		if snap.PlanType != "pro" {
			t.Fatalf("PlanType=%q, want pro (selected wrong keyed account)", snap.PlanType)
		}
		if snap.AccountID != "acc-2" {
			t.Fatalf("AccountID=%q, want acc-2 (should fall back to map key)", snap.AccountID)
		}
	})
}

func TestFetchSubscriptionStatus_AccountsCheckPrimary(t *testing.T) {
	future := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/accounts/check/") {
			body := `{"accounts":[{"account":{"account_id":"acc-1"},"entitlement":{"subscription_plan":"pro","expires_at":"` + future + `"}}]}`
			return jsonResponse(req, http.StatusOK, body), nil
		}
		t.Fatalf("unexpected request to %s", req.URL)
		return nil, nil
	})}

	snap, err := FetchSubscriptionStatus(context.Background(), "token", "acc-1", client)
	if err != nil {
		t.Fatalf("FetchSubscriptionStatus error: %v", err)
	}
	if snap.AccountID != "acc-1" || snap.PlanType != "pro" || snap.ActiveUntil != future {
		t.Fatalf("snapshot=%#v", snap)
	}
}

func TestFetchSubscriptionStatus_FallsBackToSubscriptions(t *testing.T) {
	past := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	future := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	var hitSubscriptions bool

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(req.URL.Path, "/accounts/check/"):
			// Expired entitlement triggers the subscriptions fallback.
			body := `{"accounts":[{"account":{"account_id":"acc-9"},"entitlement":{"subscription_plan":"free","expires_at":"` + past + `"}}]}`
			return jsonResponse(req, http.StatusOK, body), nil
		case strings.Contains(req.URL.Path, "/subscriptions"):
			hitSubscriptions = true
			body := `{"subscription_plan":"pro","active_until":"` + future + `"}`
			return jsonResponse(req, http.StatusOK, body), nil
		default:
			t.Fatalf("unexpected request to %s", req.URL)
			return nil, nil
		}
	})}

	snap, err := FetchSubscriptionStatus(context.Background(), "token", "acc-9", client)
	if err != nil {
		t.Fatalf("FetchSubscriptionStatus error: %v", err)
	}
	if !hitSubscriptions {
		t.Fatalf("expected subscriptions fallback to be called")
	}
	if snap.PlanType != "pro" || snap.ActiveUntil != future {
		t.Fatalf("snapshot=%#v, want plan pro / active %s", snap, future)
	}
}

func TestEnrichSubscriptionMetadataForTokens_UsesExistingExpiryWithoutBackend(t *testing.T) {
	future := time.Now().UTC().Add(20 * 24 * time.Hour).Format(time.RFC3339)
	meta := map[string]any{"subscription_active_until": future}
	// A client that fails the test if any request is made: a still-valid expiry
	// must short-circuit before any backend call.
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected backend call to %s", req.URL)
		return nil, nil
	})}

	changed, err := EnrichSubscriptionMetadataForTokens(context.Background(), meta, "", "", "", client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true (subscription_expired written)")
	}
	if v, _ := meta["subscription_expired"].(bool); v {
		t.Fatalf("subscription_expired=%v, want false for active subscription", meta["subscription_expired"])
	}
}

func TestEnrichSubscriptionMetadataForTokens_BackendFallbackPopulatesMetadata(t *testing.T) {
	future := time.Now().UTC().Add(20 * 24 * time.Hour).Format(time.RFC3339)
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/accounts/check/") {
			body := `{"accounts":[{"account":{"account_id":"acc-7"},"entitlement":{"subscription_plan":"pro","expires_at":"` + future + `"}}]}`
			return jsonResponse(req, http.StatusOK, body), nil
		}
		t.Fatalf("unexpected request to %s", req.URL)
		return nil, nil
	})}

	meta := map[string]any{}
	changed, err := EnrichSubscriptionMetadataForTokens(context.Background(), meta, "", "access-token", "acc-7", client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if got := stringMetadata(meta, "plan_type"); got != "pro" {
		t.Fatalf("plan_type=%q, want pro", got)
	}
	if got := stringMetadata(meta, "subscription_active_until"); got != future {
		t.Fatalf("subscription_active_until=%q, want %s", got, future)
	}
	if v, _ := meta["subscription_expired"].(bool); v {
		t.Fatalf("subscription_expired=%v, want false", meta["subscription_expired"])
	}
}

func TestEnrichSubscriptionMetadataForTokens_NoTokensNoChange(t *testing.T) {
	meta := map[string]any{}
	changed, err := EnrichSubscriptionMetadataForTokens(context.Background(), meta, "", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Fatalf("expected changed=false when no tokens and no expiry present")
	}
	if len(meta) != 0 {
		t.Fatalf("metadata should remain empty, got %#v", meta)
	}
}

func TestEnrichSubscriptionMetadataWrappers(t *testing.T) {
	future := time.Now().UTC().Add(15 * 24 * time.Hour).Format(time.RFC3339)

	t.Run("package-level wrapper", func(t *testing.T) {
		meta := map[string]any{"subscription_active_until": future}
		changed, err := EnrichSubscriptionMetadata(context.Background(), meta, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Fatalf("expected changed=true (subscription_expired written)")
		}
	})

	t.Run("nil metadata is a no-op", func(t *testing.T) {
		changed, err := EnrichSubscriptionMetadata(context.Background(), nil, nil)
		if err != nil || changed {
			t.Fatalf("nil metadata: changed=%v err=%v, want false/nil", changed, err)
		}
	})

	t.Run("CodexAuth method wrapper", func(t *testing.T) {
		meta := map[string]any{"subscription_active_until": future}
		auth := &CodexAuth{}
		changed, err := auth.EnrichSubscriptionMetadata(context.Background(), meta, "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Fatalf("expected changed=true")
		}
	})
}
