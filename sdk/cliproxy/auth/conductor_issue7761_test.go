package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// Note: executorKeyFromAuth currently maps "kimi.com" -> "kimi" and
// "kimi.ai" -> "kimi-ai" directly (matching canonicalSchedulingProvider), so no
// real Auth currently produces a key that changes under a second normalization.
// These tests guard end-to-end alias behavior for pickNextMixedLegacy and
// warnLogAuthUnavailable alongside the explicit canonicalSchedulingProvider
// normalization in conductor_selection.go.
func TestPickNextMixedLegacy_CanonicalSchedulingProviderAliases(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	model := "issue7761-kimi-model"

	authCom := &Auth{
		ID:       "issue7761-kimi-com-auth",
		Provider: "kimi.com",
		Status:   StatusActive,
	}
	authAI := &Auth{
		ID:       "issue7761-kimi-ai-auth",
		Provider: "kimi.ai",
		Status:   StatusActive,
	}

	reg.RegisterClient(authCom.ID, authCom.Provider, []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(authAI.ID, authAI.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authCom.ID)
		reg.UnregisterClient(authAI.ID)
	})

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&refreshMockExecutor{id: "kimi"})
	if _, errReg := manager.Register(ctx, authCom); errReg != nil {
		t.Fatalf("register kimi.com auth: %v", errReg)
	}
	if _, errReg := manager.Register(ctx, authAI); errReg != nil {
		t.Fatalf("register kimi.ai auth: %v", errReg)
	}

	cases := []struct {
		name      string
		providers []string
		wantID    string
		wantKey   string
	}{
		{name: "kimi canonical selects kimi.com auth", providers: []string{"kimi"}, wantID: authCom.ID, wantKey: "kimi"},
		{name: "kimi.com alias selects kimi.com auth", providers: []string{"kimi.com"}, wantID: authCom.ID, wantKey: "kimi"},
		{name: "kimi-ai canonical selects kimi.ai auth", providers: []string{"kimi-ai"}, wantID: authAI.ID, wantKey: "kimi-ai"},
		{name: "kimi.ai alias selects kimi.ai auth", providers: []string{"kimi.ai"}, wantID: authAI.ID, wantKey: "kimi-ai"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected, exec, providerKey, errPick := manager.pickNextMixedLegacy(ctx, tc.providers, model, cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickNextMixedLegacy(%v) error = %v", tc.providers, errPick)
			}
			if selected == nil || selected.ID != tc.wantID {
				t.Fatalf("pickNextMixedLegacy(%v) auth = %v, want %q", tc.providers, selected, tc.wantID)
			}
			if exec == nil {
				t.Fatalf("pickNextMixedLegacy(%v) executor = nil", tc.providers)
			}
			if providerKey != tc.wantKey {
				t.Fatalf("pickNextMixedLegacy(%v) providerKey = %q, want %q", tc.providers, providerKey, tc.wantKey)
			}
		})
	}
}

func TestWarnLogAuthUnavailable_CanonicalSchedulingProviderAliases(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	model := "issue7761-kimi-warn-model"

	now := time.Now()
	recoverAt := now.Add(5 * time.Minute)
	authCom := &Auth{
		ID:       "issue7761-warn-kimi-com",
		Provider: "kimi.com",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: recoverAt,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: recoverAt,
				},
			},
		},
	}
	authAI := &Auth{
		ID:       "issue7761-warn-kimi-ai",
		Provider: "kimi.ai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: recoverAt,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: recoverAt,
				},
			},
		},
	}

	reg.RegisterClient(authCom.ID, authCom.Provider, []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(authAI.ID, authAI.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authCom.ID)
		reg.UnregisterClient(authAI.ID)
	})

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&refreshMockExecutor{id: "kimi"})
	if _, errReg := manager.Register(ctx, authCom); errReg != nil {
		t.Fatalf("register kimi.com auth: %v", errReg)
	}
	if _, errReg := manager.Register(ctx, authAI); errReg != nil {
		t.Fatalf("register kimi.ai auth: %v", errReg)
	}

	for _, tc := range []struct {
		name      string
		providers []string
		wantID    string
	}{
		{name: "kimi.com alias logs cooling kimi.com candidate", providers: []string{"kimi.com"}, wantID: authCom.ID},
		{name: "kimi.ai alias logs cooling kimi.ai candidate", providers: []string{"kimi.ai"}, wantID: authAI.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := setupTestLoggerHook(t)
			manager.warnLogAuthUnavailable(ctx, tc.providers, model, cliproxyexecutor.Options{}, nil, &Error{Code: "auth_unavailable"})
			foundWarn := false
			for _, entry := range hook.AllEntries() {
				if entry.Level == log.WarnLevel && strings.Contains(entry.Message, model) {
					foundWarn = true
					if !strings.Contains(entry.Message, "1 of 1 candidate(s)") {
						t.Fatalf("warnLogAuthUnavailable unexpected candidate count: %q", entry.Message)
					}
					if !strings.Contains(entry.Message, tc.wantID) {
						t.Fatalf("warnLogAuthUnavailable missing expected auth %q in %q", tc.wantID, entry.Message)
					}
				}
			}
			if !foundWarn {
				t.Fatalf("expected warnLogAuthUnavailable log entry for model %q, got %#v", model, hook.AllEntries())
			}
		})
	}
}
