package cliproxy

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestClaudeQuotaRoutingSelectorFactory(t *testing.T) {
	for _, tc := range []struct {
		strategy string
		affinity bool
	}{
		{"round-robin", false}, {"wrr", false}, {"ff", false}, {"round-robin", true}, {"wrr", true}, {"ff", true},
	} {
		name := tc.strategy
		if tc.affinity {
			name += "/affinity"
		}
		t.Run(name, func(t *testing.T) {
			cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: tc.strategy, ClaudeQuotaSnapshot: "  /tmp/claude-quota.json  ", SessionAffinity: tc.affinity, SessionAffinityTTL: "15m"}}
			state := normalizedRoutingRuntimeState(cfg)
			selector := newRoutingSelector(state)
			quota, ok := selector.(*coreauth.ClaudeQuotaSelector)
			if !ok {
				t.Fatalf("selector=%T, want native Claude quota selector", selector)
			}
			if quota.SnapshotPath != "/tmp/claude-quota.json" {
				t.Fatalf("snapshot path=%q", quota.SnapshotPath)
			}
			if tc.affinity {
				affinity, ok := quota.Fallback.(*coreauth.SessionAffinitySelector)
				if !ok {
					t.Fatalf("fallback=%T, want session affinity", quota.Fallback)
				}
				t.Cleanup(affinity.Stop)
			} else {
				switch tc.strategy {
				case "wrr":
					if _, ok := quota.Fallback.(*coreauth.WeightedRoundRobinSelector); !ok {
						t.Fatalf("fallback=%T", quota.Fallback)
					}
				case "ff":
					if _, ok := quota.Fallback.(*coreauth.FillFirstSelector); !ok {
						t.Fatalf("fallback=%T", quota.Fallback)
					}
				default:
					if _, ok := quota.Fallback.(*coreauth.RoundRobinSelector); !ok {
						t.Fatalf("fallback=%T", quota.Fallback)
					}
				}
			}
			if state.sessionAffinityTTL != 15*time.Minute {
				t.Fatalf("affinity TTL=%v", state.sessionAffinityTTL)
			}
		})
	}
	for _, path := range []string{"", " \t\n "} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{ClaudeQuotaSnapshot: path}})
		selector := newRoutingSelector(state)
		if _, ok := selector.(*coreauth.RoundRobinSelector); !ok {
			t.Fatalf("empty path selector=%T", selector)
		}
	}
}

func TestClaudeQuotaManagerConfigChanges(t *testing.T) {
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{coreManager: manager}
	apply := func(path, strategy string, affinity bool) coreauth.Selector {
		t.Helper()
		cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{ClaudeQuotaSnapshot: path, Strategy: strategy, SessionAffinity: affinity}}
		if !service.applyManagerConfig(context.Background(), configCommit{cfg: cfg, sequence: 1}) {
			t.Fatal("apply manager config failed")
		}
		return manager.Selector()
	}
	checkQuota := func(selector coreauth.Selector, path string) *coreauth.ClaudeQuotaSelector {
		t.Helper()
		quota, ok := selector.(*coreauth.ClaudeQuotaSelector)
		if !ok {
			t.Fatalf("selector=%T, want Claude quota wrapper", selector)
		}
		if quota.SnapshotPath != path {
			t.Fatalf("path=%q, want %q", quota.SnapshotPath, path)
		}
		return quota
	}
	first := apply(" /tmp/first-quota.json ", "round-robin", false)
	checkQuota(first, "/tmp/first-quota.json")
	if same := apply("/tmp/first-quota.json", "round-robin", false); same != first {
		t.Fatal("equivalent routing config replaced selector")
	}
	second := apply("/tmp/second-quota.json", "round-robin", false)
	if second == first {
		t.Fatal("snapshot path change did not rebuild selector")
	}
	checkQuota(second, "/tmp/second-quota.json")
	weighted := checkQuota(apply("/tmp/second-quota.json", "wrr", false), "/tmp/second-quota.json")
	if _, ok := weighted.Fallback.(*coreauth.WeightedRoundRobinSelector); !ok {
		t.Fatalf("updated strategy fallback=%T", weighted.Fallback)
	}
	affinity := checkQuota(apply("/tmp/second-quota.json", "wrr", true), "/tmp/second-quota.json")
	affinityFallback, ok := affinity.Fallback.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("updated affinity fallback=%T", affinity.Fallback)
	}
	t.Cleanup(affinityFallback.Stop)
	plain := apply("  ", "fill-first", false)
	if _, ok := plain.(*coreauth.FillFirstSelector); !ok {
		t.Fatalf("disabled quota selector=%T, want fill first", plain)
	}
	reenabled := checkQuota(apply("/tmp/third-quota.json", "fill-first", false), "/tmp/third-quota.json")
	if _, ok := reenabled.Fallback.(*coreauth.FillFirstSelector); !ok {
		t.Fatalf("reenabled fallback=%T", reenabled.Fallback)
	}
}

func TestClaudeQuotaRoutingReplacementStopsFallback(t *testing.T) {
	fallback := &trackingStoppableSelector{}
	manager := coreauth.NewManager(nil, &coreauth.ClaudeQuotaSelector{SnapshotPath: "/tmp/quota.json", Fallback: fallback}, nil)
	service := &Service{coreManager: manager}
	cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "round-robin"}}
	if !service.applyManagerConfig(context.Background(), configCommit{cfg: cfg, sequence: 1}) {
		t.Fatal("apply manager config failed")
	}
	if !fallback.stopped {
		t.Fatal("replacing quota wrapper did not stop fallback; session affinity cleanup goroutine would leak")
	}
}
