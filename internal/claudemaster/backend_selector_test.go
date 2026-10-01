package claudemaster

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func backendSeriesTestAuth(id, provider string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: provider, Status: coreauth.StatusActive}
}

func backendSeriesQuotaResult(authID string) coreauth.Result {
	return coreauth.Result{
		AuthID:          authID,
		Provider:        "claude",
		CredentialScope: true,
		Error: &coreauth.Error{
			HTTPStatus: http.StatusTooManyRequests,
		},
	}
}

func requireBackendSeriesPick(t *testing.T, selector *backendSeriesSelector, provider string, auths []*coreauth.Auth, wantID string) {
	t.Helper()
	got, err := selector.Pick(t.Context(), provider, "", coreexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got == nil || got.ID != wantID {
		t.Fatalf("Pick() = %#v, want auth %q", got, wantID)
	}
}

func TestBackendSelectorAcceptsMixedDispatchButPinsActualCredential(t *testing.T) {
	selector := &backendSelector{authID: "selected", provider: "claude"}
	selected := &coreauth.Auth{ID: "selected", Provider: "claude", Status: coreauth.StatusActive}
	other := &coreauth.Auth{ID: "other", Provider: "claude", Status: coreauth.StatusActive}
	wrongProvider := &coreauth.Auth{ID: "selected", Provider: "codex", Status: coreauth.StatusActive}
	for _, dispatch := range []string{"claude", "mixed"} {
		t.Run(dispatch, func(t *testing.T) {
			got, err := selector.Pick(t.Context(), dispatch, "", coreexecutor.Options{}, []*coreauth.Auth{other, wrongProvider, selected})
			if err != nil || got == nil || got.ID != selected.ID || got.Provider != selected.Provider {
				t.Fatalf("pinned account rejected: %v", err)
			}
			for _, candidates := range [][]*coreauth.Auth{nil, {other}, {wrongProvider}, {nil, other, wrongProvider}} {
				if got, err := selector.Pick(t.Context(), dispatch, "", coreexecutor.Options{}, candidates); err == nil || got != nil {
					t.Fatal("unselected account or provider was accepted")
				}
			}
		})
	}
	if got, err := selector.Pick(t.Context(), "codex", "", coreexecutor.Options{}, []*coreauth.Auth{selected}); err == nil || got != nil {
		t.Fatal("unselected dispatcher provider was accepted")
	}
}

func TestBackendSelectorUsesRequestedModelForDynamicCooldown(t *testing.T) {
	const (
		authID       = "selected"
		limitedModel = "claude-limited"
		otherModel   = "claude-available"
	)
	selector := &backendSelector{authID: authID, provider: "claude"}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   coreauth.StatusActive,
		ModelStates: map[string]*coreauth.ModelState{
			limitedModel: {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(time.Hour),
			},
		},
	}
	opts := coreexecutor.Options{Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: limitedModel}}
	if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, opts, []*coreauth.Auth{auth}); err == nil || got != nil {
		t.Fatalf("Pick() for cooling requested model = %#v, %v; want cooldown", got, err)
	}

	opts.Metadata[coreexecutor.RequestedModelMetadataKey] = otherModel
	got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, opts, []*coreauth.Auth{auth})
	if err != nil {
		t.Fatalf("Pick() for other requested model: %v", err)
	}
	if got == nil || got.ID != authID {
		t.Fatalf("Pick() for other requested model = %#v, want %q", got, authID)
	}
}

func TestBackendSeriesSelectorPreservesExplicitOrderWhileQuotaIsUnknown(t *testing.T) {
	selector := &backendSeriesSelector{
		authIDs:  []string{"profile-c", "profile-a", "profile-b"},
		provider: "claude",
	}
	t.Cleanup(selector.Stop)
	auths := []*coreauth.Auth{
		backendSeriesTestAuth("profile-a", "claude"),
		backendSeriesTestAuth("profile-b", "claude"),
		backendSeriesTestAuth("profile-c", "claude"),
	}

	requireBackendSeriesPick(t, selector, "claude", auths, "profile-c")
	if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{}, auths[:2]); err == nil || got != nil {
		t.Fatalf("Pick() without preferred auth = %#v, %v; want no model-scoped fallback", got, err)
	}

	selector.OnResult(backendSeriesQuotaResult("profile-c"))
	requireBackendSeriesPick(t, selector, "mixed", auths, "profile-a")
	selector.OnResult(backendSeriesQuotaResult("profile-a"))
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-b")

	disabledCurrent := []*coreauth.Auth{
		backendSeriesTestAuth("profile-a", "claude"),
		backendSeriesTestAuth("profile-b", "claude"),
		backendSeriesTestAuth("profile-c", "claude"),
	}
	disabledCurrent[1].Status = coreauth.StatusDisabled
	if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{}, disabledCurrent); err == nil || got != nil {
		t.Fatalf("Pick() with unavailable current auth = %#v, %v; want no fallback", got, err)
	}
}

