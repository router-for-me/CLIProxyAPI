package auth

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestQuotaStateObserveResponseHeadersKeepsProviderScopedSignals(t *testing.T) {
	observedAt := time.Unix(123, 0)
	var quota QuotaState
	if !quota.ObserveResponseHeadersForProvider("codex", http.Header{
		"X-Codex-Active-Limit":             []string{"codex_bengalfox"},
		"X-Codex-Primary-Used-Percent":     []string{"2"},
		"X-Codex-Turn-State":               []string{"opaque-state"},
		"X-Codex-Safety-Buffering-Enabled": []string{"true"},
		"Retry-After":                      []string{"120"},
		"Authorization":                    []string{"Bearer secret"},
	}, observedAt) {
		t.Fatal("ObserveResponseHeadersForProvider() reported no change")
	}
	if !quota.ObservedAt.Equal(observedAt) {
		t.Fatalf("ObservedAt = %v, want %v", quota.ObservedAt, observedAt)
	}
	if quota.Signals["X-Codex-Active-Limit"] != "codex_bengalfox" || quota.Signals["Retry-After"] != "120" {
		t.Fatalf("quota signals = %#v", quota.Signals)
	}
	if _, ok := quota.Signals["Authorization"]; ok {
		t.Fatal("authorization header was retained as a quota signal")
	}
	if _, ok := quota.Signals["X-Codex-Turn-State"]; ok {
		t.Fatal("non-quota Codex response header was retained as a quota signal")
	}
	if _, ok := quota.Signals["X-Codex-Safety-Buffering-Enabled"]; ok {
		t.Fatal("Codex safety-buffering metadata was retained as a quota signal")
	}
}

func TestQuotaStateObserveResponseHeadersBoundsAndCanonicalizesValues(t *testing.T) {
	var quota QuotaState
	longValue := strings.Repeat("x", maxQuotaSignalValue+1)
	if quota.ObserveResponseHeadersForProvider("codex", http.Header{
		"X-Codex-Empty": []string{""},
		"X-Codex-Long":  []string{longValue},
	}, time.Unix(123, 0)) {
		t.Fatal("invalid-only headers reported a change")
	}
	if quota.Signals != nil || !quota.ObservedAt.IsZero() {
		t.Fatalf("invalid signals were retained: %#v", quota)
	}
	if !quota.ObserveResponseHeadersForProvider("codex", http.Header{
		"x-codex-plan-type": []string{"pro"},
	}, time.Unix(124, 0)) {
		t.Fatal("canonical valid header reported no change")
	}
	if quota.Signals["X-Codex-Plan-Type"] != "pro" {
		t.Fatalf("canonical signal = %#v", quota.Signals)
	}
}

