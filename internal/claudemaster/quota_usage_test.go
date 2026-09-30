package claudemaster

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestParseClaudeWeeklyQuotaLegacy(t *testing.T) {
	wantReset := time.Date(2026, time.October, 4, 12, 30, 0, 0, time.UTC)
	tests := []struct {
		name    string
		payload string
		used    float64
		reset   time.Time
	}{
		{
			name:    "RFC3339 reset",
			payload: `{"five_hour":{"utilization":1},"seven_day":{"utilization":42.5,"resets_at":"2026-10-04T12:30:00Z"}}`,
			used:    0.425,
			reset:   wantReset,
		},
		{
			name:    "epoch reset and string utilization",
			payload: `{"seven_day":{"utilization":"75","resets_at":1791117000}}`,
			used:    0.75,
			reset:   wantReset,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quota, known, err := ParseClaudeWeeklyQuota([]byte(test.payload))
			if err != nil {
				t.Fatalf("ParseClaudeWeeklyQuota() error = %v", err)
			}
			if !known {
				t.Fatal("ParseClaudeWeeklyQuota() did not recognize weekly quota")
			}
			if quota.UsedFraction != test.used {
				t.Fatalf("UsedFraction = %v, want %v", quota.UsedFraction, test.used)
			}
			if !quota.ResetsAt.Equal(test.reset) {
				t.Fatalf("ResetsAt = %v, want %v", quota.ResetsAt, test.reset)
			}
		})
	}
}

func TestParseClaudeWeeklyQuotaRateLimits(t *testing.T) {
	payload := []byte(`{
		"rate_limits": {
			"limits": [
				{"name":"weekly","utilization":20,"resets_at":"2026-10-05T00:00:00Z"},
				{"name":"five_hour","utilization":1,"resets_at":"2026-09-30T12:00:00Z"},
				{"kind":"weekly_all","group":"weekly","percent":91,"resets_at":"2026-10-03T00:00:00Z","is_active":true}
			]
		}
	}`)
	quota, known, err := ParseClaudeWeeklyQuota(payload)
	if err != nil {
		t.Fatalf("ParseClaudeWeeklyQuota() error = %v", err)
	}
	if !known {
		t.Fatal("ParseClaudeWeeklyQuota() did not recognize weekly quota")
	}
	if quota.UsedFraction != 0.91 {
		t.Fatalf("UsedFraction = %v, want 0.91", quota.UsedFraction)
	}
	wantReset := time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC)
	if !quota.ResetsAt.Equal(wantReset) {
		t.Fatalf("ResetsAt = %v, want %v", quota.ResetsAt, wantReset)
	}
}

func TestParseClaudeWeeklyQuotaCurrentTopLevelLimits(t *testing.T) {
	payload := []byte(`{
		"seven_day": {"utilization": 12, "resets_at": "2026-10-05T00:00:00Z"},
		"limits": [
			{"kind":"weekly_scoped","group":"weekly","percent":80,"resets_at":"2026-10-04T00:00:00Z","severity":"warning","is_active":true},
			{"kind":"weekly_all","group":"weekly","percent":35,"resets_at":"2026-10-03T00:00:00Z","severity":"normal","is_active":false}
		]
	}`)
	quota, known, err := ParseClaudeWeeklyQuota(payload)
	if err != nil {
		t.Fatalf("ParseClaudeWeeklyQuota() error = %v", err)
	}
	if !known {
		t.Fatal("ParseClaudeWeeklyQuota() did not recognize current limits[]")
	}
	if quota.UsedFraction != 0.35 {
		t.Fatalf("UsedFraction = %v, want 0.35", quota.UsedFraction)
	}
	wantReset := time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC)
	if !quota.ResetsAt.Equal(wantReset) {
		t.Fatalf("ResetsAt = %v, want %v", quota.ResetsAt, wantReset)
	}
}

func TestParseClaudeWeeklyQuotaRateLimitsMap(t *testing.T) {
	payload := []byte(`{
		"rate_limits": {
			"limits": {
				"weekly": {"utilization":15,"resets_at":"2026-10-05T00:00:00Z"},
				"weekly-all": {"utilization":125,"reset_at":1790985600000}
			}
		}
	}`)
	quota, known, err := ParseClaudeWeeklyQuota(payload)
	if err != nil {
		t.Fatalf("ParseClaudeWeeklyQuota() error = %v", err)
	}
	if !known {
		t.Fatal("ParseClaudeWeeklyQuota() did not recognize weekly quota")
	}
	if quota.UsedFraction != 1 {
		t.Fatalf("UsedFraction = %v, want clamped value 1", quota.UsedFraction)
	}
	wantReset := time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC)
	if !quota.ResetsAt.Equal(wantReset) {
		t.Fatalf("ResetsAt = %v, want %v", quota.ResetsAt, wantReset)
	}
}

