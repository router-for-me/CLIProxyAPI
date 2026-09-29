package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommandCodeTier_Normalize(t *testing.T) {
	cases := []struct {
		planID   string
		expected CommandCodeTier
	}{
		{"individual-go", CommandCodeTierGo},
		{"go", CommandCodeTierGo},
		{"individual-goat", CommandCodeTierGoat},
		{"goat", CommandCodeTierGoat},
		{"individual-pro", CommandCodeTierGoat},
		{"team-pro", CommandCodeTierGoat},
		{"max10x", CommandCodeTierGoat},
		{"provider", CommandCodeTierGoat},
	}

	for _, c := range cases {
		got := NormalizeCommandCodePlan(c.planID)
		if got != c.expected {
			t.Errorf("NormalizeCommandCodePlan(%q) = %q, want %q", c.planID, got, c.expected)
		}
	}
}

func TestCommandCodeTier_FetchAndCache(t *testing.T) {
	ClearCommandCodeTierCache()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"data":{"planId":"individual-goat","status":"active"}}`)
	}))
	defer ts.Close()

	oldURL := SubscriptionsAPIURL
	SubscriptionsAPIURL = ts.URL
	defer func() { SubscriptionsAPIURL = oldURL }()

	tier, err := FetchCommandCodeTierHTTP(context.Background(), "test-key", ts.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CommandCodeTierGoat {
		t.Fatalf("got tier %q, want %q", tier, CommandCodeTierGoat)
	}

	cached := GetCommandCodeTier("test-key")
	if cached != CommandCodeTierGoat {
		t.Fatalf("cached tier %q, want %q", cached, CommandCodeTierGoat)
	}
}