func TestBackendSeriesSelectorAdvancesOnlyForCredentialScoped429(t *testing.T) {
	tests := []struct {
		name   string
		result coreauth.Result
	}{
		{
			name: "request scoped",
			result: coreauth.Result{
				AuthID:          "profile-a",
				CredentialScope: true,
				Error: &coreauth.Error{
					Code:       coreauth.ErrorCodeRequestScoped,
					HTTPStatus: http.StatusTooManyRequests,
				},
			},
		},
		{
			name: "request scoped with forced cooldown",
			result: coreauth.Result{
				AuthID:          "profile-a",
				CredentialScope: true,
				Error: &coreauth.Error{
					Code:       coreauth.ErrorCodeForceCooldown,
					HTTPStatus: http.StatusTooManyRequests,
				},
			},
		},
		{
			name: "ordinary model 429",
			result: coreauth.Result{
				AuthID: "profile-a",
				Error:  &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
			},
		},
		{
			name: "success",
			result: coreauth.Result{
				AuthID:          "profile-a",
				Success:         true,
				CredentialScope: true,
				Error:           &coreauth.Error{HTTPStatus: http.StatusTooManyRequests},
			},
		},
		{
			name: "server error",
			result: coreauth.Result{
				AuthID:          "profile-a",
				CredentialScope: true,
				Error:           &coreauth.Error{HTTPStatus: http.StatusServiceUnavailable},
			},
		},
	}
	auths := []*coreauth.Auth{
		backendSeriesTestAuth("profile-a", "claude"),
		backendSeriesTestAuth("profile-b", "claude"),
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
			t.Cleanup(selector.Stop)
			selector.OnResult(test.result)
			requireBackendSeriesPick(t, selector, "claude", auths, "profile-a")
		})
	}

	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	selector.OnResult(backendSeriesQuotaResult("profile-a"))
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-b")

	countSelector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(countSelector.Stop)
	countResult := backendSeriesQuotaResult("profile-a")
	countResult.SkipQuotaObservation = true
	countSelector.OnResult(countResult)
	requireBackendSeriesPick(t, countSelector, "claude", auths, "profile-b")
}

func TestBackendSeriesSelectorStaleDuplicateResultsCannotSkip(t *testing.T) {
	selector := &backendSeriesSelector{
		authIDs:  []string{"profile-a", "profile-b", "profile-c"},
		provider: "claude",
	}
	t.Cleanup(selector.Stop)
	auths := []*coreauth.Auth{
		backendSeriesTestAuth("profile-a", "claude"),
		backendSeriesTestAuth("profile-b", "claude"),
		backendSeriesTestAuth("profile-c", "claude"),
	}

	selector.OnResult(backendSeriesQuotaResult("profile-a"))
	selector.OnResult(backendSeriesQuotaResult("profile-a"))
	selector.OnResult(backendSeriesQuotaResult("profile-a"))
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-b")

	selector.OnResult(backendSeriesQuotaResult("profile-b"))
	selector.OnResult(backendSeriesQuotaResult("profile-b"))
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-c")
}

func TestBackendSeriesSelectorConcurrentDuplicateResultsAdvanceOnce(t *testing.T) {
	selector := &backendSeriesSelector{
		authIDs:  []string{"profile-a", "profile-b", "profile-c"},
		provider: "claude",
	}
	t.Cleanup(selector.Stop)
	result := backendSeriesQuotaResult("profile-a")
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			selector.OnResult(result)
		}()
	}
	group.Wait()
	requireBackendSeriesPick(t, selector, "claude", []*coreauth.Auth{
		backendSeriesTestAuth("profile-a", "claude"),
		backendSeriesTestAuth("profile-b", "claude"),
		backendSeriesTestAuth("profile-c", "claude"),
	}, "profile-b")
}