func TestQuotaStateObserveResponseHeadersRetainsMeasuredClaudeAndCodexWatermarks(t *testing.T) {
	var claudeQuota QuotaState
	if !claudeQuota.ObserveResponseHeadersForProvider("claude", http.Header{
		"Anthropic-Ratelimit-Unified-5h-Status":               []string{"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Utilization":          []string{"0.0"},
		"Anthropic-Ratelimit-Unified-5h-Reset":                []string{"1787296800"},
		"Anthropic-Ratelimit-Unified-7d-Status":               []string{"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":          []string{"0.53"},
		"Anthropic-Ratelimit-Unified-7d-Reset":                []string{"1787695200"},
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":     []string{"0.5"},
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": []string{"member_zero_credit_limit"},
		"Anthropic-Ratelimit-Unified-Overage-Status":          []string{"rejected"},
		"Anthropic-Ratelimit-Unified-Representative-Claim":    []string{"five_hour"},
		"Anthropic-Ratelimit-Unified-Reset":                   []string{"1787296800"},
		"Anthropic-Ratelimit-Unified-Status":                  []string{"allowed"},
		"Anthropic-Workspace-Id":                              []string{"workspace-must-not-be-quota-signal"},
	}, time.Unix(1787279282, 0)) {
		t.Fatal("Claude observation reported no change")
	}
	for key, want := range map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Reset":                "1787296800",
		"Anthropic-Ratelimit-Unified-5h-Status":               "allowed",
		"Anthropic-Ratelimit-Unified-5h-Utilization":          "0.0",
		"Anthropic-Ratelimit-Unified-7d-Reset":                "1787695200",
		"Anthropic-Ratelimit-Unified-7d-Status":               "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization":          "0.53",
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":     "0.5",
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": "member_zero_credit_limit",
		"Anthropic-Ratelimit-Unified-Overage-Status":          "rejected",
		"Anthropic-Ratelimit-Unified-Representative-Claim":    "five_hour",
		"Anthropic-Ratelimit-Unified-Reset":                   "1787296800",
		"Anthropic-Ratelimit-Unified-Status":                  "allowed",
	} {
		if got := claudeQuota.Signals[key]; got != want {
			t.Fatalf("Claude quota signal %s = %q, want %q", key, got, want)
		}
	}
	if _, ok := claudeQuota.Signals["Anthropic-Workspace-Id"]; ok {
		t.Fatal("workspace identity header was retained as a quota signal")
	}

	var codexQuota QuotaState
	if !codexQuota.ObserveResponseHeadersForProvider("codex", http.Header{
		"X-Codex-Plan-Type":                        []string{"pro"},
		"X-Codex-Primary-Used-Percent":             []string{"51"},
		"X-Codex-Primary-Window-Minutes":           []string{"10080"},
		"X-Codex-Primary-Reset-After-Seconds":      []string{"309718"},
		"X-Codex-Primary-Reset-At":                 []string{"1787588999"},
		"X-Codex-Bengalfox-Limit-Name":             []string{"GPT-5.3-Codex-Spark"},
		"X-Codex-Bengalfox-Secondary-Used-Percent": []string{"35"},
		"X-Codex-Credits-Has-Credits":              []string{"False"},
	}, time.Unix(1787279282, 0)) {
		t.Fatal("Codex observation reported no change")
	}
	for key, want := range map[string]string{
		"X-Codex-Plan-Type":                        "pro",
		"X-Codex-Primary-Used-Percent":             "51",
		"X-Codex-Bengalfox-Limit-Name":             "GPT-5.3-Codex-Spark",
		"X-Codex-Bengalfox-Secondary-Used-Percent": "35",
		"X-Codex-Credits-Has-Credits":              "False",
	} {
		if got := codexQuota.Signals[key]; got != want {
			t.Fatalf("Codex quota signal %s = %q, want %q", key, got, want)
		}
	}
}

func TestQuotaStateObserveResponseHeadersDropsKimiGrokAndAntigravitySignals(t *testing.T) {
	for _, provider := range []string{"kimi", "xai", "grok", "antigravity", "gemini", "vertex", "aistudio"} {
		quota := QuotaState{
			ObservedAt: time.Unix(1786082736, 0),
			Signals:    map[string]string{"old": "value"},
		}
		changed := quota.ObserveResponseHeadersForProvider(provider, http.Header{
			"X-Ratelimit-Remaining-Requests":             []string{"0"},
			"X-Ratelimit-Remaining-Tokens":               []string{"0"},
			"Retry-After":                                []string{"60"},
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.5"},
		}, time.Now())
		if !changed || !quota.ObservedAt.IsZero() || len(quota.Signals) != 0 {
			t.Fatalf("provider %s retained observation signals: changed=%v quota=%#v", provider, changed, quota)
		}
	}
}

func TestCooldownEqualityIgnoresObservationSignals(t *testing.T) {
	base := QuotaState{
		Exceeded:      true,
		Reason:        "quota",
		NextRecoverAt: time.Unix(1787588999, 0),
		BackoffLevel:  2,
	}
	observed := base.Clone()
	observed.ObservedAt = time.Unix(1787279282, 0)
	observed.Signals = map[string]string{
		"X-Codex-Primary-Used-Percent": "51",
		"X-Codex-Primary-Reset-At":     "1787588999",
	}
	if !cooldownQuotaEqual(base, observed) {
		t.Fatal("observation-only quota signals changed cooldown equality")
	}
}

func TestManagerMarkResultRecordsResponseQuotaSignalsInMemory(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "quota-signal-auth",
		Provider: "codex",
	})
	if errRegister != nil || auth == nil {
		t.Fatalf("Register() auth=%#v err=%v", auth, errRegister)
	}

	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"X-Codex-Active-Limit":           []string{"codex_bengalfox"},
		"X-Codex-Primary-Used-Percent":   []string{"2"},
		"X-Codex-Primary-Window-Minutes": []string{"10080"},
		"X-Codex-Primary-Reset-At":       []string{"1782951970"},
	})
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.3-codex",
		Success:  true,
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found after MarkResult")
	}
	if updated.Quota.Signals["X-Codex-Active-Limit"] != "codex_bengalfox" ||
		updated.Quota.Signals["X-Codex-Primary-Used-Percent"] != "2" {
		t.Fatalf("in-memory quota signals = %#v", updated.Quota.Signals)
	}
}

