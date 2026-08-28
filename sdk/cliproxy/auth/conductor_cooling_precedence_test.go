package auth

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestManagerMarkResultUsesCredentialCoolingPrecedence(t *testing.T) {
	previousGlobal := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousGlobal) })

	disabled := true
	enabled := false
	tests := []struct {
		name             string
		homeEnabled      bool
		globalDisable    bool
		credential       *bool
		providerOverride *bool
		wantCooldown     bool
	}{
		{name: "credential true overrides global false", credential: &disabled},
		{name: "credential false overrides global true", globalDisable: true, credential: &enabled, wantCooldown: true},
		{name: "unset inherits global true", globalDisable: true},
		{name: "unset inherits global false", wantCooldown: true},
		{name: "provider false overrides global true", globalDisable: true, providerOverride: &enabled, wantCooldown: true},
		{name: "provider true overrides global false", providerOverride: &disabled},
		{name: "credential false overrides provider true", credential: &enabled, providerOverride: &disabled, wantCooldown: true},
		{name: "home mode disables local cooling despite credential false", homeEnabled: true, credential: &enabled},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			cfg := &internalconfig.Config{
				DisableCooling: tc.globalDisable,
				Home:           internalconfig.HomeConfig{Enabled: tc.homeEnabled},
			}
			auth := &Auth{ID: tc.name, Provider: "claude", Status: StatusActive}
			if tc.credential != nil {
				auth.Metadata = map[string]any{"disable_cooling": *tc.credential}
			}
			if tc.providerOverride != nil {
				auth.Provider = "openai-compatibility"
				auth.Attributes = map[string]string{
					"provider_key": "compat",
					"compat_name":  "compat",
				}
				cfg.OpenAICompatibility = []internalconfig.OpenAICompatibility{{
					Name:           "compat",
					BaseURL:        "https://compat.example.com",
					DisableCooling: tc.providerOverride,
				}}
			}
			manager.SetConfig(cfg)
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("Register() error = %v", errRegister)
			}

			const model = "test-model"
			manager.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Error:    &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream failed"},
			})

			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil || updated.ModelStates[model] == nil {
				t.Fatalf("updated auth/model state missing: %#v", updated)
			}
			gotCooldown := !updated.ModelStates[model].NextRetryAfter.IsZero()
			if gotCooldown != tc.wantCooldown {
				t.Fatalf("cooldown present = %t, want %t", gotCooldown, tc.wantCooldown)
			}
		})
	}
}