func TestBackendSeriesSelectorFinalExhaustionNeverWraps(t *testing.T) {
	selector := &backendSeriesSelector{
		authIDs:  []string{"profile-a", "profile-b"},
		provider: "claude",
	}
	t.Cleanup(selector.Stop)
	auths := []*coreauth.Auth{
		backendSeriesTestAuth("profile-a", "claude"),
		backendSeriesTestAuth("profile-b", "claude"),
	}

	selector.OnResult(backendSeriesQuotaResult("profile-a"))
	selector.OnResult(backendSeriesQuotaResult("profile-b"))
	for i := 0; i < 2; i++ {
		if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{}, auths); err == nil || got != nil {
			t.Fatalf("Pick() after exhaustion = %#v, %v; want terminal exhaustion", got, err)
		}
		selector.OnResult(backendSeriesQuotaResult("profile-b"))
		selector.OnResult(backendSeriesQuotaResult("profile-a"))
	}
}

func TestBackendSeriesSelectorRejectsWrongProvider(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	claudeAuth := backendSeriesTestAuth("profile-a", "claude")
	if got, err := selector.Pick(t.Context(), "codex", "", coreexecutor.Options{}, []*coreauth.Auth{claudeAuth}); err == nil || got != nil {
		t.Fatalf("Pick() for wrong dispatch provider = %#v, %v; want rejection", got, err)
	}

	wrongProviderAuth := backendSeriesTestAuth("profile-a", "codex")
	if got, err := selector.Pick(t.Context(), "mixed", "", coreexecutor.Options{}, []*coreauth.Auth{wrongProviderAuth}); err == nil || got != nil {
		t.Fatalf("Pick() for wrong credential provider = %#v, %v; want rejection", got, err)
	}
}

func TestBackendSeriesSelectorUsesSoonestWeeklyReset(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b", "profile-c"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	resetSoon := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return resetSoon.Add(-time.Hour) }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.2, resetsAt: resetSoon.Add(48 * time.Hour)})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.4, resetsAt: resetSoon})

	requireBackendSeriesPick(t, selector, "claude", []*coreauth.Auth{
		backendSeriesTestAuth("profile-c", "claude"),
		backendSeriesTestAuth("profile-a", "claude"),
		backendSeriesTestAuth("profile-b", "claude"),
	}, "profile-b")
}

func TestBackendSeriesSelectorReopensQuotaAtWeeklyReset(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	now := reset.Add(-time.Minute)
	selector.now = func() time.Time { return now }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.25, resetsAt: reset.Add(9 * 24 * time.Hour)})
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-b")

	now = reset
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-a")
	selector.mu.Lock()
	quota := selector.quota["profile-a"]
	selector.mu.Unlock()
	if quota.used != 0 || !quota.resetsAt.Equal(reset.Add(7*24*time.Hour)) {
		t.Fatalf("quota after reset = %+v, want zero usage and next weekly reset", quota)
	}
}

func TestBackendSeriesSelectorReservesTenPercentForContinuation(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.89, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(24 * time.Hour)})
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}
	plain := coreexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"session-1"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	got, err := selector.Pick(t.Context(), "claude", "", plain, auths)
	if err != nil || got.ID != "profile-a" {
		t.Fatalf("initial Pick() = %#v, %v, want profile-a", got, err)
	}

	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.90, resetsAt: reset})
	signed := plain
	signed.Headers = plain.Headers.Clone()
	signed.Headers.Set("X-CC-Context-Compacted", "manual")
	signed.OriginalRequest = []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"opaque"}]}]}`)
	got, err = selector.Pick(t.Context(), "claude", "", signed, auths)
	if err != nil || got.ID != "profile-a" {
		t.Fatalf("continuation Pick() = %#v, %v, want reserved profile-a", got, err)
	}

	got, err = selector.Pick(t.Context(), "claude", "", plain, auths)
	if err != nil || got.ID != "profile-b" {
		t.Fatalf("clean Pick() = %#v, %v, want profile-b after reserve threshold", got, err)
	}
}

func TestBackendSeriesSelectorDoesNotMoveOpaqueStateWhenBoundAuthUnavailable(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	authA := backendSeriesTestAuth("profile-a", "claude")
	authB := backendSeriesTestAuth("profile-b", "claude")
	headers := http.Header{"X-Claude-Code-Session-Id": []string{"session-opaque"}}
	if _, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{
		Headers: headers, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"start"}]}`),
	}, []*coreauth.Auth{authA, authB}); err != nil {
		t.Fatal(err)
	}
	got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{
		Headers: headers, OriginalRequest: []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"opaque"}]}]}`),
	}, []*coreauth.Auth{authB})
	if err == nil || got != nil {
		t.Fatalf("opaque continuation moved accounts: got %#v, err %v", got, err)
	}
}

func TestBackendSeriesSelectorCanMoveOrdinaryToolHistory(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	authA := backendSeriesTestAuth("profile-a", "claude")
	authB := backendSeriesTestAuth("profile-b", "claude")
	headers := http.Header{"X-Claude-Code-Session-Id": []string{"session-tool"}}
	if _, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{
		Headers: headers, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"start"}]}`),
	}, []*coreauth.Auth{authA, authB}); err != nil {
		t.Fatal(err)
	}
	got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{
		Headers: headers, OriginalRequest: []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}]}`),
	}, []*coreauth.Auth{authB})
	if err != nil || got == nil || got.ID != "profile-b" {
		t.Fatalf("self-contained tool continuation Pick() = %#v, %v, want profile-b", got, err)
	}
}

func TestBackendSeriesSelectorUnclassifiablePayloadStaysBound(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.89, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(time.Hour)})
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}
	headers := http.Header{"X-Claude-Code-Session-Id": []string{"session-invalid"}}
	if _, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{
		Headers: headers, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"start"}]}`),
	}, auths); err != nil {
		t.Fatal(err)
	}
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.90, resetsAt: reset})
	got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{Headers: headers, OriginalRequest: []byte(`{`)}, auths)
	if err != nil || got == nil || got.ID != "profile-a" {
		t.Fatalf("unclassifiable continuation Pick() = %#v, %v, want bound profile-a", got, err)
	}
}

