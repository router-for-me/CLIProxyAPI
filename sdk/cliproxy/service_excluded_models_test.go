package cliproxy

import (
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_UsesPreMergedExcludedModelsAttribute(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"gemini-cli": {"gemini-2.5-pro"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini-cli",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"excluded_models": "gemini-2.5-flash",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetAvailableModelsByProvider("gemini-cli")
	if len(models) == 0 {
		t.Fatal("expected gemini-cli models to be registered")
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if strings.EqualFold(modelID, "gemini-2.5-flash") {
			t.Fatalf("expected model %q to be excluded by auth attribute", modelID)
		}
	}

	seenGlobalExcluded := false
	for _, model := range models {
		if model == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(model.ID), "gemini-2.5-pro") {
			seenGlobalExcluded = true
			break
		}
	}
	if !seenGlobalExcluded {
		t.Fatal("expected global excluded model to be present when attribute override is set")
	}
}

func TestEffectiveCodexPlanType_QuotaWindowsOverrideStalePlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		authKind string
		attrs    map[string]string
		want     string
	}{
		{
			name:     "single quota window is free even with stale plus plan",
			authKind: "oauth",
			attrs: map[string]string{
				"plan_type":                "plus",
				"codex_quota_window_count": "1",
			},
			want: "free",
		},
		{
			name:     "two quota windows promote an otherwise free snapshot to paid",
			authKind: "oauth",
			attrs: map[string]string{
				"plan_type":           "free",
				"codex_quota_windows": "5h,168h",
			},
			want: "plus",
		},
		{
			name:     "unknown oauth accounts default to free",
			authKind: "oauth",
			attrs:    map[string]string{},
			want:     "free",
		},
		{
			name:     "unknown api key accounts keep legacy pro default",
			authKind: "api_key",
			attrs:    map[string]string{},
			want:     "pro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveCodexPlanType(tt.authKind, tt.attrs); got != tt.want {
				t.Fatalf("effectiveCodexPlanType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCodexUsageEntitlementAttrsFromJSON_CountsQuotaWindows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		wantPlan  string
		wantCount string
	}{
		{
			name: "free one-window account",
			body: `{
				"plan_type": "free",
				"rate_limit": {
					"primary_window": {"limit_window_seconds": 604800}
				}
			}`,
			wantPlan:  "free",
			wantCount: "1",
		},
		{
			name: "paid two-window account",
			body: `{
				"plan_type": "plus",
				"rate_limit": {
					"primary_window": {"limit_window_seconds": 604800},
					"secondary_window": {"limit_window_seconds": 18000}
				}
			}`,
			wantPlan:  "plus",
			wantCount: "2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := codexUsageEntitlementAttrsFromJSON([]byte(tt.body))
			if got := attrs["plan_type"]; got != tt.wantPlan {
				t.Fatalf("plan_type = %q, want %q", got, tt.wantPlan)
			}
			if got := attrs["codex_quota_window_count"]; got != tt.wantCount {
				t.Fatalf("codex_quota_window_count = %q, want %q", got, tt.wantCount)
			}
		})
	}
}

func TestRegisterModelsForAuth_CodexSingleQuotaWindowHidesGPT55(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-codex-single-quota-window",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":                "oauth",
			"plan_type":                "plus",
			"codex_quota_window_count": "1",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetAvailableModelsByProvider("codex")
	if hasModelID(models, "gpt-5.5") {
		t.Fatal("expected gpt-5.5 to be hidden for a single-window Codex account")
	}
	if !hasModelID(models, "gpt-5.4") {
		t.Fatal("expected free-tier Codex models such as gpt-5.4 to remain registered")
	}
}

func TestRegisterModelsForAuth_CodexTwoQuotaWindowsShowsGPT55(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-codex-two-quota-windows",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":           "oauth",
			"plan_type":           "free",
			"codex_quota_windows": "5h,168h",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	if !hasModelID(registry.GetAvailableModelsByProvider("codex"), "gpt-5.5") {
		t.Fatal("expected gpt-5.5 to be registered for a two-window paid Codex account")
	}
}

func hasModelID(models []*ModelInfo, id string) bool {
	for _, model := range models {
		if model != nil && strings.EqualFold(strings.TrimSpace(model.ID), id) {
			return true
		}
	}
	return false
}
