package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

const (
	holdTestWindow   = 30 * time.Minute
	holdTestAffinity = time.Hour
)

var holdTestHeaders = http.Header{"X-Claude-Code-Session-Id": []string{"hold-session"}}

// holdTestBody declares a prompt cache with the given ttl ("" for the 5-minute default, "none" for no cache).
func holdTestBody(ttl string) []byte {
	switch ttl {
	case "none":
		return []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	case "":
		return []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)
	}
	return []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"` + ttl + `"}}]}]}`)
}

func holdTestKey(t *testing.T, body []byte) string {
	t.Helper()
	sessionID, _ := extractSessionIDs(holdTestHeaders, body, nil)
	if sessionID == "" {
		t.Fatal("no session ID in the test request")
	}
	return affinityProvider + "::" + cliproxysession.BoundSessionIdentity(sessionID) + "::" + canonicalModelKey(exhaustionTestModel)
}

func newHoldSelector(hold time.Duration) *SessionAffinitySelector {
	return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{}, TTL: holdTestAffinity, RebindOn: SessionAffinityRebindOnQuota, Hold: hold,
	})
}

// spentClaudeAuth is a credential benched by a 5-hour usage limit that resets at reset.
func spentClaudeAuth(t *testing.T, id string, reset time.Time) *Auth {
	return credentialLimitAuth(t, id, map[string]string{
		claudeUnifiedSignalPrefix + "5h-Status": QuotaWindowStatusRejected,
		claudeUnifiedSignalPrefix + "5h-Reset":  strconv.FormatInt(reset.Unix(), 10),
	}, reset)
}

func TestDeclaredPromptCacheLifetime(t *testing.T) {
	tests := map[string]time.Duration{"1h": time.Hour, "": defaultPromptCacheLifetime, "none": 0}
	for ttl, want := range tests {
		if got := declaredPromptCacheLifetime(holdTestBody(ttl)); got != want {
			t.Errorf("ttl %q: lifetime = %v, want %v", ttl, got, want)
		}
	}
	system := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[]}`)
	if got := declaredPromptCacheLifetime(system); got != time.Hour {
		t.Errorf("system cache lifetime = %v, want 1h", got)
	}
}

func TestHeldUsageLimit(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	tests := []struct {
		name      string
		resetIn   time.Duration
		servedAgo time.Duration
		ttl       string
		spent     bool
		want      bool
	}{
		{"reset inside the hold and the cache", 20 * time.Minute, 10 * time.Minute, "1h", true, true},
		{"reset past the hold", 40 * time.Minute, time.Minute, "1h", true, false},
		{"cache expires before the reset", 20 * time.Minute, 50 * time.Minute, "1h", true, false},
		{"five-minute cache", 20 * time.Minute, time.Minute, "", true, false},
		{"no prompt cache", 20 * time.Minute, time.Minute, "none", true, false},
		{"bound credential not spent", 20 * time.Minute, time.Minute, "1h", false, false},
		{"never served", 20 * time.Minute, 0, "1h", true, false},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selector := newHoldSelector(holdTestWindow)
			body := holdTestBody(tc.ttl)
			key := holdTestKey(t, body)
			id := "held-" + strconv.Itoa(i)
			reset := now.Add(tc.resetIn)
			bound := registerExhaustionAuth(t, &Auth{ID: id, Provider: "claude", Status: StatusActive})
			if tc.spent {
				bound = spentClaudeAuth(t, id, reset)
			}
			selector.cache.Set(key, id)
			if tc.servedAgo > 0 {
				selector.served.record(key, now.Add(-tc.servedAgo), holdTestAffinity)
			}
			opts := cliproxyexecutor.Options{Headers: holdTestHeaders, OriginalRequest: body}
			got, held := selector.heldUsageLimit(opts, exhaustionTestModel, []*Auth{bound}, now)
			if held != tc.want || (held && !got.ResetsAt.Equal(reset)) {
				t.Fatalf("held = %v at %v, want %v at %v", held, got.ResetsAt, tc.want, reset)
			}
		})
	}
}