func TestMarkResultCountTokensDoesNotReplaceObservation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "quota-count-tokens-auth",
		Provider: "claude",
		Quota: QuotaState{
			ObservedAt: time.Unix(10, 0),
			Signals:    map[string]string{"Anthropic-Ratelimit-Unified-Status": "allowed"},
		},
	})
	if errRegister != nil || auth == nil {
		t.Fatalf("Register() auth=%#v err=%v", auth, errRegister)
	}

	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-Status": []string{"rejected"},
	})
	manager.MarkResult(ctx, Result{
		AuthID:               auth.ID,
		Provider:             "claude",
		Model:                "claude-opus-4-6",
		Success:              true,
		SkipQuotaObservation: true,
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found after MarkResult")
	}
	if updated.Quota.Signals["Anthropic-Ratelimit-Unified-Status"] != "allowed" || !updated.Quota.ObservedAt.Equal(time.Unix(10, 0)) {
		t.Fatalf("count_tokens replaced the last generation snapshot: %#v", updated.Quota)
	}
}

func TestResetModelStatePreservesObservationSignals(t *testing.T) {
	state := &ModelState{
		Status:        StatusError,
		Unavailable:   true,
		StatusMessage: "quota",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: time.Unix(20, 0),
			BackoffLevel:  2,
			ObservedAt:    time.Unix(10, 0),
			Signals:       map[string]string{"X-Codex-Active-Limit": "premium"},
		},
	}
	resetModelState(state, time.Unix(30, 0))
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("cooldown state was not reset: %#v", state.Quota)
	}
	if !state.Quota.ObservedAt.Equal(time.Unix(10, 0)) || state.Quota.Signals["X-Codex-Active-Limit"] != "premium" {
		t.Fatalf("observation signals were lost during reset: %#v", state.Quota)
	}
}

func TestMergeModelStateKeepsNewestObservationSnapshot(t *testing.T) {
	target := &ModelState{UpdatedAt: time.Unix(20, 0), Quota: QuotaState{
		ObservedAt: time.Unix(20, 0), Signals: map[string]string{"X-Codex-Plan-Type": "pro"},
	}}
	source := &ModelState{UpdatedAt: time.Unix(30, 0), Quota: QuotaState{
		ObservedAt: time.Unix(30, 0), Signals: map[string]string{"X-Codex-Active-Limit": "codex_bengalfox"},
	}}
	mergeModelState(target, source)
	if !target.Quota.ObservedAt.Equal(time.Unix(30, 0)) {
		t.Fatalf("merged ObservedAt = %v, want newest snapshot time", target.Quota.ObservedAt)
	}
	if target.Quota.Signals["X-Codex-Active-Limit"] != "codex_bengalfox" {
		t.Fatalf("newest snapshot was lost: %#v", target.Quota.Signals)
	}
	// Unioning snapshots taken at different times would resurrect the older
	// watermark, so the stale key must be gone.
	if _, ok := target.Quota.Signals["X-Codex-Plan-Type"]; ok {
		t.Fatalf("stale snapshot key survived the merge: %#v", target.Quota.Signals)
	}
}

