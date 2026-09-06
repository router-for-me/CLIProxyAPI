package synthesizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// fakeCommandCodeCLICredential builds a credential payload shaped exactly like
// the real `cmdc login` auth.json, but with a synthetic key. It must never
// contain a real secret; tests only use placeholder values.
func fakeCommandCodeCLICredential(apiKey string) []byte {
	cred := map[string]any{
		"apiKey":          apiKey,
		"userId":          "00000000-0000-0000-0000-000000000000",
		"userName":        "test-user",
		"keyName":         "test-key",
		"authenticatedAt": "2026-01-01T00:00:00.000Z",
	}
	b, _ := json.Marshal(cred)
	return b
}

// writeCommandCodeCLIAuth writes a fake credential file and points the path
// resolver at it. Returns a cleanup func that restores the original resolver.
func writeCommandCodeCLIAuth(t *testing.T, content []byte) (cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("write fake cli auth: %v", err)
	}
	orig := commandCodeCLIAuthPathFn
	commandCodeCLIAuthPathFn = func() (string, error) { return path, nil }
	return func() { commandCodeCLIAuthPathFn = orig }
}

func TestSynthesizeCommandCodeCLI_ValidCredentialRegistersAuth(t *testing.T) {
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("cmdc-test-key-0001"))
	defer cleanup()

	ctx := &SynthesisContext{
		Config:      &config.Config{},
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}
	s := NewConfigSynthesizer()
	auths := s.synthesizeCommandCodeCLI(ctx)
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	a := auths[0]
	if a.Provider != constant.CommandCode {
		t.Errorf("provider = %q, want %q", a.Provider, constant.CommandCode)
	}
	if got := a.Attributes["api_key"]; got != "cmdc-test-key-0001" {
		t.Errorf("api_key mismatch (secret must not leak): %q", got)
	}
	if a.Attributes["source"] != "cli:commandcode" {
		t.Errorf("source = %q", a.Attributes["source"])
	}
	if a.Status != coreauth.StatusActive {
		t.Errorf("auth should be active, got %v", a.Status)
	}
	if a.ID == "" {
		t.Errorf("auth ID must be set")
	}
}

func TestSynthesizeCommandCodeCLI_MissingCredentialIsUnauthenticated(t *testing.T) {
	// Point the resolver at a non-existent path.
	orig := commandCodeCLIAuthPathFn
	commandCodeCLIAuthPathFn = func() (string, error) {
		return filepath.Join(t.TempDir(), "does-not-exist.json"), nil
	}
	defer func() { commandCodeCLIAuthPathFn = orig }()

	ctx := &SynthesisContext{
		Config:      &config.Config{},
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}
	s := NewConfigSynthesizer()
	auths := s.synthesizeCommandCodeCLI(ctx)
	if len(auths) != 0 {
		t.Fatalf("expected no auth when credential missing, got %d", len(auths))
	}
}

func TestSynthesizeCommandCodeCLI_MalformedCredentialFailsClosed(t *testing.T) {
	cleanup := writeCommandCodeCLIAuth(t, []byte("{not-json!!"))
	defer cleanup()

	ctx := &SynthesisContext{
		Config:      &config.Config{},
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}
	s := NewConfigSynthesizer()
	auths := s.synthesizeCommandCodeCLI(ctx)
	if len(auths) != 0 {
		t.Fatalf("expected fail-closed (0 auths) on malformed credential, got %d", len(auths))
	}
}

func TestSynthesizeCommandCodeCLI_EmptyAPIKeyFailsClosed(t *testing.T) {
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("  "))
	defer cleanup()

	ctx := &SynthesisContext{
		Config:      &config.Config{},
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}
	s := NewConfigSynthesizer()
	auths := s.synthesizeCommandCodeCLI(ctx)
	if len(auths) != 0 {
		t.Fatalf("expected fail-closed (0 auths) for empty apiKey, got %d", len(auths))
	}
}

func TestSynthesizeCommandCodeCLI_CoexistsWithOtherProviders(t *testing.T) {
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("cmdc-test-key-0002"))
	defer cleanup()

	cfg := &config.Config{
		XAIKey: []config.XAIKey{{APIKey: "xai-test-key"}},
	}
	ctx := &SynthesisContext{
		Config:      cfg,
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}
	s := NewConfigSynthesizer()
	auths, err := s.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	var cmdCount, xaiCount int
	for _, a := range auths {
		switch a.Provider {
		case constant.CommandCode:
			cmdCount++
		case "xai":
			xaiCount++
		}
	}
	if cmdCount != 1 {
		t.Errorf("expected 1 commandcode auth, got %d", cmdCount)
	}
	if xaiCount != 1 {
		t.Errorf("expected 1 xai auth, got %d", xaiCount)
	}
}