func TestBackendSeriesSelectorSubagentUsesParentBindingForOpaqueState(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}
	parentHeaders := http.Header{"X-Claude-Code-Session-Id": []string{"session-parent"}}
	if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{
		Headers: parentHeaders, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"start"}]}`),
	}, auths); err != nil || got.ID != "profile-a" {
		t.Fatalf("parent Pick() = %#v, %v, want profile-a", got, err)
	}
	childHeaders := parentHeaders.Clone()
	childHeaders.Set("X-Claude-Code-Agent-Id", "agent-reviewer")
	childHeaders.Set("X-Claude-Code-Parent-Agent-Id", "main")
	got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{
		Headers:         childHeaders,
		OriginalRequest: []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"opaque"}]}]}`),
	}, auths)
	if err != nil || got == nil || got.ID != "profile-a" {
		t.Fatalf("opaque subagent Pick() = %#v, %v, want parent's profile-a", got, err)
	}
}

func TestBackendSeriesSelectorCanReselectFromSelfContainedPayloadWithoutCompactionSignal(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(24 * time.Hour)})
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}
	headers := http.Header{"X-Claude-Code-Session-Id": []string{"session-compact"}}
	require := func(opts coreexecutor.Options, want string) {
		t.Helper()
		got, err := selector.Pick(t.Context(), "claude", "", opts, auths)
		if err != nil || got.ID != want {
			t.Fatalf("Pick() = %#v, %v, want %s", got, err, want)
		}
	}
	require(coreexecutor.Options{Headers: headers, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"start"}]}`)}, "profile-a")
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.90, resetsAt: reset.Add(48 * time.Hour)})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	require(coreexecutor.Options{Headers: headers, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"self-contained continuation"}]}`)}, "profile-b")
}

func TestBackendSeriesSelectorSelfContainedSubagentStartsOwnBinding(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(2 * time.Hour)})
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}
	parentHeaders := http.Header{"X-Claude-Code-Session-Id": []string{"session-parent"}}
	if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{
		Headers: parentHeaders, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"start"}]}`),
	}, auths); err != nil || got.ID != "profile-a" {
		t.Fatalf("parent Pick() = %#v, %v, want profile-a", got, err)
	}

	// The preferred account changes before this independent child begins.
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.3, resetsAt: reset})
	childHeaders := parentHeaders.Clone()
	childHeaders.Set("X-Claude-Code-Agent-Id", "agent-reviewer")
	childHeaders.Set("X-Claude-Code-Parent-Agent-Id", "main")
	childOpts := coreexecutor.Options{
		Headers: childHeaders, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"review independently"}]}`),
	}
	got, err := selector.Pick(t.Context(), "claude", "", childOpts, auths)
	if err != nil || got == nil || got.ID != "profile-b" {
		t.Fatalf("self-contained child Pick() = %#v, %v, want profile-b", got, err)
	}
	childID, _ := backendSeriesSessionIDs(childOpts)
	if bound, ok := selector.sessions.Get(childID); !ok || bound != "profile-b" {
		t.Fatalf("child binding = %q, %v, want profile-b", bound, ok)
	}
}