// A watermark such as Retry-After only appears on the response that produced it.
// Later responses must not keep advertising it.
func TestObserveResponseHeadersReplacesStaleWatermarks(t *testing.T) {
	var quota QuotaState
	if !quota.ObserveResponseHeadersForProvider("codex", http.Header{
		"Retry-After":                  []string{"120"},
		"X-Codex-Primary-Used-Percent": []string{"99"},
	}, time.Unix(100, 0)) {
		t.Fatal("initial observation reported no change")
	}
	if quota.Signals["Retry-After"] != "120" {
		t.Fatalf("initial snapshot = %#v", quota.Signals)
	}

	if !quota.ObserveResponseHeadersForProvider("codex", http.Header{
		"X-Codex-Primary-Used-Percent": []string{"5"},
	}, time.Unix(200, 0)) {
		t.Fatal("second observation reported no change")
	}
	if _, ok := quota.Signals["Retry-After"]; ok {
		t.Fatalf("expired Retry-After survived a later response: %#v", quota.Signals)
	}
	if quota.Signals["X-Codex-Primary-Used-Percent"] != "5" {
		t.Fatalf("snapshot not refreshed: %#v", quota.Signals)
	}
	if !quota.ObservedAt.Equal(time.Unix(200, 0)) {
		t.Fatalf("ObservedAt = %v, want the latest observation time", quota.ObservedAt)
	}
}

// Responses without any quota header (transport failures, 5xx) must not erase
// the last known snapshot.
func TestObserveResponseHeadersKeepsSnapshotWhenResponseCarriesNoSignal(t *testing.T) {
	quota := QuotaState{
		ObservedAt: time.Unix(100, 0),
		Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "5"},
	}
	if quota.ObserveResponseHeadersForProvider("codex", http.Header{
		"Content-Type": []string{"application/json"},
	}, time.Unix(200, 0)) {
		t.Fatal("signal-free response reported a change")
	}
	if quota.Signals["X-Codex-Primary-Used-Percent"] != "5" || !quota.ObservedAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("snapshot was disturbed: %#v", quota)
	}
}

// ObservedAt must advance even when the watermark values are unchanged,
// otherwise consumers cannot tell a fresh reading from a stale one.
func TestObserveResponseHeadersAdvancesObservedAtOnRepeatedValues(t *testing.T) {
	var quota QuotaState
	headers := http.Header{"X-Codex-Primary-Used-Percent": []string{"5"}}
	quota.ObserveResponseHeadersForProvider("codex", headers, time.Unix(100, 0))
	quota.ObserveResponseHeadersForProvider("codex", headers, time.Unix(200, 0))
	if !quota.ObservedAt.Equal(time.Unix(200, 0)) {
		t.Fatalf("ObservedAt = %v, want 200", quota.ObservedAt)
	}
}

// Observed values reach the plain-text upstream request log, so control
// characters must never be stored.
func TestObserveResponseHeadersRejectsControlCharacterValues(t *testing.T) {
	var quota QuotaState
	if quota.ObserveResponseHeadersForProvider("codex", http.Header{
		"X-Codex-Bengalfox-Limit-Name": []string{"evil\r\nX-Injected: 1"},
	}, time.Unix(100, 0)) {
		t.Fatal("control-character value was accepted")
	}
	if len(quota.Signals) != 0 {
		t.Fatalf("control-character value was stored: %#v", quota.Signals)
	}
}

// Truncation at the header cap must not depend on map iteration order.
func TestObserveResponseHeadersTruncatesDeterministically(t *testing.T) {
	headers := make(http.Header, maxQuotaSignalHeaders*2)
	for i := 0; i < maxQuotaSignalHeaders*2; i++ {
		headers.Set(fmt.Sprintf("X-Codex-L%03d-Primary-Used-Percent", i), strconv.Itoa(i))
	}
	var first QuotaState
	first.ObserveResponseHeadersForProvider("codex", headers, time.Unix(100, 0))
	if len(first.Signals) != maxQuotaSignalHeaders {
		t.Fatalf("snapshot size = %d, want %d", len(first.Signals), maxQuotaSignalHeaders)
	}
	for attempt := 0; attempt < 5; attempt++ {
		var next QuotaState
		next.ObserveResponseHeadersForProvider("codex", headers, time.Unix(100, 0))
		if !reflect.DeepEqual(first.Signals, next.Signals) {
			t.Fatal("truncated snapshot varied between identical observations")
		}
	}
}

