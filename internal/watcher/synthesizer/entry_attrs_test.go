package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// attrKeys is the set of attributes stamped by the per-entry concurrency and
// auto-disable features. Tests assert each is present (with the right value)
// when configured and absent when unset/nil/zero.
var attrKeys = []string{
	"max_parallel",
	"max_wait_ms",
	"auto_disable_codes",
	"auto_disable_cooldown_seconds",
}

// TestConfigSynthesizer_EntryAttrs verifies the per-entry concurrency cap
// (max_parallel, max_wait_ms) and the provider-level auto-disable config
// (auto_disable_codes JSON list, cooldown) are stamped onto the auth
// attributes of every entry-fan-out path (OpenAI-compat, Claude, opencode-go).
// Unset/nil/zero values must leave the attributes absent.
func TestConfigSynthesizer_EntryAttrs(t *testing.T) {
	cases := []struct {
		name       string
		cfg        *config.Config
		wantAbsent bool
	}{
		{
			name: "openai-compat",
			cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{
					{
						Name:    "ExampleProvider",
						BaseURL: "https://compat.example.com/v1",
						APIKeyEntries: []config.OpenAICompatibilityAPIKey{
							{
								APIKey:                     "sk-1",
								MaxConcurrent:              intPtr(3),
								MaxWaitMs:                  intPtr(1500),
								AutoDisableErrorCodes:      []string{"401", "account_suspended"},
								AutoDisableCooldownSeconds: intPtr(3600),
							},
						},
					},
				},
			},
		},
		{
			name: "claude",
			cfg: &config.Config{
				ClaudeKey: []config.ClaudeKey{
					{
						APIKey:                     "sk-ant-1",
						MaxConcurrent:              intPtr(3),
						MaxWaitMs:                  intPtr(1500),
						AutoDisableErrorCodes:      []string{"401", "account_suspended"},
						AutoDisableCooldownSeconds: intPtr(3600),
					},
				},
			},
		},
		{
			name: "opencode-go",
			cfg: &config.Config{
				OpenCodeGo: []config.OpenCodeGo{
					{
						Name:    "ocgo",
						BaseURL: "https://opencode.ai/zen/go/v1",
						APIKeyEntries: []config.OpenCodeGoKey{
							{
								APIKey:                     "sk-1",
								MaxConcurrent:              intPtr(3),
								MaxWaitMs:                  intPtr(1500),
								AutoDisableErrorCodes:      []string{"401", "account_suspended"},
								AutoDisableCooldownSeconds: intPtr(3600),
							},
						},
					},
				},
			},
		},
		{
			name: "unset absent",
			cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{
					{
						Name:    "ExampleProvider",
						BaseURL: "https://compat.example.com/v1",
						APIKeyEntries: []config.OpenAICompatibilityAPIKey{
							{APIKey: "sk-1"},
						},
					},
				},
			},
			wantAbsent: true,
		},
		{
			name: "zero absent",
			cfg: &config.Config{
				ClaudeKey: []config.ClaudeKey{
					{
						APIKey:                     "sk-ant-zero",
						MaxConcurrent:              intPtr(0),
						MaxWaitMs:                  intPtr(0),
						AutoDisableCooldownSeconds: intPtr(0),
					},
				},
			},
			wantAbsent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synth := NewConfigSynthesizer()
			ctx := &SynthesisContext{
				Config:      tc.cfg,
				Now:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				IDGenerator: NewStableIDGenerator(),
			}

			auths, err := synth.Synthesize(ctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(auths) != 1 {
				t.Fatalf("expected 1 auth, got %d", len(auths))
			}
			a := auths[0]
			if tc.wantAbsent {
				assertEntryAttrsAbsent(t, a)
				return
			}
			assertEntryAttrsPresent(t, a)
		})
	}
}

// assertEntryAttrsPresent verifies all four stamped attribute keys carry the
// exact configured values.
func assertEntryAttrsPresent(t *testing.T, a *coreauth.Auth) {
	t.Helper()
	want := map[string]string{
		"max_parallel":                  "3",
		"max_wait_ms":                   "1500",
		"auto_disable_codes":            `["401","account_suspended"]`,
		"auto_disable_cooldown_seconds": "3600",
	}
	for _, key := range attrKeys {
		if got := a.Attributes[key]; got != want[key] {
			t.Errorf("%s = %q, want %q", key, got, want[key])
		}
	}
}

// assertEntryAttrsAbsent verifies none of the four stamped attribute keys are
// present on the auth.
func assertEntryAttrsAbsent(t *testing.T, a *coreauth.Auth) {
	t.Helper()
	for _, key := range attrKeys {
		if v, ok := a.Attributes[key]; ok {
			t.Errorf("%s = %q, want attribute to be absent", key, v)
		}
	}
}
