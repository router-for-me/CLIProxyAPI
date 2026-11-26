package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestFindAllAntigravityCreditsCandidateAuths_EnforcesModelStateGuards_Issue8182(t *testing.T) {
	const model = "claude-opus-5-5-high"
	const quotaErrMsg = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`
	const rateLimitErrMsg = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`
	const capacityErrMsg = `{"error":{"code":503,"status":"UNAVAILABLE","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`

	now := time.Now()
	future := now.Add(30 * time.Minute)
	past := now.Add(-5 * time.Minute)

	cases := []struct {
		name  string
		state *ModelState
		want  int
	}{
		{
			name: "model_status_disabled",
			state: &ModelState{
				Status:         StatusDisabled,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: future,
				},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: quotaErrMsg},
			},
			want: 0,
		},
		{
			name: "model_status_disabled_without_unavailable_flag",
			state: &ModelState{
				Status: StatusDisabled,
			},
			want: 0,
		},
		{
			name: "permanent_unavailability",
			state: &ModelState{
				Status:      StatusError,
				Unavailable: true,
			},
			want: 0,
		},
		{
			name: "forced_model_cooldown_seeded",
			state: &ModelState{
				Status:              StatusError,
				Unavailable:         true,
				NextRetryAfter:      future,
				forceCooldownSeeded: true,
				forceCooldownUntil:  future,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: future,
				},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: quotaErrMsg},
			},
			want: 0,
		},
		{
			name: "forced_model_cooldown_from_last_error",
			state: &ModelState{
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				LastError: &Error{
					Code:       ErrorCodeForceCooldown,
					HTTPStatus: http.StatusTooManyRequests,
					Message:    quotaErrMsg,
				},
			},
			want: 0,
		},
		{
			name: "active_non_quota_cooldown_403",
			state: &ModelState{
				Status:                StatusError,
				Unavailable:           true,
				NextRetryAfter:        future,
				nonQuotaCooldownUntil: future,
				LastError: &Error{
					HTTPStatus: http.StatusForbidden,
					Message:    "forbidden",
				},
			},
			want: 0,
		},
		{
			name: "active_non_quota_cooldown_alongside_quota",
			state: &ModelState{
				Status:                StatusError,
				Unavailable:           true,
				NextRetryAfter:        future,
				nonQuotaCooldownUntil: future,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: future,
				},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: quotaErrMsg},
			},
			want: 0,
		},
		{
			name: "short_burst_rate_limit_429",
			state: &ModelState{
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: future,
				},
				LastError: &Error{
					HTTPStatus: http.StatusTooManyRequests,
					Message:    rateLimitErrMsg,
					Metadata: map[string]string{
						antigravityCreditsRetryHintKey: strconv.FormatInt(int64(45*time.Second), 10),
					},
				},
			},
			want: 0,
		},
		{
			name: "forced_capacity_503_cooldown_rejected",
			state: &ModelState{
				Status:                StatusError,
				Unavailable:           true,
				NextRetryAfter:        future,
				nonQuotaCooldownUntil: future,
				forceCooldownSeeded:   true,
				forceCooldownUntil:    future,
				LastError:             &Error{HTTPStatus: http.StatusServiceUnavailable, Message: capacityErrMsg},
			},
			want: 0,
		},
		{
			name: "eligible_quota_429_cooldown",
			state: &ModelState{
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: future,
				},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: quotaErrMsg},
			},
			want: 1,
		},
		{
			name: "eligible_non_forced_capacity_503_cooldown",
			state: &ModelState{
				Status:                StatusError,
				Unavailable:           true,
				NextRetryAfter:        now.Add(5 * time.Second),
				nonQuotaCooldownUntil: now.Add(5 * time.Second),
				LastError:             &Error{HTTPStatus: http.StatusServiceUnavailable, Message: capacityErrMsg},
			},
			want: 1,
		},
		{
			name: "expired_forced_cooldown_allowed",
			state: &ModelState{
				Status:              StatusActive,
				Unavailable:         true,
				NextRetryAfter:      past,
				forceCooldownSeeded: true,
				forceCooldownUntil:  past,
			},
			want: 1,
		},
	}

	reg := registry.GetGlobalRegistry()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authID := "ag-issue8182-" + tc.name
			reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				reg.UnregisterClient(authID)
				SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
			})

			m := &Manager{
				auths: map[string]*Auth{
					authID: {
						ID:       authID,
						Provider: "antigravity",
						Status:   StatusActive,
						ModelStates: map[string]*ModelState{
							model: tc.state,
						},
					},
				},
				executors: map[string]ProviderExecutor{
					"antigravity": schedulerTestExecutor{},
				},
			}

			candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), model, cliproxyexecutor.Options{})
			if errCandidates != nil {
				t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
			}
			if len(candidates) != tc.want {
				t.Fatalf("got %d credits candidate(s) for %s, want %d", len(candidates), tc.name, tc.want)
			}
		})
	}
}