func TestSynthesizeCommandCodeCLI_DefaultConfigNeedsNoAPIKey(t *testing.T) {
	// Default config with no commandcode-api-key: when CLI credential exists,
	// the config path is not required and Synthesize still produces the auth.
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("cmdc-test-key-0003"))
	defer cleanup()

	cfg := &config.Config{}
	ctx := &SynthesisContext{
		Config:      cfg,
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}
	s := NewConfigSynthesizer()
	auths, err := s.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	cmdCount := 0
	for _, a := range auths {
		if a.Provider == constant.CommandCode {
			cmdCount++
		}
	}
	if cmdCount != 1 {
		t.Errorf("default config should register commandcode via CLI credential; got %d", cmdCount)
	}
}

func TestSynthesizeCommandCodeCLI_ConfigKeyTakesPrecedence(t *testing.T) {
	// CLI credential absent + explicit commandcode-api-key present: config key
	// path must produce an auth (config-key precedence).
	orig := commandCodeCLIAuthPathFn
	commandCodeCLIAuthPathFn = func() (string, error) {
		return filepath.Join(t.TempDir(), "missing.json"), nil
	}
	defer func() { commandCodeCLIAuthPathFn = orig }()

	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{{APIKey: "cmdc-config-override-key"}},
	}
	ctx := &SynthesisContext{
		Config:      cfg,
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}
	s := NewConfigSynthesizer()
	auths, err := s.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	cmdCount := 0
	for _, a := range auths {
		if a.Provider == constant.CommandCode {
			cmdCount++
			if a.Attributes["api_key"] != "cmdc-config-override-key" {
				t.Errorf("expected config override key to be used")
			}
		}
	}
	if cmdCount != 1 {
		t.Errorf("expected config override auth, got %d", cmdCount)
	}
}

// TestSynthesizeCommandCodeCLI_ConfigKeyBeatsCLICredential verifies the
// precedence rule when both sources exist: an explicit commandcode-api-key
// config wins over the auto-imported `cmdc login` CLI credential.
func TestSynthesizeCommandCodeCLI_ConfigKeyBeatsCLICredential(t *testing.T) {
	// Both CLI credential and config key present.
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("cmdc-cli-credential-key"))
	defer cleanup()

	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{{APIKey: "cmdc-config-explicit-key"}},
	}
	ctx := &SynthesisContext{
		Config:      cfg,
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}
	s := NewConfigSynthesizer()
	auths, err := s.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	cmdCount := 0
	for _, a := range auths {
		if a.Provider == constant.CommandCode {
			cmdCount++
			if got := a.Attributes["api_key"]; got != "cmdc-config-explicit-key" {
				t.Errorf("explicit config key must win over CLI credential, got %q", got)
			}
		}
	}
	if cmdCount != 1 {
		t.Errorf("expected exactly one commandcode auth, got %d", cmdCount)
	}
}

// TestSynthesizeCommandCode_AuthFileBeatsCLICredential verifies that when an
// auth JSON file exists in authDir, it takes precedence and suppresses the
// auto-imported `cmdc login` CLI credential.
func TestSynthesizeCommandCode_AuthFileBeatsCLICredential(t *testing.T) {
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("cmdc-cli-key-should-be-suppressed"))
	defer cleanup()

	authDir := t.TempDir()
	authPayload := map[string]any{
		"type":      "commandcode",
		"api_key":   "cmdc-authfile-key",
		"user_name": "developer",
		"label":     "custom-commandcode",
	}
	raw, _ := json.Marshal(authPayload)
	if err := os.WriteFile(filepath.Join(authDir, "commandcode-test.json"), raw, 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	cfg := &config.Config{}
	ctx := &SynthesisContext{
		Config:      cfg,
		AuthDir:     authDir,
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	// 1. Config synthesizer should produce 0 commandcode auths because auth file exists in authDir
	configSynth := NewConfigSynthesizer()
	configAuths, err := configSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("config Synthesize error: %v", err)
	}
	for _, a := range configAuths {
		if a.Provider == constant.CommandCode {
			t.Errorf("ConfigSynthesizer should not produce CLI auth when auth file exists, got: %+v", a)
		}
	}

	// 2. File synthesizer should produce the file auth
	fileSynth := NewFileSynthesizer()
	fileAuths, err := fileSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("file Synthesize error: %v", err)
	}
	if len(fileAuths) != 1 {
		t.Fatalf("expected 1 file auth, got %d", len(fileAuths))
	}
	a := fileAuths[0]
	if a.Provider != constant.CommandCode {
		t.Errorf("provider = %q, want %q", a.Provider, constant.CommandCode)
	}
	if a.Attributes["api_key"] != "cmdc-authfile-key" {
		t.Errorf("api_key = %q, want cmdc-authfile-key", a.Attributes["api_key"])
	}
	if a.Label != "custom-commandcode" {
		t.Errorf("label = %q, want custom-commandcode", a.Label)
	}
}