func TestProviderSupportsQuotaObservation(t *testing.T) {
	for _, provider := range []string{
		"", "kimi", "xai", "grok", "antigravity", "gemini", "gemini-interactions",
		"vertex", "aistudio", "openai", "openai-compatibility", "third-party-plugin",
		"XAI", " Grok ", " Gemini ",
	} {
		if ProviderSupportsQuotaObservation(provider) {
			t.Fatalf("provider %q unexpectedly supports quota observation", provider)
		}
	}
	for _, provider := range []string{"codex", "claude", "CODEX", " Claude "} {
		if !ProviderSupportsQuotaObservation(provider) {
			t.Fatalf("provider %q unexpectedly excluded from quota observation", provider)
		}
	}
}

func TestQuotaStateCloneCopiesSignals(t *testing.T) {
	original := QuotaState{Signals: map[string]string{"X-Codex-Plan-Type": "pro"}}
	clone := original.Clone()
	clone.Signals["X-Codex-Plan-Type"] = "team"
	if original.Signals["X-Codex-Plan-Type"] != "pro" {
		t.Fatalf("mutating cloned quota changed original: %#v", original.Signals)
	}
}

func TestApplyCooldownFieldsPreservesObservation(t *testing.T) {
	quota := QuotaState{
		Exceeded:      true,
		Reason:        "quota",
		NextRecoverAt: time.Unix(20, 0),
		BackoffLevel:  1,
		ObservedAt:    time.Unix(10, 0),
		Signals:       map[string]string{"X-Codex-Primary-Used-Percent": "51"},
	}
	applyCooldownFields(&quota, QuotaState{
		Exceeded:      true,
		Reason:        "credential_quota",
		NextRecoverAt: time.Unix(40, 0),
		BackoffLevel:  2,
	})
	if !quota.Exceeded || quota.Reason != "credential_quota" || quota.BackoffLevel != 2 || !quota.NextRecoverAt.Equal(time.Unix(40, 0)) {
		t.Fatalf("cooldown fields were not applied: %#v", quota)
	}
	if !quota.ObservedAt.Equal(time.Unix(10, 0)) || quota.Signals["X-Codex-Primary-Used-Percent"] != "51" {
		t.Fatalf("cooldown overwrote the last observation: %#v", quota)
	}
}

func TestClearCooldownStateForAuthPreservesObservation(t *testing.T) {
	auth := &Auth{
		Unavailable:    true,
		NextRetryAfter: time.Unix(40, 0),
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: time.Unix(40, 0),
			ObservedAt:    time.Unix(10, 0),
			Signals:       map[string]string{"X-Codex-Primary-Used-Percent": "51"},
		},
		ModelStates: map[string]*ModelState{
			"gpt-5.3-codex": {
				Unavailable:    true,
				NextRetryAfter: time.Unix(40, 0),
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: time.Unix(40, 0),
					ObservedAt:    time.Unix(11, 0),
					Signals:       map[string]string{"X-Codex-Plan-Type": "pro"},
				},
			},
		},
	}
	if !clearCooldownStateForAuth(auth, time.Unix(50, 0)) {
		t.Fatal("clearCooldownStateForAuth reported no change")
	}
	if auth.Unavailable || auth.Quota.Exceeded || auth.Quota.Reason != "" {
		t.Fatalf("cooldown was not cleared: %#v", auth.Quota)
	}
	if !auth.Quota.ObservedAt.Equal(time.Unix(10, 0)) || auth.Quota.Signals["X-Codex-Primary-Used-Percent"] != "51" {
		t.Fatalf("clearing cooldown overwrote auth observation: %#v", auth.Quota)
	}
	state := auth.ModelStates["gpt-5.3-codex"]
	if state.Unavailable || state.Quota.Exceeded {
		t.Fatalf("model cooldown was not cleared: %#v", state.Quota)
	}
	if !state.Quota.ObservedAt.Equal(time.Unix(11, 0)) || state.Quota.Signals["X-Codex-Plan-Type"] != "pro" {
		t.Fatalf("clearing cooldown overwrote model observation: %#v", state.Quota)
	}
}

