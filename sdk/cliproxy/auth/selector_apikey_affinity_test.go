package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func apiKeyOpts(key string) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{Headers: http.Header{"Authorization": {"Bearer " + key}}}
}

func affinityAuths(ids ...string) []*Auth {
	auths := make([]*Auth, 0, len(ids))
	for _, id := range ids {
		auths = append(auths, &Auth{ID: id, Provider: "codex"})
	}
	return auths
}

func TestAPIKeyAffinity_StickyPerKey(t *testing.T) {
	selector := NewAPIKeyAffinitySelector(&FillFirstSelector{})
	auths := affinityAuths("acct-a", "acct-b", "acct-c", "acct-d")

	first, err := selector.Pick(context.Background(), "mixed", "gpt-5.5", apiKeyOpts("sk-jeffrey"), auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	// Repeated calls for the same key must resolve to the same account.
	for i := 0; i < 25; i++ {
		got, errPick := selector.Pick(context.Background(), "mixed", "gpt-5.5", apiKeyOpts("sk-jeffrey"), auths)
		if errPick != nil {
			t.Fatalf("Pick() error = %v", errPick)
		}
		if got.ID != first.ID {
			t.Fatalf("sticky violated: got %s, want %s", got.ID, first.ID)
		}
	}
}

func TestAPIKeyAffinity_DistributesAcrossKeys(t *testing.T) {
	selector := NewAPIKeyAffinitySelector(&FillFirstSelector{})
	auths := affinityAuths("acct-a", "acct-b", "acct-c", "acct-d", "acct-e", "acct-f")

	keys := []string{"sk-jeffrey", "sk-doyun", "sk-minsung", "sk-donggun", "sk-yongbin", "sk-bobb", "sk-cheol", "sk-hyunwook"}
	seen := make(map[string]int)
	for _, k := range keys {
		got, err := selector.Pick(context.Background(), "mixed", "gpt-5.5", apiKeyOpts(k), auths)
		if err != nil {
			t.Fatalf("Pick(%s) error = %v", k, err)
		}
		seen[got.ID]++
	}
	// With 8 keys over 6 accounts, rendezvous hashing should spread onto several
	// distinct accounts (not collapse onto one like fill-first would).
	if len(seen) < 3 {
		t.Fatalf("expected keys spread across >=3 accounts, got %d distinct: %v", len(seen), seen)
	}
}

func TestAPIKeyAffinity_DeterministicFailover(t *testing.T) {
	selector := NewAPIKeyAffinitySelector(&FillFirstSelector{})
	full := affinityAuths("acct-a", "acct-b", "acct-c", "acct-d")

	home, err := selector.Pick(context.Background(), "mixed", "gpt-5.5", apiKeyOpts("sk-jeffrey"), full)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}

	// Simulate the home account being cooled down (removed from the candidate set).
	reduced := make([]*Auth, 0, len(full)-1)
	for _, a := range full {
		if a.ID != home.ID {
			reduced = append(reduced, a)
		}
	}
	failover1, err := selector.Pick(context.Background(), "mixed", "gpt-5.5", apiKeyOpts("sk-jeffrey"), reduced)
	if err != nil {
		t.Fatalf("failover Pick() error = %v", err)
	}
	if failover1.ID == home.ID {
		t.Fatalf("failover returned the removed home account %s", home.ID)
	}
	// Failover must be deterministic: same reduced set => same choice.
	failover2, err := selector.Pick(context.Background(), "mixed", "gpt-5.5", apiKeyOpts("sk-jeffrey"), reduced)
	if err != nil {
		t.Fatalf("failover Pick() error = %v", err)
	}
	if failover1.ID != failover2.ID {
		t.Fatalf("failover not deterministic: %s vs %s", failover1.ID, failover2.ID)
	}
}

func TestAPIKeyAffinity_NoKeyFallsBack(t *testing.T) {
	selector := NewAPIKeyAffinitySelector(&FillFirstSelector{})
	auths := affinityAuths("acct-a", "acct-b", "acct-c")

	// FillFirst fallback returns the lexicographically first available auth.
	got, err := selector.Pick(context.Background(), "mixed", "gpt-5.5", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "acct-a" {
		t.Fatalf("no-key fallback = %s, want acct-a (fill-first)", got.ID)
	}
}