// TestSynthesizeCommandCode_EnvKeyBeatsCLICredential verifies Level 3 vs Level 4:
// an explicit environment variable (COMMANDCODE_API_KEY) takes precedence over
// the external auto-discovery fallback (~/.commandcode/auth.json).
func TestSynthesizeCommandCode_EnvKeyBeatsCLICredential(t *testing.T) {
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("cmdc-cli-key-should-be-suppressed"))
	defer cleanup()

	t.Setenv("COMMANDCODE_API_KEY", "cmdc-env-explicit-key")

	cfg := &config.Config{}
	ctx := &SynthesisContext{
		Config:      cfg,
		AuthDir:     t.TempDir(),
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	configSynth := NewConfigSynthesizer()
	auths, err := configSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	var cmdAuths []*coreauth.Auth
	for _, a := range auths {
		if a.Provider == constant.CommandCode {
			cmdAuths = append(cmdAuths, a)
		}
	}
	if len(cmdAuths) != 1 {
		t.Fatalf("expected exactly 1 commandcode auth from env, got %d", len(cmdAuths))
	}
	if got := cmdAuths[0].Attributes["api_key"]; got != "cmdc-env-explicit-key" {
		t.Errorf("expected env key %q, got %q", "cmdc-env-explicit-key", got)
	}
	if got := cmdAuths[0].Attributes["source"]; got != "env:commandcode" {
		t.Errorf("expected source env:commandcode, got %q", got)
	}
}

// Final semantic contract:
//   - DIFFERENT keys across explicit sources (config / managed auth-dir / env) coexist as separate accounts.
//   - The SAME key across sources dedupes to the highest-precedence source (config > managed auth > env).
//   - ~/.commandcode/auth.json is only imported when NO explicit credential exists.

// collectCommandCodeAuths collects only commandcode provider auths from both synthesized slices.
func collectCommandCodeAuths(configAuths, fileAuths []*coreauth.Auth) []*coreauth.Auth {
	var cmdAuths []*coreauth.Auth
	for _, a := range configAuths {
		if a != nil && a.Provider == constant.CommandCode {
			cmdAuths = append(cmdAuths, a)
		}
	}
	for _, a := range fileAuths {
		if a != nil && a.Provider == constant.CommandCode {
			cmdAuths = append(cmdAuths, a)
		}
	}
	return cmdAuths
}

func writeCommandCodeAuthFile(t *testing.T, authDir, name, apiKey string) {
	t.Helper()
	payload := map[string]any{
		"type":    "commandcode",
		"api_key": apiKey,
		"label":   name,
	}
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(authDir, name+".json"), raw, 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
}

// TestCommandCode_ThreeDistinctExplicitAccounts ensures Config Key A + AuthDir Key B + Env Key C
// all coexist as three separate active clients.
func TestCommandCode_ThreeDistinctExplicitAccounts(t *testing.T) {
	authDir := t.TempDir()
	writeCommandCodeAuthFile(t, authDir, "commandcode-b", "key-B")

	t.Setenv("COMMANDCODE_API_KEY", "key-C")

	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{{APIKey: "key-A", Prefix: "accA"}},
	}
	ctx := &SynthesisContext{
		Config:      cfg,
		AuthDir:     authDir,
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	configSynth := NewConfigSynthesizer()
	configAuths, err := configSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("config Synthesize error: %v", err)
	}
	fileSynth := NewFileSynthesizer()
	fileAuths, err := fileSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("file Synthesize error: %v", err)
	}

	cmdAuths := collectCommandCodeAuths(configAuths, fileAuths)
	if len(cmdAuths) != 3 {
		t.Fatalf("expected 3 distinct commandcode accounts (config A, auth-dir B, env C), got %d: %+v", len(cmdAuths), cmdAuths)
	}
}