func TestCooldownStateRecordOmitsObservation(t *testing.T) {
	now := time.Unix(50, 0)
	auth := &Auth{
		ID:             "auth-1",
		Unavailable:    true,
		NextRetryAfter: now.Add(time.Hour),
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: now.Add(time.Hour),
			BackoffLevel:  2,
			ObservedAt:    time.Unix(10, 0),
			Signals:       map[string]string{"X-Codex-Primary-Used-Percent": "51"},
		},
	}
	record, ok := authCooldownStateRecord(auth, now)
	if !ok {
		t.Fatal("expected a cooldown record")
	}
	if !record.Quota.Exceeded || record.Quota.Reason != "quota" || record.Quota.BackoffLevel != 2 {
		t.Fatalf("cooldown fields missing from record: %#v", record.Quota)
	}
	if !record.Quota.ObservedAt.IsZero() || len(record.Quota.Signals) != 0 {
		t.Fatalf("observation leaked into cooldown persistence: %#v", record.Quota)
	}
}

func TestMarkResultQuotaFailureDoesNotEraseSiblingObservation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "quota-sibling-auth",
		Provider: "codex",
		ModelStates: map[string]*ModelState{
			"gpt-5.3-codex": {
				Status: StatusActive,
				Quota: QuotaState{
					ObservedAt: time.Unix(10, 0),
					Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "40"},
				},
			},
			"gpt-5.4": {
				Status: StatusActive,
				Quota: QuotaState{
					ObservedAt: time.Unix(11, 0),
					Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "41"},
				},
			},
		},
	})
	if errRegister != nil || auth == nil {
		t.Fatalf("Register() auth=%#v err=%v", auth, errRegister)
	}

	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Retry-After":                  []string{"120"},
		"X-Codex-Primary-Used-Percent": []string{"99"},
	})
	manager.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        "codex",
		Model:           "gpt-5.3-codex",
		Success:         false,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: 429, Message: "quota"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found after MarkResult")
	}
	current := updated.ModelStates["gpt-5.3-codex"]
	if current == nil || !current.Quota.Exceeded || current.Quota.Reason != "quota" {
		t.Fatalf("current model cooldown missing: %#v", current)
	}
	if current.Quota.Signals["X-Codex-Primary-Used-Percent"] != "99" || current.Quota.Signals["Retry-After"] != "120" {
		t.Fatalf("current model observation was not refreshed: %#v", current.Quota.Signals)
	}
	sibling := updated.ModelStates["gpt-5.4"]
	if sibling == nil || !sibling.Quota.Exceeded || sibling.Quota.Reason != "credential_quota" {
		t.Fatalf("sibling cooldown missing: %#v", sibling)
	}
	if sibling.Quota.Signals["X-Codex-Primary-Used-Percent"] != "41" {
		t.Fatalf("credential-scope cooldown erased sibling observation: %#v", sibling.Quota.Signals)
	}
	if _, ok := sibling.Quota.Signals["Retry-After"]; ok {
		t.Fatalf("sibling observation was replaced by the current request: %#v", sibling.Quota.Signals)
	}
}

