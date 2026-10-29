package auth

import (
	"context"
	"testing"
	"time"
)

func TestRequestRefreshLateDemandReusesCommittedExpiry(t *testing.T) {
	for _, sameToken := range []bool{false, true} {
		t.Run(map[bool]string{false: "rotated", true: "same-token"}[sameToken], func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
			if sameToken {
				e.refresh = func(_ context.Context, auth *Auth) (*Auth, error) {
					auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
					return auth, nil
				}
			}
			if _, err := m.EnsureRequestAuth(context.Background(), a, ""); err != nil {
				t.Fatal(err)
			}
			current, err := m.managedRefresh(context.Background(), a, false, "")
			if err != nil || !current.HasValidAccessToken(time.Now()) || e.refreshes.Load() != 1 {
				t.Fatalf("late demand did not reuse committed freshness: %v refreshes=%d", err, e.refreshes.Load())
			}
		})
	}
}
