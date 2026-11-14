package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func openAIListedModelSet(models []map[string]any) map[string]bool {
	out := make(map[string]bool, len(models))
	for _, m := range models {
		if id, _ := m["id"].(string); id != "" {
			out[id] = true
		}
	}
	return out
}

func TestManager_ClientModelProjection_TransientCooldownExpiresWithForkedAlias(t *testing.T) {
	previousCooldown := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousCooldown) })

	m := NewManager(nil, nil, nil)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"xai": {{
			Name:         "grok-4.7",
			Alias:        "grok-4.7-cp",
			Fork:         true,
			ForceMapping: true,
		}},
	})

	auth := &Auth{
		ID:       "xai-transient-7421",
		Provider: "xai",
		Status:   StatusActive,
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	nowUnix := time.Now().Unix()
	reg.RegisterClient(auth.ID, "xai", []*registry.ModelInfo{
		{ID: "grok-4.7", OwnedBy: "xai", Created: nowUnix},
		{ID: "grok-4.7-cp", OwnedBy: "xai", Created: nowUnix},
		{ID: "grok-4.6", OwnedBy: "xai", Created: nowUnix},
	})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "xai",
		Model:    "grok-4.7",
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusRequestTimeout,
			Message:    "xai stream error: stream disconnected",
		},
	})

	snap, ok := m.GetByID(auth.ID)
	if !ok || snap == nil {
		t.Fatal("expected auth snapshot after MarkResult")
	}
	state := existingModelState(snap, "grok-4.7")
	if state == nil || !state.Unavailable || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected active transient model cooldown, got %+v", state)
	}

	listedDuring := openAIListedModelSet(reg.GetAvailableModels("openai"))
	if listedDuring["grok-4.7"] || listedDuring["grok-4.7-cp"] {
		t.Fatalf("expected grok-4.7 and grok-4.7-cp to be hidden during live transient cooldown, got %v", listedDuring)
	}
	if !listedDuring["grok-4.6"] {
		t.Fatalf("expected unaffected sibling grok-4.6 to remain listed, got %v", listedDuring)
	}

	during := state.NextRetryAfter.Add(-time.Second)
	after := state.NextRetryAfter.Add(time.Second)

	for _, modelID := range []string{"grok-4.7", "grok-4.7-cp"} {
		selectionModel := m.selectionModelForAuth(snap, modelID)
		blockedDuring, _, _ := isAuthBlockedForModel(snap, selectionModel, during)
		if !blockedDuring {
			t.Fatalf("expected selector to block %s during transient cooldown", modelID)
		}
		projDuring := m.clientModelProjectionForAuth(snap, modelID, during)
		if !projDuring.Suspended {
			t.Fatalf("expected %s to be projected suspended during transient cooldown: %+v", modelID, projDuring)
		}
		if !projDuring.SuspendUntil.Equal(state.NextRetryAfter) {
			t.Fatalf("expected %s SuspendUntil=%v, got %v", modelID, state.NextRetryAfter, projDuring.SuspendUntil)
		}

		blockedAfter, _, _ := isAuthBlockedForModel(snap, selectionModel, after)
		if blockedAfter {
			t.Fatalf("expected selector to unblock %s after transient cooldown expired", modelID)
		}
		projAfter := m.clientModelProjectionForAuth(snap, modelID, after)
		if projAfter.Suspended {
			t.Fatalf("expected %s projection Suspended=false once NextRetryAfter passed, got %+v", modelID, projAfter)
		}
		if !projAfter.SuspendUntil.IsZero() {
			t.Fatalf("expected %s SuspendUntil to be zero once expired, got %v", modelID, projAfter.SuspendUntil)
		}
	}

	// Simulate the recorded transient deadline now lying in the past without a
	// new MarkResult or credential refresh: /v1/models must list both models.
	epoch := reg.ClientRegistrationEpoch(auth.ID)
	staleUntil := time.Now().Add(-time.Second)
	if !reg.ApplyClientModelProjections(auth.ID, epoch, snap.Generation, []registry.ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: staleUntil},
		{ModelID: "grok-4.7-cp", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: staleUntil},
		{ModelID: "grok-4.6"},
	}) {
		t.Fatal("projection with past deadline rejected")
	}
	listedAfter := openAIListedModelSet(reg.GetAvailableModels("openai"))
	if !listedAfter["grok-4.7"] || !listedAfter["grok-4.7-cp"] || !listedAfter["grok-4.6"] {
		t.Fatalf("expected /v1/models to list grok-4.7 and grok-4.7-cp after deadline passed, got %v", listedAfter)
	}
}

