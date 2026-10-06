package executor

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestResolveClaudeWirePolicy_RelaxedSystemPromptPrecedence(t *testing.T) {
	enabled := true
	disabled := false
	tests := []struct {
		name        string
		auth        *cliproxyauth.Auth
		cloak       *config.CloakConfig
		wantCloak   bool
		wantRelaxed bool
	}{
		{name: "default disabled", auth: &cliproxyauth.Auth{}},
		{name: "metadata bool enabled", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": true}}, wantCloak: true, wantRelaxed: true},
		{name: "metadata string enabled", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": "true"}}, wantCloak: true, wantRelaxed: true},
		{name: "attribute disabled overrides metadata", auth: &cliproxyauth.Auth{Attributes: map[string]string{"cloak_relaxed_system_prompt": "false"}, Metadata: map[string]any{"cloak_relaxed_system_prompt": true}}, wantCloak: true},
		{name: "key config overrides metadata", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": false}}, cloak: &config.CloakConfig{RelaxedSystemPrompt: &enabled}, wantCloak: true, wantRelaxed: true},
		{name: "key config explicit false", auth: &cliproxyauth.Auth{Metadata: map[string]any{"cloak_relaxed_system_prompt": true}}, cloak: &config.CloakConfig{RelaxedSystemPrompt: &disabled}, wantCloak: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			if test.cloak != nil {
				test.auth.Attributes = map[string]string{"api_key": "key-123"}
				cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "key-123", Cloak: test.cloak}}
			}
			policy, settings := resolveClaudeWirePolicy(cfg, test.auth, "key-123", false)
			if policy.Cloak != test.wantCloak {
				t.Fatalf("Cloak = %v, want %v", policy.Cloak, test.wantCloak)
			}
			if settings.relaxedSystemPrompt != test.wantRelaxed {
				t.Fatalf("relaxedSystemPrompt = %v, want %v", settings.relaxedSystemPrompt, test.wantRelaxed)
			}
		})
	}
}

func TestResolveClaudeWirePolicy_RelaxedHonorsSharedUpstreamDefaults(t *testing.T) {
	for _, test := range []struct {
		name      string
		cloak     string
		attrMode  string
		wantCloak bool
		wantRelax bool
	}{
		{name: "shared disable", cloak: "relaxed-system-prompt: true", wantRelax: true},
		{name: "key enables cloak", cloak: "mode: auto, relaxed-system-prompt: true", wantCloak: true, wantRelax: true},
		{name: "key disables relaxed", cloak: "mode: auto, relaxed-system-prompt: false", wantCloak: true},
		{name: "metadata enables cloak", cloak: "relaxed-system-prompt: true", attrMode: "always", wantCloak: true, wantRelax: true},
		{name: "key overrides metadata mode", cloak: "mode: never, relaxed-system-prompt: true", attrMode: "always", wantRelax: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, errConfig := config.ParseConfigBytes([]byte(fmt.Sprintf(`upstream:
  claude:
    disable-claude-cloak-mode: true
    header-defaults: {timezone: Asia/Singapore}
api-keys:
  claude:
    - name: primary
      keys:
        - api-key: shared-default-key
          fingerprint-profile: claude-code-cli
          cloak: {%s}
`, test.cloak)))
			if errConfig != nil {
				t.Fatal(errConfig)
			}
			auth := &cliproxyauth.Auth{
				Attributes: map[string]string{"api_key": "shared-default-key", "cloak_mode": test.attrMode},
				Metadata:   map[string]any{"cloak_relaxed_system_prompt": true},
			}
			for _, snapshot := range []*config.Config{cfg, cfg.CloneForRuntime()} {
				if scoped := snapshot.ForAPIKey(); !scoped.DisableClaudeCloakMode || scoped.ClaudeHeaderDefaults.Timezone != "Asia/Singapore" {
					t.Fatal("shared Claude defaults must remain available to API-key credentials")
				}
				policy, settings := resolveClaudeWirePolicy(snapshot, auth, "shared-default-key", false)
				if policy.Cloak != test.wantCloak || settings.relaxedSystemPrompt != test.wantRelax {
					t.Fatalf("cloak/relaxed = %t/%t, want %t/%t", policy.Cloak, settings.relaxedSystemPrompt, test.wantCloak, test.wantRelax)
				}
				if native, _ := resolveClaudeWirePolicy(snapshot, auth, "shared-default-key", true); native.Cloak {
					t.Fatal("confirmed native Claude Code must remain a passthrough client")
				}
			}
		})
	}
}
