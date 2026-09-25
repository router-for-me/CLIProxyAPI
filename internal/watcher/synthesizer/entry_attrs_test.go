package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

// TestConfigSynthesizer_EntryAttrs_OpenAICompat verifies the per-entry
// concurrency cap (max_parallel, max_wait_ms) and the provider-level
// auto-disable config (auto_disable_codes JSON list, cooldown) are stamped
// onto the auth attributes of OpenAI-compat entries.
func TestConfigSynthesizer_EntryAttrs_OpenAICompat(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
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
		Now:         time.Now(),
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
	if got := a.Attributes["max_parallel"]; got != "3" {
		t.Errorf("max_parallel = %q, want %q", got, "3")
	}
	if got := a.Attributes["max_wait_ms"]; got != "1500" {
		t.Errorf("max_wait_ms = %q, want %q", got, "1500")
	}
	if got := a.Attributes["auto_disable_codes"]; got != `["401","account_suspended"]` {
		t.Errorf("auto_disable_codes = %q, want %q", got, `["401","account_suspended"]`)
	}
	if got := a.Attributes["auto_disable_cooldown_seconds"]; got != "3600" {
		t.Errorf("auto_disable_cooldown_seconds = %q, want %q", got, "3600")
	}
}

// TestConfigSynthesizer_EntryAttrs_Claude verifies the same stamping on the
// Claude fan-out path (each config.ClaudeKey carries the provider-level
// auto-disable render copy).
func TestConfigSynthesizer_EntryAttrs_Claude(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
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
		Now:         time.Now(),
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
	if got := a.Attributes["max_parallel"]; got != "3" {
		t.Errorf("max_parallel = %q, want %q", got, "3")
	}
	if got := a.Attributes["max_wait_ms"]; got != "1500" {
		t.Errorf("max_wait_ms = %q, want %q", got, "1500")
	}
	if got := a.Attributes["auto_disable_codes"]; got != `["401","account_suspended"]` {
		t.Errorf("auto_disable_codes = %q, want %q", got, `["401","account_suspended"]`)
	}
	if got := a.Attributes["auto_disable_cooldown_seconds"]; got != "3600" {
		t.Errorf("auto_disable_cooldown_seconds = %q, want %q", got, "3600")
	}
}

// TestConfigSynthesizer_EntryAttrs_OpenCodeGo verifies the same stamping on
// the opencode-go fan-out path.
func TestConfigSynthesizer_EntryAttrs_OpenCodeGo(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
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
		Now:         time.Now(),
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
	if got := a.Attributes["max_parallel"]; got != "3" {
		t.Errorf("max_parallel = %q, want %q", got, "3")
	}
	if got := a.Attributes["max_wait_ms"]; got != "1500" {
		t.Errorf("max_wait_ms = %q, want %q", got, "1500")
	}
	if got := a.Attributes["auto_disable_codes"]; got != `["401","account_suspended"]` {
		t.Errorf("auto_disable_codes = %q, want %q", got, `["401","account_suspended"]`)
	}
	if got := a.Attributes["auto_disable_cooldown_seconds"]; got != "3600" {
		t.Errorf("auto_disable_cooldown_seconds = %q, want %q", got, "3600")
	}
}

// TestConfigSynthesizer_EntryAttrs_UnsetAbsent asserts that entries with all
// four fields unset/nil produce an auth with none of the four attributes.
func TestConfigSynthesizer_EntryAttrs_UnsetAbsent(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
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
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	for _, key := range attrKeys {
		if v, ok := auths[0].Attributes[key]; ok {
			t.Errorf("%s = %q, want attribute to be absent when unset", key, v)
		}
	}
}

// TestConfigSynthesizer_EntryAttrs_ZeroAbsent asserts that explicit zero or
// empty values are never stamped: max_concurrent 0 = unlimited, empty code
// list = feature off, cooldown 0 = manual re-enable.
func TestConfigSynthesizer_EntryAttrs_ZeroAbsent(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{
					APIKey:                     "sk-ant-zero",
					MaxConcurrent:              intPtr(0),
					MaxWaitMs:                  intPtr(0),
					AutoDisableCooldownSeconds: intPtr(0),
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	for _, key := range attrKeys {
		if v, ok := auths[0].Attributes[key]; ok {
			t.Errorf("%s = %q, want attribute to be absent when value is zero/empty", key, v)
		}
	}
}
