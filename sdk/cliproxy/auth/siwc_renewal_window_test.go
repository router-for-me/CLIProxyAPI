package auth

import (
	"testing"
	"time"
)

func TestSIWCRenewalUsesRealExpiryAndServerWindow(t *testing.T) {
	expiry := time.Date(2026, 10, 29, 13, 8, 9, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		earliest any
		wantDue  string
	}{
		{"real expiry without double margin", nil, "2026-10-29T13:03:09Z"},
		{"early server window", "2026-10-29T12:58:09Z", "2026-10-29T13:03:09Z"},
		{"server RFC3339 window", "2026-10-29T13:07:09Z", "2026-10-29T13:07:09Z"},
		{"server Unix window", int64(1793279229), "2026-10-29T13:07:09Z"},
		{"server JSON Unix window", float64(1793279229), "2026-10-29T13:07:09Z"},
		{"malformed optional window", "invalid", "2026-10-29T13:03:09Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			siwc := map[string]any{
				"access_token": "access", "refresh_token": "refresh", "client_id": "oaiapp_test",
				"last_refresh": "2026-10-29T12:08:09Z", "expired": "2026-10-29T13:05:09Z",
				"expires_at": "2026-10-29T13:08:09Z", "earliest_refresh_at": tc.earliest,
			}
			auth := &Auth{Provider: "codex", Metadata: map[string]any{"siwc": siwc}}
			wantDue, errParse := time.Parse(time.RFC3339, tc.wantDue)
			if errParse != nil {
				t.Fatal(errParse)
			}
			due, has, need := CodexSIWCNeedsRefresh(auth, expiry.Add(-8*time.Minute))
			if !has || need || !due.Equal(wantDue) {
				t.Fatalf("due = %v, has = %v, need = %v; want %v, true, false", due, has, need, wantDue)
			}
			for _, now := range []time.Time{wantDue, expiry.Add(3 * time.Hour)} {
				if _, has, need := CodexSIWCNeedsRefresh(auth, now); !has || !need {
					t.Fatalf("at %v: has = %v, need = %v; want renewal", now, has, need)
				}
			}
		})
	}
}

func TestSIWCRenewalServerWindowWithoutUsableAccessToken(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	earliest := now.Add(time.Minute)
	for _, accessToken := range []string{"", "rejected", "opaque-without-expiry"} {
		t.Run(accessToken, func(t *testing.T) {
			auth := &Auth{RejectedSIWCAccessToken: "rejected", Metadata: map[string]any{
				"siwc": map[string]any{
					"access_token": accessToken, "refresh_token": "refresh", "client_id": "oaiapp_window",
					"earliest_refresh_at": "2026-10-29T12:01:00Z",
				},
			}}
			due, has, need := CodexSIWCNeedsRefresh(auth, now)
			if !has || need || !due.Equal(earliest) {
				t.Fatalf("due = %v, has = %v, need = %v; want %v, true, false", due, has, need, earliest)
			}
			if _, has, need := CodexSIWCNeedsRefresh(auth, earliest); !has || !need {
				t.Fatal("renewal was not due at the server window")
			}
		})
	}
}