// TestCommandCode_SameKeyAcrossSourcesDedup verifies config/auth-dir/env all equal Key A
// result in exactly ONE active client, with config winning.
func TestCommandCode_SameKeyAcrossSourcesDedup(t *testing.T) {
	authDir := t.TempDir()
	writeCommandCodeAuthFile(t, authDir, "commandcode-dup", "key-A")

	t.Setenv("COMMANDCODE_API_KEY", "key-A")

	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{{APIKey: "key-A", Prefix: "main"}},
	}
	ctx := &SynthesisContext{
		Config:      cfg,
		AuthDir:     authDir,
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	configSynth := NewConfigSynthesizer()
	configAuths, err := configSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("config Synthesize error: %v", err)
	}
	fileSynth := NewFileSynthesizer()
	fileAuths, err := fileSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("file Synthesize error: %v", err)
	}

	cmdAuths := collectCommandCodeAuths(configAuths, fileAuths)
	if len(cmdAuths) != 1 {
		t.Fatalf("expected EXACTLY 1 commandcode client with same key A, got %d: %+v", len(cmdAuths), cmdAuths)
	}
	if !strings.HasPrefix(cmdAuths[0].Attributes["source"], "config:commandcode") {
		t.Errorf("expected config source to win, got source: %q", cmdAuths[0].Attributes["source"])
	}
}

// TestCommandCode_NoExplicitImportsAuthJSON ensures that with NO explicit credential,
// ~/.commandcode/auth.json Key D is imported as the active client.
func TestCommandCode_NoExplicitImportsAuthJSON(t *testing.T) {
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("key-D"))
	defer cleanup()

	// No config key, no auth-dir file, no env.
	cfg := &config.Config{}
	ctx := &SynthesisContext{
		Config:      cfg,
		AuthDir:     t.TempDir(),
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	configSynth := NewConfigSynthesizer()
	configAuths, err := configSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("config Synthesize error: %v", err)
	}
	fileSynth := NewFileSynthesizer()
	fileAuths, err := fileSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("file Synthesize error: %v", err)
	}

	cmdAuths := collectCommandCodeAuths(configAuths, fileAuths)
	if len(cmdAuths) != 1 {
		t.Fatalf("expected auth.json Key D to be imported, got %d: %+v", len(cmdAuths), cmdAuths)
	}
	if got := cmdAuths[0].Attributes["api_key"]; got != "key-D" {
		t.Errorf("expected key D, got %q", got)
	}
	if cmdAuths[0].Attributes["source"] != "cli:commandcode" {
		t.Errorf("expected source cli:commandcode, got %q", cmdAuths[0].Attributes["source"])
	}
}

// TestCommandCode_ExplicitSuppressesAuthJSON ensures that when an explicit credential exists,
// ~/.commandcode/auth.json is NOT imported as an extra implicit client.
func TestCommandCode_ExplicitSuppressesAuthJSON(t *testing.T) {
	cleanup := writeCommandCodeCLIAuth(t, fakeCommandCodeCLICredential("key-D"))
	defer cleanup()

	// Explicit config credential present.
	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{{APIKey: "key-A", Prefix: "main"}},
	}
	ctx := &SynthesisContext{
		Config:      cfg,
		AuthDir:     t.TempDir(),
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	configSynth := NewConfigSynthesizer()
	configAuths, err := configSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("config Synthesize error: %v", err)
	}
	fileSynth := NewFileSynthesizer()
	fileAuths, err := fileSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("file Synthesize error: %v", err)
	}

	cmdAuths := collectCommandCodeAuths(configAuths, fileAuths)
	if len(cmdAuths) != 1 {
		t.Fatalf("expected ONLY explicit key A, no implicit auth.json; got %d: %+v", len(cmdAuths), cmdAuths)
	}
	if got := cmdAuths[0].Attributes["api_key"]; got != "key-A" {
		t.Errorf("expected explicit key A, got %q", got)
	}
}

