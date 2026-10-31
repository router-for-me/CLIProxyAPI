package auth

import (
	"context"
	"testing"
	"time"
)

func TestRequestRefreshUnknownExpiryCannotTriggerManagedRotation(t *testing.T) {
	for _, mode := range []string{"missing", "zero", "malformed", "no-token"} {
		t.Run(mode, func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
			switch mode {
			case "missing":
				delete(a.Metadata, "expired")
			case "zero":
				a.Metadata["expired"] = 0
			case "malformed":
				a.Metadata["expired"] = "not an expiry"
			case "no-token":
				delete(a.Metadata, "access_token")
			}
			a, _ = m.Update(context.Background(), a)
			if _, err := m.EnsureRequestAuth(context.Background(), a, ""); err == nil || e.refreshes.Load() != 0 {
				t.Fatalf("unknown token lifecycle rotated: %v count=%d", err, e.refreshes.Load())
			}
		})
	}
}
