package auth

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestManagerResetQuotaIfUnchanged(t *testing.T) {
	for _, change := range []string{"none", "cooldown", "replacement", "reregister", "removed", "missing", "unversioned", "trimmed-id"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			original, errRegister := manager.Register(ctx, &Auth{ID: "guarded-reset", Provider: "codex", Metadata: map[string]any{"account_id": "original"}})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			delay := time.Hour
			manager.MarkResult(ctx, Result{AuthID: original.ID, Error: &Error{HTTPStatus: 429, Message: "quota exhausted"}, RetryAfter: &delay})
			expected, _ := manager.GetByID(original.ID)
			switch change {
			case "cooldown":
				manager.MarkResult(ctx, Result{AuthID: original.ID, Error: &Error{HTTPStatus: 429, Message: "fresh quota exhaustion"}, RetryAfter: &delay})
			case "replacement":
				replacement := expected.Clone()
				replacement.Metadata["account_id"] = "different-user"
				if _, errUpdate := manager.Update(ctx, replacement); errUpdate != nil {
					t.Fatal(errUpdate)
				}
			case "reregister":
				if _, errRegister = manager.Register(ctx, expected.Clone()); errRegister != nil {
					t.Fatal(errRegister)
				}
			case "removed":
				manager.Remove(ctx, original.ID)
			case "missing":
				expected = nil
			case "unversioned":
				expected.Generation = 0
			case "trimmed-id":
				expected.ID += " "
			}
			before, _ := manager.GetByID(original.ID)
			updated, models, errReset := manager.ResetQuotaIfUnchanged(ctx, expected)
			if errReset != nil {
				t.Fatal(errReset)
			}
			after, _ := manager.GetByID(original.ID)
			if change == "none" {
				if updated == nil || after.Unavailable || !after.NextRetryAfter.IsZero() || after.Quota.Exceeded {
					t.Fatalf("unchanged credential was not cleared: %+v", after)
				}
			} else if updated != nil || len(models) != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("stale observation mutated current credential (%s)", change)
			}
		})
	}
}