func TestFindAllAntigravityCreditsCandidateAuths_Capacity503DoesNotMaskPrior404Cooldown_Issue8182(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "claude-opus-5-5-high"
	const capacityErrMsg = `{"error":{"code":503,"status":"UNAVAILABLE","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`
	authID := "ag-issue8182-404-then-503"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
		SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
	})

	cdsDir := t.TempDir()
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	m.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))
	m.RegisterExecutor(schedulerTestExecutor{provider: "antigravity"})

	registered, errReg := m.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
	})
	if errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	// Precondition: healthy auth is eligible before the 404 cooldown is recorded.
	if before, errBefore := m.findAllAntigravityCreditsCandidateAuths(context.Background(), model, cliproxyexecutor.Options{}); errBefore != nil || len(before) != 1 {
		t.Fatalf("precondition candidates = (%d, %v), want (1, nil)", len(before), errBefore)
	}

	// First in-flight request fails with 404 (12h non-quota cooldown).
	m.MarkResult(context.Background(), Result{
		AuthID:            registered.ID,
		Provider:          "antigravity",
		Model:             model,
		RouteModel:        model,
		Success:           false,
		Error:             &Error{HTTPStatus: http.StatusNotFound, Message: "model not found"},
		CredentialVersion: registered.CredentialVersion,
		RegistrationEpoch: registered.RegistrationEpoch,
	})

	// Second in-flight request fails with 503 MODEL_CAPACITY_EXHAUSTED, overwriting LastError
	// while keeping the active 404 cooldown deadline.
	m.MarkResult(context.Background(), Result{
		AuthID:            registered.ID,
		Provider:          "antigravity",
		Model:             model,
		RouteModel:        model,
		Success:           false,
		Error:             &Error{HTTPStatus: http.StatusServiceUnavailable, Message: capacityErrMsg},
		CredentialVersion: registered.CredentialVersion,
		RegistrationEpoch: registered.RegistrationEpoch,
	})

	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(context.Background(), model, cliproxyexecutor.Options{})
	if errCandidates != nil {
		t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
	}
	if len(candidates) != 0 {
		t.Fatalf("got %d credits candidate(s) after 404 followed by 503 capacity, want 0", len(candidates))
	}

	// Verify the non-capacity 404 cooldown survives persistence and restart into a fresh Manager.
	restoredManager := NewManager(nil, nil, nil)
	restoredManager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	restoredManager.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))
	restoredManager.RegisterExecutor(schedulerTestExecutor{provider: "antigravity"})
	if _, errReg2 := restoredManager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
	}); errReg2 != nil {
		t.Fatalf("restoredManager.Register() error = %v", errReg2)
	}
	if errRestore := restoredManager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() error = %v", errRestore)
	}
	restoredCandidates, errRestored := restoredManager.findAllAntigravityCreditsCandidateAuths(context.Background(), model, cliproxyexecutor.Options{})
	if errRestored != nil {
		t.Fatalf("restoredManager.findAllAntigravityCreditsCandidateAuths() error = %v", errRestored)
	}
	if len(restoredCandidates) != 0 {
		t.Fatalf("got %d credits candidate(s) after restart restore of 404+503, want 0", len(restoredCandidates))
	}
}

func TestFindAllAntigravityCreditsCandidateAuths_SwitchedVariantNotRejectedBySourceModelCooldown_Issue8182(t *testing.T) {
	const group = "claude-issue8182-group"
	const high, low = group + "-high", group + "-low"
	const quotaErrMsg = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`
	future := time.Now().Add(30 * time.Minute)

	cases := []struct {
		name     string
		observed string
		opts     cliproxyexecutor.Options
		want     int
	}{
		{
			name:     "observed_switched_variant",
			observed: low,
			opts:     cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude},
			want:     1,
		},
		{
			name: "inferred_switched_variant_from_payload",
			opts: cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]}`),
			},
			want: 1,
		},
		{
			name: "uncertain_grouped_model_without_payload",
			opts: cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude},
			want: 1,
		},
		{
			name: "uncertain_grouped_model_with_interceptor",
			opts: cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`),
				RequestAfterAuthInterceptor: func(_ context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
				},
			},
			want: 1,
		},
		{
			name: "confirmed_direct_high_rejected",
			opts: cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`),
			},
			want: 0,
		},
	}

	reg := registry.GetGlobalRegistry()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authID := "ag-issue8182-variant-" + tc.name
			reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: high}, {ID: low}})
			t.Cleanup(func() {
				reg.UnregisterClient(authID)
				SetAntigravityCreditsHint(authID, AntigravityCreditsHint{})
			})

			m := NewManager(nil, nil, nil)
			m.SetConfig(&internalconfig.Config{
				QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
			})
			m.RegisterExecutor(schedulerTestExecutor{provider: "antigravity"})

			registered, errReg := m.Register(context.Background(), &Auth{
				ID:       authID,
				Provider: "antigravity",
				Status:   StatusActive,
				ModelStates: map[string]*ModelState{
					high: {
						Status:                   StatusError,
						Unavailable:              true,
						NextRetryAfter:           future,
						nonQuotaCooldownUntil:    future,
						nonCapacityCooldownUntil: future,
						LastError:                &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
					},
					low: {
						Status:         StatusError,
						Unavailable:    true,
						NextRetryAfter: future,
						Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future},
						LastError:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: quotaErrMsg},
					},
				},
			})
			if errReg != nil {
				t.Fatalf("Register() error = %v", errReg)
			}
			if !m.StoreAntigravityModelCatalog(context.Background(), registered, map[string]string{high: "HIGH", low: "LOW"}, map[string]map[string]int{group: {"low": 50000, "high": 60000}}, nil) {
				t.Fatal("failed to publish catalog")
			}

			ctx := withAntigravityAttemptedModels(context.Background())
			if tc.observed != "" {
				recordAntigravityAttemptedModel(ctx, registered.ID, tc.observed)
			}

			candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(ctx, high, tc.opts)
			if errCandidates != nil {
				t.Fatalf("findAllAntigravityCreditsCandidateAuths() error = %v", errCandidates)
			}
			if len(candidates) != tc.want {
				t.Fatalf("got %d credits candidate(s) for %s, want %d", len(candidates), tc.name, tc.want)
			}
		})
	}
}
