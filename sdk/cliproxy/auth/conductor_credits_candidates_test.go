package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestFindAllAntigravityCreditsCandidateAuths_PrefersKnownCreditsThenUnknown(t *testing.T) {
	m := &Manager{
		auths: map[string]*Auth{
			"zz-credits": {ID: "zz-credits", Provider: "antigravity"},
			"aa-unknown": {ID: "aa-unknown", Provider: "antigravity"},
			"mm-no":      {ID: "mm-no", Provider: "antigravity"},
		},
		executors: map[string]ProviderExecutor{
			"antigravity": schedulerTestExecutor{},
		},
	}

	SetAntigravityCreditsHint("zz-credits", AntigravityCreditsHint{
		Known:     true,
		Available: true,
		UpdatedAt: time.Now(),
	})
	SetAntigravityCreditsHint("mm-no", AntigravityCreditsHint{
		Known:     true,
		Available: false,
		UpdatedAt: time.Now(),
	})

	opts := cliproxyexecutor.Options{}

	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), "claude-sonnet-4-6", opts)
	if errCandidates != nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates len = %d, want 2", len(candidates))
	}
	if candidates[0].auth.ID != "zz-credits" {
		t.Fatalf("candidates[0].auth.ID = %q, want %q", candidates[0].auth.ID, "zz-credits")
	}
	if candidates[1].auth.ID != "aa-unknown" {
		t.Fatalf("candidates[1].auth.ID = %q, want %q", candidates[1].auth.ID, "aa-unknown")
	}

	nonClaude, errNonClaude := m.findAllAntigravityCreditsCandidateAuths(context.Background(), "gemini-3-flash", opts)
	if errNonClaude != nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths(non claude) error = %v", errNonClaude)
	}
	if len(nonClaude) != 0 {
		t.Fatalf("nonClaude len = %d, want 0", len(nonClaude))
	}

	pinnedOpts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "aa-unknown"},
	}
	pinned, errPinned := m.findAllAntigravityCreditsCandidateAuths(context.Background(), "claude-sonnet-4-6", pinnedOpts)
	if errPinned != nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths(pinned) error = %v", errPinned)
	}
	if len(pinned) != 1 {
		t.Fatalf("pinned len = %d, want 1", len(pinned))
	}
	if pinned[0].auth.ID != "aa-unknown" {
		t.Fatalf("pinned[0].auth.ID = %q, want %q", pinned[0].auth.ID, "aa-unknown")
	}
}

func TestFindAllAntigravityCreditsCandidateAuths_HomeKVUnavailableReturnsError(t *testing.T) {
	homekv.SetCurrent(homekv.New(internalconfig.HomeConfig{Enabled: false}))
	t.Cleanup(homekv.ClearCurrent)

	m := &Manager{
		auths: map[string]*Auth{
			"ag-home-kv": {ID: "ag-home-kv", Provider: "antigravity"},
		},
		executors: map[string]ProviderExecutor{
			"antigravity": schedulerTestExecutor{},
		},
	}

	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), "claude-sonnet-4-6", cliproxyexecutor.Options{})
	if errCandidates == nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = nil, candidates=%#v", candidates)
	}
	if status := statusCodeFromError(errCandidates); status != http.StatusServiceUnavailable {
		t.Fatalf("statusCodeFromError() = %d, want %d; err=%v", status, http.StatusServiceUnavailable, errCandidates)
	}
	if !strings.Contains(errCandidates.Error(), "home kv store unavailable") {
		t.Fatalf("error = %v, want home kv store unavailable", errCandidates)
	}
}
