package auth

import "testing"

func TestApplyCodexSubscriptionAttributes(t *testing.T) {
	t.Run("copies plan and expiry from metadata", func(t *testing.T) {
		a := &Auth{Metadata: map[string]any{
			"plan_type":                 " plus ",
			"subscription_active_until": "2030-01-01T00:00:00Z",
		}}
		ApplyCodexSubscriptionAttributes(a)
		if a.Attributes["plan_type"] != "plus" {
			t.Fatalf("plan_type=%q, want plus", a.Attributes["plan_type"])
		}
		if a.Attributes["subscription_active_until"] != "2030-01-01T00:00:00Z" {
			t.Fatalf("subscription_active_until=%q", a.Attributes["subscription_active_until"])
		}
	})

	t.Run("overrides an existing seed value", func(t *testing.T) {
		a := &Auth{
			Attributes: map[string]string{"plan_type": "free"},
			Metadata:   map[string]any{"plan_type": "pro"},
		}
		ApplyCodexSubscriptionAttributes(a)
		if a.Attributes["plan_type"] != "pro" {
			t.Fatalf("plan_type=%q, want pro (metadata overrides seed)", a.Attributes["plan_type"])
		}
	})

	t.Run("no metadata is a safe no-op", func(t *testing.T) {
		a := &Auth{Attributes: map[string]string{"plan_type": "keep"}}
		ApplyCodexSubscriptionAttributes(a)
		if a.Attributes["plan_type"] != "keep" {
			t.Fatalf("plan_type=%q, want keep", a.Attributes["plan_type"])
		}
		ApplyCodexSubscriptionAttributes(nil) // must not panic
	})
}
