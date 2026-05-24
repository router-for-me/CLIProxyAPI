package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSynthesizeGrokAuth_ProducesAuthWithProviderGrok(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			GrokAuth: []config.GrokAuth{
				{
					Name:         "alice-supergrok",
					Email:        "alice@example.com",
					AccountID:    "acct-123",
					AccessToken:  "eyJhbGciOiJSUzI1NiJ9.access",
					RefreshToken: "opaque-refresh-token",
					IDToken:      "eyJhbGciOiJSUzI1NiJ9.id",
					ExpiresAt:    "2026-05-23T18:00:00Z",
					Priority:     1,
				},
			},
		},
		Now:         time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC),
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
	if a.Provider != "grok" {
		t.Errorf("expected provider grok, got %s", a.Provider)
	}
	if a.Label != "grok-oauth" {
		t.Errorf("expected label grok-oauth, got %s", a.Label)
	}
	if a.Status != coreauth.StatusActive {
		t.Errorf("expected status active, got %s", a.Status)
	}
	if a.Attributes["access_token"] != "eyJhbGciOiJSUzI1NiJ9.access" {
		t.Errorf("expected access_token, got %s", a.Attributes["access_token"])
	}
	if a.Attributes["refresh_token"] != "opaque-refresh-token" {
		t.Errorf("expected refresh_token, got %s", a.Attributes["refresh_token"])
	}
	if a.Attributes["expired"] != "2026-05-23T18:00:00Z" {
		t.Errorf("expected expired=2026-05-23T18:00:00Z, got %s", a.Attributes["expired"])
	}
	if a.Attributes["email"] != "alice@example.com" {
		t.Errorf("expected email=alice@example.com, got %s", a.Attributes["email"])
	}
	if a.Attributes["account_id"] != "acct-123" {
		t.Errorf("expected account_id=acct-123, got %s", a.Attributes["account_id"])
	}
	if a.Attributes["id_token"] != "eyJhbGciOiJSUzI1NiJ9.id" {
		t.Errorf("expected id_token set, got %s", a.Attributes["id_token"])
	}
	if a.Attributes["priority"] != "1" {
		t.Errorf("expected priority=1, got %s", a.Attributes["priority"])
	}
	if a.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestSynthesizeGrokAuth_EmptyConfigReturnsNothing(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{GrokAuth: []config.GrokAuth{}},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range auths {
		if a.Provider == "grok" {
			t.Errorf("expected no grok auths, but got one with ID %s", a.ID)
		}
	}
}

func TestSynthesizeGrokAuth_SkipsEmptyAccessToken(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			GrokAuth: []config.GrokAuth{
				{Name: "no-token", AccessToken: ""},
				{Name: "whitespace-token", AccessToken: "   "},
				{Name: "valid", AccessToken: "eyJhbGciOiJSUzI1NiJ9.valid", RefreshToken: "r"},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	grokAuths := make([]*coreauth.Auth, 0)
	for _, a := range auths {
		if a.Provider == "grok" {
			grokAuths = append(grokAuths, a)
		}
	}
	if len(grokAuths) != 1 {
		t.Fatalf("expected 1 grok auth (empty tokens skipped), got %d", len(grokAuths))
	}
}

func TestSynthesizeGrokAuth_MultipleEntries(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			GrokAuth: []config.GrokAuth{
				{
					Name:         "alice",
					Email:        "alice@example.com",
					AccessToken:  "token-alice",
					RefreshToken: "refresh-alice",
				},
				{
					Name:         "bob",
					Email:        "bob@example.com",
					AccessToken:  "token-bob",
					RefreshToken: "refresh-bob",
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

	grokAuths := make([]*coreauth.Auth, 0)
	for _, a := range auths {
		if a.Provider == "grok" {
			grokAuths = append(grokAuths, a)
		}
	}
	if len(grokAuths) != 2 {
		t.Fatalf("expected 2 grok auths, got %d", len(grokAuths))
	}

	// IDs must be distinct
	if grokAuths[0].ID == grokAuths[1].ID {
		t.Errorf("expected distinct IDs, both are %s", grokAuths[0].ID)
	}

	// Each entry should carry its own access_token
	tokens := map[string]bool{
		grokAuths[0].Attributes["access_token"]: true,
		grokAuths[1].Attributes["access_token"]: true,
	}
	if len(tokens) != 2 {
		t.Error("expected distinct access_tokens on each auth entry")
	}
}