// TestCommandCode_DuplicateWithinAndAcrossSources ensures no repeated clients are produced
// from duplicate entries within config and across config/auth-dir.
func TestCommandCode_DuplicateWithinAndAcrossSources(t *testing.T) {
	authDir := t.TempDir()
	writeCommandCodeAuthFile(t, authDir, "commandcode-x", "key-X")

	// Duplicate config entries for the same key + base + prefix.
	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{
			{APIKey: "key-X", Prefix: "p1"},
			{APIKey: "key-X", Prefix: "p1"},
			{APIKey: "key-X", Prefix: "p2"}, // different prefix: distinct account, kept
		},
	}
	cfg.SanitizeCommandCodeKeys()
	if len(cfg.CommandCodeKey) != 2 {
		t.Fatalf("expected 2 config keys after dedup (p1+p2), got %d", len(cfg.CommandCodeKey))
	}

	ctx := &SynthesisContext{
		Config:      cfg,
		AuthDir:     authDir,
		Now:         time.Unix(1700000000, 0),
		IDGenerator: NewStableIDGenerator(),
	}

	configSynth := NewConfigSynthesizer()
	configAuths, err := configSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("config Synthesize error: %v", err)
	}
	fileSynth := NewFileSynthesizer()
	fileAuths, err := fileSynth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("file Synthesize error: %v", err)
	}

	cmdAuths := collectCommandCodeAuths(configAuths, fileAuths)
	// config key-X (p1) + config key-X (p2) = 2 distinct; auth-dir key-X file deduped against config.
	if len(cmdAuths) != 2 {
		t.Fatalf("expected 2 distinct clients after dedup (config p1, config p2, auth-dir deduped), got %d: %+v", len(cmdAuths), cmdAuths)
	}
}

// TestSynthesizeCommandCode_FileAuthVariants verifies that supported JSON field
// shapes (api_key and apiKey) are correctly parsed, while unrelated ones are rejected.
func TestSynthesizeCommandCode_FileAuthVariants(t *testing.T) {
	cases := []struct {
		name       string
		payload    map[string]any
		wantKey    string
		expectAuth bool
	}{
		{
			name:       "snake_case_api_key",
			payload:    map[string]any{"type": "commandcode", "api_key": "key-1"},
			wantKey:    "key-1",
			expectAuth: true,
		},
		{
			name:       "camelCase_apiKey",
			payload:    map[string]any{"type": "commandcode", "apiKey": "key-2"},
			wantKey:    "key-2",
			expectAuth: true,
		},
		{
			name:       "unsupported_oauth_token_field",
			payload:    map[string]any{"type": "commandcode", "token": "key-3"},
			expectAuth: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authDir := t.TempDir()
			raw, _ := json.Marshal(tc.payload)
			if err := os.WriteFile(filepath.Join(authDir, "test.json"), raw, 0600); err != nil {
				t.Fatalf("write auth file: %v", err)
			}
			ctx := &SynthesisContext{
				Config:      &config.Config{},
				AuthDir:     authDir,
				Now:         time.Unix(1700000000, 0),
				IDGenerator: NewStableIDGenerator(),
			}
			fileSynth := NewFileSynthesizer()
			auths, err := fileSynth.Synthesize(ctx)
			if err != nil {
				t.Fatalf("Synthesize error: %v", err)
			}
			if !tc.expectAuth {
				if len(auths) > 0 && auths[0].Attributes["api_key"] != "" {
					t.Fatalf("expected no valid api_key auth, got: %+v", auths[0])
				}
				return
			}
			if len(auths) != 1 {
				t.Fatalf("expected 1 auth, got %d", len(auths))
			}
			if auths[0].Attributes["api_key"] != tc.wantKey {
				t.Errorf("api_key = %q, want %q", auths[0].Attributes["api_key"], tc.wantKey)
			}
		})
	}
}

// TestSynthesizeCommandCode_Deduplication verifies that duplicate config entries
// are deduplicated during normalization.
func TestSynthesizeCommandCode_Deduplication(t *testing.T) {
	cfg := &config.Config{
		CommandCodeKey: []config.CommandCodeKey{
			{APIKey: "key-dup", BaseURL: "https://api.commandcode.ai", Prefix: "p1"},
			{APIKey: "key-dup", BaseURL: "https://api.commandcode.ai", Prefix: "p1"},
			{APIKey: "key-dup", BaseURL: "https://api.commandcode.ai", Prefix: "p2"}, // different prefix: keep
			{APIKey: "key-diff", BaseURL: "https://api.commandcode.ai", Prefix: "p1"},
		},
	}
	cfg.SanitizeCommandCodeKeys()
	if len(cfg.CommandCodeKey) != 3 {
		t.Fatalf("expected 3 keys after deduplication, got %d", len(cfg.CommandCodeKey))
	}
}