func TestBackendSeriesSelectorReserveMoveUsesAvailableAlternative(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-c", "profile-b"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset.Add(2 * time.Hour)})
	authA := backendSeriesTestAuth("profile-a", "claude")
	authB := backendSeriesTestAuth("profile-b", "claude")
	opts := coreexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"session-reserve"}},
		OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	if got, err := selector.Pick(t.Context(), "claude", "", opts, []*coreauth.Auth{authA, authB}); err != nil || got.ID != "profile-a" {
		t.Fatalf("initial Pick() = %#v, %v, want profile-a", got, err)
	}
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.9, resetsAt: reset.Add(4 * time.Hour)})
	selector.observeQuota("profile-c", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	got, err := selector.Pick(t.Context(), "claude", "", opts, []*coreauth.Auth{authA, authB})
	if err != nil || got == nil || got.ID != "profile-b" {
		t.Fatalf("reserve Pick() = %#v, %v, want available profile-b", got, err)
	}
}

func TestBackendSeriesSelectorQuotaObservationsDoNotRegress(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.92, resetsAt: reset})
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.30, resetsAt: reset})
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.99, resetsAt: reset.Add(-time.Hour)})
	selector.mu.Lock()
	quota := selector.currentQuotaLocked("profile-a")
	selector.mu.Unlock()
	if quota.used != 0.92 || !quota.resetsAt.Equal(reset) {
		t.Fatalf("quota regressed to %+v, want 0.92 at %v", quota, reset)
	}
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.05, resetsAt: reset.Add(7 * 24 * time.Hour)})
	selector.mu.Lock()
	quota = selector.currentQuotaLocked("profile-a")
	selector.mu.Unlock()
	if quota.used != 0.05 || !quota.resetsAt.Equal(reset.Add(7*24*time.Hour)) {
		t.Fatalf("next-window quota = %+v", quota)
	}
}

func TestBackendSeriesResultPolicySkipsCountQuotaHeaders(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude"}
	t.Cleanup(selector.Stop)
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.now = func() time.Time { return reset.Add(-time.Hour) }
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	manager := coreauth.NewManager(nil, selector, nil)
	setBackendSeriesResultPolicy(manager, selector)
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.85"},
		"Anthropic-Ratelimit-Unified-7d-Reset":       []string{reset.Format(time.RFC3339)},
	})
	manager.ResultPolicy().ApplyResultPolicy(ctx, coreauth.Result{AuthID: "profile-a", Success: true, SkipQuotaObservation: true})
	selector.mu.Lock()
	quota := selector.currentQuotaLocked("profile-a")
	selector.mu.Unlock()
	if quota.used != 0.2 {
		t.Fatalf("count response changed quota to %v", quota.used)
	}
	manager.ResultPolicy().ApplyResultPolicy(ctx, coreauth.Result{AuthID: "profile-a", Success: true})
	selector.mu.Lock()
	quota = selector.currentQuotaLocked("profile-a")
	selector.mu.Unlock()
	if quota.used != 0.85 {
		t.Fatalf("generation response quota = %v, want 0.85", quota.used)
	}
}

func TestBackendSeriesSelectorStopIsTerminalDuringPick(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude"}
	reset := time.Date(2026, time.October, 2, 0, 0, 0, 0, time.UTC)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	selector.now = func() time.Time {
		once.Do(func() { close(entered) })
		<-release
		return reset.Add(-time.Hour)
	}
	pickDone := make(chan error, 1)
	go func() {
		_, err := selector.Pick(context.Background(), "claude", "", coreexecutor.Options{}, []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude")})
		pickDone <- err
	}()
	<-entered
	stopDone := make(chan struct{})
	go func() {
		selector.Stop()
		close(stopDone)
	}()
	close(release)
	if err := <-pickDone; err != nil {
		t.Fatalf("in-flight Pick() error = %v", err)
	}
	<-stopDone
	selector.mu.Lock()
	stopped, sessions := selector.stopped, selector.sessions
	selector.mu.Unlock()
	if !stopped || sessions != nil {
		t.Fatalf("selector after Stop = stopped %v, sessions %#v", stopped, sessions)
	}
	if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{}, []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude")}); err == nil || got != nil {
		t.Fatalf("Pick() after Stop = %#v, %v", got, err)
	}
	selector.mu.Lock()
	sessions = selector.sessions
	selector.mu.Unlock()
	if sessions != nil {
		t.Fatal("Pick() after Stop recreated the session cache")
	}
}
