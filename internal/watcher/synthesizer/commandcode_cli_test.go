package synthesizer

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestSynthesizeCommandCodeCLI_ConfigKeyUsedOnlyWhenNoCLI(t *testing.T) {
	// CLI credential absent + explicit commandcode-api-key present: config path
	// (advanced override) must still produce an auth.
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