func TestManager_ClientModelProjection_OpenEndedAuthGatesStayUnbounded(t *testing.T) {
	now := time.Now()
	transientUntil := now.Add(time.Minute)
	m := NewManager(nil, nil, nil)

	cases := []struct {
		name string
		auth *Auth
	}{
		{
			name: "disabled_auth_with_model_transient_deadline",
			auth: &Auth{
				ID:       "disabled-with-transient",
				Provider: "xai",
				Disabled: true,
				Status:   StatusDisabled,
				ModelStates: map[string]*ModelState{
					"grok-4.7": {
						Unavailable:    true,
						Status:         StatusError,
						StatusMessage:  "xai stream error: stream disconnected",
						NextRetryAfter: transientUntil,
					},
				},
			},
		},
		{
			name: "disabled_auth_with_active_model_quota",
			auth: &Auth{
				ID:       "disabled-with-quota",
				Provider: "xai",
				Disabled: true,
				Status:   StatusDisabled,
				ModelStates: map[string]*ModelState{
					"grok-4.7": {
						Unavailable:    true,
						Status:         StatusError,
						NextRetryAfter: transientUntil,
						Quota: QuotaState{
							Exceeded:      true,
							Reason:        "quota",
							NextRecoverAt: transientUntil,
						},
					},
				},
			},
		},
		{
			name: "disabled_auth_with_auth_level_deadline",
			auth: &Auth{
				ID:             "disabled-auth-level",
				Provider:       "xai",
				Disabled:       true,
				Unavailable:    true,
				NextRetryAfter: transientUntil,
			},
		},
		{
			name: "terminal_unauthorized_auth_without_model_state",
			auth: &Auth{
				ID:          "unauthorized-no-model-state",
				Provider:    "xai",
				Unavailable: true,
				Status:      StatusError,
				LastError: &Error{
					HTTPStatus: http.StatusUnauthorized,
					Code:       "unauthorized",
					Message:    "refresh token invalid",
				},
			},
		},
		{
			name: "terminal_unauthorized_auth_with_model_transient_deadline",
			auth: &Auth{
				ID:          "unauthorized-with-transient",
				Provider:    "xai",
				Unavailable: true,
				Status:      StatusError,
				LastError: &Error{
					HTTPStatus: http.StatusUnauthorized,
					Code:       "unauthorized",
					Message:    "refresh token invalid",
				},
				ModelStates: map[string]*ModelState{
					"grok-4.7": {
						Unavailable:    true,
						Status:         StatusError,
						StatusMessage:  "xai stream error: stream disconnected",
						NextRetryAfter: transientUntil,
					},
				},
			},
		},
		{
			name: "disabled_model_state_with_transient_deadline",
			auth: &Auth{
				ID:       "model-disabled-with-transient",
				Provider: "xai",
				Status:   StatusActive,
				ModelStates: map[string]*ModelState{
					"grok-4.7": {
						Unavailable:    true,
						Status:         StatusDisabled,
						StatusMessage:  "model disabled",
						NextRetryAfter: transientUntil,
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := m.clientModelProjectionForAuth(tc.auth, "grok-4.7", now)
			if !proj.Suspended {
				t.Fatalf("expected Suspended=true, got %+v", proj)
			}
			if !proj.SuspendUntil.IsZero() {
				t.Fatalf("open-ended gate must not carry finite SuspendUntil, got %v", proj.SuspendUntil)
			}
			if proj.SuspendReason == "quota" || proj.SuspendReason == "credential_quota" {
				t.Fatalf("open-ended disabled/unauthorized gate must not carry quota SuspendReason, got %q", proj.SuspendReason)
			}
		})
	}
}

func TestManager_ClientModelProjection_CredentialQuotaIgnoresExpiredModelTransientError(t *testing.T) {
	now := time.Now()
	recoverAt := now.Add(time.Hour)
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "xai-cred-quota-stale-transient",
		Provider: "xai",
		Status:   StatusError,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: recoverAt,
		},
		ModelStates: map[string]*ModelState{
			"grok-4.7": {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "xai stream error: stream disconnected",
				NextRetryAfter: now.Add(-time.Minute),
				LastError: &Error{
					HTTPStatus: http.StatusRequestTimeout,
					Message:    "xai stream error: stream disconnected",
				},
			},
		},
	}

	proj := m.clientModelProjectionForAuth(auth, "grok-4.7", now)
	if !proj.Suspended {
		t.Fatalf("expected model under live credential_quota to be suspended, got %+v", proj)
	}
	if proj.SuspendReason != "credential_quota" {
		t.Fatalf("expected SuspendReason=%q so sole-credential quota stays listed, got %q", "credential_quota", proj.SuspendReason)
	}
	if !proj.SuspendUntil.Equal(recoverAt) {
		t.Fatalf("expected SuspendUntil=%v, got %v", recoverAt, proj.SuspendUntil)
	}

	// Sibling model with no ModelState entry also inherits the credential_quota deadline.
	siblingProj := m.clientModelProjectionForAuth(auth, "grok-4.6", now)
	if !siblingProj.Suspended || siblingProj.SuspendReason != "credential_quota" || !siblingProj.SuspendUntil.Equal(recoverAt) {
		t.Fatalf("unexpected sibling projection under credential_quota: %+v", siblingProj)
	}
}
