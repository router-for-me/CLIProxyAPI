package claudemaster

import (
	"net/http"
	"sync"
	"testing"
	"time"

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

func TestBackendSeriesSelectorPreservesExplicitOrderAndOnlyCurrentIsEligible(t *testing.T) {
	selector := &backendSeriesSelector{
		authIDs:  []string{"profile-c", "profile-a", "profile-b"},
		provider: "claude",
	}
	auths := []*coreauth.Auth{
		backendSeriesTestAuth("profile-a", "claude"),
		backendSeriesTestAuth("profile-b", "claude"),
		backendSeriesTestAuth("profile-c", "claude"),
	}

	requireBackendSeriesPick(t, selector, "claude", auths, "profile-c")
	if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{}, auths[:2]); err == nil || got != nil {
		t.Fatalf("Pick() without current auth = %#v, %v; want no fallback", got, err)
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
			selector.OnResult(test.result)
			requireBackendSeriesPick(t, selector, "claude", auths, "profile-a")
		})
	}

	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
	selector.OnResult(backendSeriesQuotaResult("profile-a"))
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-b")

	countSelector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude"}
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
	claudeAuth := backendSeriesTestAuth("profile-a", "claude")
	if got, err := selector.Pick(t.Context(), "codex", "", coreexecutor.Options{}, []*coreauth.Auth{claudeAuth}); err == nil || got != nil {
		t.Fatalf("Pick() for wrong dispatch provider = %#v, %v; want rejection", got, err)
	}

	wrongProviderAuth := backendSeriesTestAuth("profile-a", "codex")
	if got, err := selector.Pick(t.Context(), "mixed", "", coreexecutor.Options{}, []*coreauth.Auth{wrongProviderAuth}); err == nil || got != nil {
		t.Fatalf("Pick() for wrong credential provider = %#v, %v; want rejection", got, err)
	}
}