// A held session answers with its own credential's reset instead of moving to a usable one.
func TestExecute_HeldSessionWaitsForItsReset(t *testing.T) {
	for _, hold := range []time.Duration{holdTestWindow, 0} {
		selector := newHoldSelector(hold)
		manager := NewManager(nil, selector, nil)
		manager.RegisterExecutor(&issue6865Executor{id: "claude"})
		reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
		suffix := strconv.FormatInt(int64(hold), 10)
		for _, auth := range []*Auth{
			spentClaudeAuth(t, "held-spent-"+suffix, reset),
			registerExhaustionAuth(t, &Auth{ID: "held-usable-" + suffix, Provider: "claude", Status: StatusActive}),
		} {
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("Register: %v", errRegister)
			}
		}
		body := holdTestBody("1h")
		key := holdTestKey(t, body)
		selector.cache.Set(key, "held-spent-"+suffix)
		selector.served.record(key, time.Now().Add(-time.Minute), holdTestAffinity)

		req := cliproxyexecutor.Request{Model: exhaustionTestModel}
		_, errExec := manager.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{Headers: holdTestHeaders, OriginalRequest: body})
		if hold == 0 {
			if errExec != nil {
				t.Fatalf("without a hold the session should move to the usable credential: %v", errExec)
			}
			continue
		}
		assertUsageLimit(t, errExec, time.Until(reset))
	}
}

func TestOnResult_HoldKeepsBindingThroughA429(t *testing.T) {
	for _, hold := range []time.Duration{holdTestWindow, 0} {
		selector := newHoldSelector(hold)
		body := holdTestBody("1h")
		key := holdTestKey(t, body)
		selector.cache.Set(key, "bound")
		selector.OnResult(Result{
			AuthID: "bound", Provider: "claude", Model: exhaustionTestModel,
			Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "limit"},
			Options: cliproxyexecutor.Options{Headers: holdTestHeaders, OriginalRequest: body, Metadata: map[string]any{
				cliproxyexecutor.SessionAffinityProviderMetadataKey: affinityProvider,
				cliproxyexecutor.SessionAffinityModelMetadataKey:    exhaustionTestModel,
			}},
		})
		if _, _, bound := selector.cache.peek(key); bound != (hold > 0) {
			t.Fatalf("hold %v: binding kept = %v, want %v", hold, bound, hold > 0)
		}
	}
}

func TestOnResult_TracksServedSessionsOnlyWhenHoldEnabled(t *testing.T) {
	for _, hold := range []time.Duration{0, holdTestWindow} {
		t.Run(hold.String(), func(t *testing.T) {
			selector := newHoldSelector(hold)
			t.Cleanup(selector.cache.Stop)
			body := holdTestBody("1h")
			key := holdTestKey(t, body)
			selector.cache.Set(key, "bound")
			for range 3 {
				selector.OnResult(Result{
					AuthID: "bound", Provider: "claude", Model: exhaustionTestModel, Success: true,
					Options: cliproxyexecutor.Options{Headers: holdTestHeaders, OriginalRequest: body, Metadata: map[string]any{
						cliproxyexecutor.SessionAffinityProviderMetadataKey: affinityProvider,
						cliproxyexecutor.SessionAffinityModelMetadataKey:    exhaustionTestModel,
					}},
				})
			}
			if authID, _, bound := selector.cache.peek(key); !bound || authID != "bound" {
				t.Fatalf("successful requests lost their binding: %q, bound=%v", authID, bound)
			}
			served := selector.served.lastServed(key)
			if tracked := !served.IsZero(); tracked != (hold > 0) {
				t.Fatalf("hold %v: served timestamp = %v", hold, served)
			}
			if hold == 0 && len(selector.served.at) != 0 {
				t.Fatalf("hold disabled: retained %d served sessions", len(selector.served.at))
			}
		})
	}
}