func TestMarkResultRetainedCredentialQuotaStillObservesModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	recoverAt := time.Now().Add(time.Hour)
	auth, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "quota-retain-auth",
		Provider: "codex",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: recoverAt,
			ObservedAt:    time.Unix(10, 0),
			Signals:       map[string]string{"X-Codex-Primary-Used-Percent": "10"},
		},
		ModelStates: map[string]*ModelState{
			"gpt-5.3-codex": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: recoverAt,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "credential_quota",
					NextRecoverAt: recoverAt,
					ObservedAt:    time.Unix(10, 0),
					Signals:       map[string]string{"X-Codex-Primary-Used-Percent": "10"},
				},
			},
		},
	})
	if errRegister != nil || auth == nil {
		t.Fatalf("Register() auth=%#v err=%v", auth, errRegister)
	}

	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"X-Codex-Primary-Used-Percent": []string{"20"},
	})
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    "gpt-5.3-codex",
		Success:  true,
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found after MarkResult")
	}
	if !updated.Quota.Exceeded || updated.Quota.Reason != "credential_quota" {
		t.Fatalf("retained cooldown was disturbed: %#v", updated.Quota)
	}
	if updated.Quota.Signals["X-Codex-Primary-Used-Percent"] != "20" {
		t.Fatalf("auth observation was not refreshed: %#v", updated.Quota.Signals)
	}
	state := updated.ModelStates["gpt-5.3-codex"]
	if state == nil || !state.Quota.Exceeded || state.Quota.Reason != "credential_quota" {
		t.Fatalf("model cooldown was disturbed: %#v", state)
	}
	if state.Quota.Signals["X-Codex-Primary-Used-Percent"] != "20" {
		t.Fatalf("retained cooldown prevented model observation: %#v", state.Quota.Signals)
	}
}

func TestRestoreCooldownRecordDoesNotOverwriteNewerObservation(t *testing.T) {
	nextRetry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	store := &recordingCooldownStateStore{
		load: []CooldownStateRecord{{
			Provider:       "codex",
			AuthID:         "auth-restore-obs",
			Status:         "cooling",
			NextRetryAfter: nextRetry,
			Reason:         "quota",
			Quota: QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: nextRetry,
				ObservedAt:    time.Unix(5, 0),
				Signals:       map[string]string{"X-Codex-Primary-Used-Percent": "1"},
			},
			UpdatedAt: nextRetry.Add(-time.Minute),
		}},
	}
	manager := NewManager(nil, nil, nil)
	manager.SetCooldownStateStore(store)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       "auth-restore-obs",
		Provider: "codex",
		Quota: QuotaState{
			ObservedAt: time.Unix(50, 0),
			Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "77"},
		},
	}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() returned error: %v", errRestore)
	}
	auth, ok := manager.GetByID("auth-restore-obs")
	if !ok || auth == nil {
		t.Fatal("restored auth was not found")
	}
	if !auth.Unavailable || !auth.Quota.Exceeded || auth.Quota.Reason != "quota" {
		t.Fatalf("cooldown was not restored: %#v", auth.Quota)
	}
	if auth.Quota.Signals["X-Codex-Primary-Used-Percent"] != "77" {
		t.Fatalf("restoring cooldown overwrote a newer observation: %#v", auth.Quota.Signals)
	}
}

func TestObserveResponseHeadersKeepsPrimaryWhenTruncatingAdditional(t *testing.T) {
	headers := make(http.Header, maxQuotaSignalHeaders+8)
	headers.Set("X-Codex-Plan-Type", "pro")
	headers.Set("X-Codex-Primary-Used-Percent", "81")
	headers.Set("X-Codex-Credits-Balance", "0")
	for i := 0; i < maxQuotaSignalHeaders; i++ {
		headers.Set(fmt.Sprintf("X-Codex-Additional-L%03d-Primary-Used-Percent", i), strconv.Itoa(i))
	}
	var quota QuotaState
	quota.ObserveResponseHeadersForProvider("codex", headers, time.Unix(100, 0))
	if quota.Signals["X-Codex-Plan-Type"] != "pro" ||
		quota.Signals["X-Codex-Primary-Used-Percent"] != "81" ||
		quota.Signals["X-Codex-Credits-Balance"] != "0" {
		t.Fatalf("credential-level watermarks were truncated: %#v", quota.Signals)
	}
	if len(quota.Signals) != maxQuotaSignalHeaders {
		t.Fatalf("snapshot size = %d, want %d", len(quota.Signals), maxQuotaSignalHeaders)
	}
}