func TestParseClaudeWeeklyQuotaUnknownAndInvalid(t *testing.T) {
	quota, known, err := ParseClaudeWeeklyQuota([]byte(`{"rate_limits":{"limits":[{"name":"five_hour","utilization":50,"resets_at":1790985600}]}}`))
	if err != nil || known || quota != (ClaudeWeeklyQuota{}) {
		t.Fatalf("unknown schema = (%+v, %v, %v), want zero, false, nil", quota, known, err)
	}
	if _, _, err = ParseClaudeWeeklyQuota([]byte(`{"seven_day":`)); err == nil {
		t.Fatal("invalid JSON unexpectedly parsed")
	}
	if _, _, err = ParseClaudeWeeklyQuota([]byte(`{} {}`)); err == nil {
		t.Fatal("multiple JSON values unexpectedly parsed")
	}
}

func TestParseClaudeWeeklyQuotaHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		used    float64
		reset   time.Time
		known   bool
	}{
		{
			name: "epoch reset",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.53"},
				"Anthropic-Ratelimit-Unified-7d-Reset":       []string{"1790985600"},
			},
			used:  0.53,
			reset: time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC),
			known: true,
		},
		{
			name: "RFC3339 reset and overage",
			headers: http.Header{
				"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"1.02"},
				"Anthropic-Ratelimit-Unified-7d-Reset":       []string{"2026-10-03T00:00:00Z"},
			},
			used:  1,
			reset: time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC),
			known: true,
		},
		{name: "missing reset", headers: http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.1"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quota, known := ParseClaudeWeeklyQuotaHeaders(test.headers)
			if known != test.known {
				t.Fatalf("known = %v, want %v", known, test.known)
			}
			if !known {
				return
			}
			if quota.UsedFraction != test.used || !quota.ResetsAt.Equal(test.reset) {
				t.Fatalf("quota = %+v, want used %v reset %v", quota, test.used, test.reset)
			}
		})
	}
}

func TestFetchClaudeWeeklyQuota(t *testing.T) {
	auth := &coreauth.Auth{ID: "quota-account", Provider: "claude"}
	quota, known, err := FetchClaudeWeeklyQuota(context.Background(), auth, time.Second, func(_ context.Context, gotAuth *coreauth.Auth, request *http.Request) (*http.Response, error) {
		if gotAuth != auth {
			t.Fatal("quota callback did not receive selected account")
		}
		if request.Method != http.MethodGet || request.URL.String() != ClaudeOAuthUsageEndpoint {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Accept") != "application/json" {
			t.Fatalf("Accept = %q", request.Header.Get("Accept"))
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatal("quota helper unexpectedly supplied authorization")
		}
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
			t.Fatalf("request deadline = %v, want active one-second timeout", deadline)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":25,"resets_at":1790985600}}`)),
		}, nil
	})
	if err != nil {
		t.Fatalf("FetchClaudeWeeklyQuota() error = %v", err)
	}
	if !known || quota.UsedFraction != 0.25 {
		t.Fatalf("quota = (%+v, %v), want known 0.25", quota, known)
	}
}

func TestFetchClaudeWeeklyQuotaFailures(t *testing.T) {
	tests := []struct {
		name string
		do   ClaudeQuotaRequestFunc
	}{
		{
			name: "request error",
			do: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
				return nil, errors.New("offline")
			},
		},
		{
			name: "HTTP error excludes body",
			do: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("secret upstream detail"))}, nil
			},
		},
		{
			name: "oversized body",
			do: func(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", claudeQuotaMaxBodyBytes+1)))}, nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := FetchClaudeWeeklyQuota(context.Background(), &coreauth.Auth{ID: "quota-account", Provider: "claude"}, 0, test.do)
			if err == nil {
				t.Fatal("FetchClaudeWeeklyQuota() unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), "secret upstream detail") {
				t.Fatalf("error exposed upstream body: %v", err)
			}
		})
	}
}

func TestLoadBackendWeeklyQuotasOrdersRegisteredAccountsByReset(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	// The fixture resets are fixed dates, so the selector gets a clock before them: with the real
	// clock, once both dates were past, currentQuotaLocked rolled them forward and the order flipped.
	selector := &backendSeriesSelector{authIDs: []string{"later", "sooner"}, provider: "claude", now: func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }}
	t.Cleanup(selector.Stop)
	for _, authID := range selector.authIDs {
		_, err := manager.Register(t.Context(), &coreauth.Auth{
			ID:       authID,
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
			},
			Metadata: map[string]any{
				"access_token": "opaque-" + authID,
				"expired":      "2099-01-01T00:00:00Z",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	loadBackendWeeklyQuotas(t.Context(), manager, selector, selector.authIDs, func(_ context.Context, auth *coreauth.Auth, _ *http.Request) (*http.Response, error) {
		reset := "2026-10-04T00:00:00Z"
		if auth.ID == "sooner" {
			reset = "2026-10-02T00:00:00Z"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":25,"resets_at":"` + reset + `"}}`)),
		}, nil
	})

	requireBackendSeriesPick(t, selector, "claude", []*coreauth.Auth{
		backendSeriesTestAuth("later", "claude"),
		backendSeriesTestAuth("sooner", "claude"),
	}, "sooner")
}
