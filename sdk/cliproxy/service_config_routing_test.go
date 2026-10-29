package cliproxy

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestDefaultRoutingUsesRoundRobinRegardlessOfTokenExpiry(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{})
	if state.strategy != defaultRoutingStrategy {
		t.Fatalf("default routing strategy = %q, want %q", state.strategy, defaultRoutingStrategy)
	}

	selector, ok := newRoutingSelector(state).(*coreauth.RoundRobinSelector)
	if !ok {
		t.Fatalf("default selector type = %T, want *auth.RoundRobinSelector", newRoutingSelector(state))
	}

	auths := []*coreauth.Auth{
		{
			ID:       "expires-soon",
			Provider: "codex",
			Metadata: map[string]any{"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
		},
		{
			ID:       "expires-later",
			Provider: "codex",
			Metadata: map[string]any{"expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339)},
		},
	}

	first, errFirst := selector.Pick(context.Background(), "codex", "gpt-5", cliproxyexecutor.Options{}, auths)
	if errFirst != nil {
		t.Fatalf("first pick: %v", errFirst)
	}
	second, errSecond := selector.Pick(context.Background(), "codex", "gpt-5", cliproxyexecutor.Options{}, auths)
	if errSecond != nil {
		t.Fatalf("second pick: %v", errSecond)
	}
	if first == nil || second == nil || first.ID != "expires-later" || second.ID != "expires-soon" {
		t.Fatalf("picks = %q, %q, want ID-sorted round-robin order independent of expiry", authID(first), authID(second))
	}
}

func authID(auth *coreauth.Auth) string {
	if auth == nil {
		return "<nil>"
	}
	return auth.ID
}
