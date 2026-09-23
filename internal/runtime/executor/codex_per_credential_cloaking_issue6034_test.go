package executor

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func boolPtr(b bool) *bool {
	return &b
}

func TestCodexPerCredentialDisableCloaking_HTTP_ExplicitTrue(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: false,
		},
		CodexKey: []config.CodexKey{
			{
				APIKey:               "custom-key",
				BaseURL:              "https://example.com/v1",
				DisableCodexCloaking: boolPtr(true),
			},
		},
	}

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":                "custom-key",
			"base_url":               "https://example.com/v1",
			"header:User-Agent":      "CustomAgent/1.0",
			"header:Originator":      "CustomOriginator",
			"codex_disable_cloaking": "true",
		},
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest failed: %v", err)
	}

	applyCodexHeadersFromSources(req, auth, "test-token", false, cfg, nil)

	if got := req.Header.Get("User-Agent"); got != "CustomAgent/1.0" {
		t.Errorf("User-Agent = %q, want %q", got, "CustomAgent/1.0")
	}
	if got := req.Header.Get("Originator"); got != "CustomOriginator" {
		t.Errorf("Originator = %q, want %q", got, "CustomOriginator")
	}
}

func TestCodexPerCredentialDisableCloaking_HTTP_ExplicitFalseOverridesGlobalTrue(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: true,
		},
		CodexKey: []config.CodexKey{
			{
				APIKey:               "cloaked-key",
				BaseURL:              "https://example.com/v1",
				DisableCodexCloaking: boolPtr(false),
			},
		},
	}

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":                "cloaked-key",
			"base_url":               "https://example.com/v1",
			"header:User-Agent":      "CustomAgent/1.0",
			"header:Originator":      "CustomOriginator",
			"codex_disable_cloaking": "false",
		},
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest failed: %v", err)
	}

	applyCodexHeadersFromSources(req, auth, "test-token", false, cfg, nil)

	// Since disable-codex-cloaking is explicitly false on the credential, cloaking should be enforced.
	if got := req.Header.Get("User-Agent"); got != codexUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, codexUserAgent)
	}
	if got := req.Header.Get("Originator"); got != codexOriginator {
		t.Errorf("Originator = %q, want %q", got, codexOriginator)
	}
}

func TestCodexPerCredentialDisableCloaking_HTTP_FallbackToGlobal(t *testing.T) {
	// 1. Global true, credential nil -> cloaking disabled
	cfgGlobalTrue := &config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: true,
		},
		CodexKey: []config.CodexKey{
			{
				APIKey:  "key-nil",
				BaseURL: "https://example.com/v1",
			},
		},
	}

	authNil := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":           "key-nil",
			"base_url":          "https://example.com/v1",
			"header:User-Agent": "CustomAgent/1.0",
			"header:Originator": "CustomOriginator",
		},
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest failed: %v", err)
	}
	applyCodexHeadersFromSources(req, authNil, "test-token", false, cfgGlobalTrue, nil)
	if got := req.Header.Get("User-Agent"); got != "CustomAgent/1.0" {
		t.Errorf("Global true fallback User-Agent = %q, want %q", got, "CustomAgent/1.0")
	}

	// 2. Global false, credential nil -> cloaking enabled
	cfgGlobalFalse := &config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: false,
		},
		CodexKey: []config.CodexKey{
			{
				APIKey:  "key-nil",
				BaseURL: "https://example.com/v1",
			},
		},
	}

	req2, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest failed: %v", err)
	}
	applyCodexHeadersFromSources(req2, authNil, "test-token", false, cfgGlobalFalse, nil)
	if got := req2.Header.Get("User-Agent"); got != codexUserAgent {
		t.Errorf("Global false fallback User-Agent = %q, want %q", got, codexUserAgent)
	}
}

func TestCodexPerCredentialDisableCloaking_WebSocket_ExplicitTrue(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: false,
		},
	}

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":                "custom-key",
			"base_url":               "https://example.com/v1",
			"header:User-Agent":      "CustomWSAgent/1.0",
			"header:Originator":      "CustomWSOriginator",
			"codex_disable_cloaking": "true",
		},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), nil, auth, "test-token", cfg, true)

	if got := headers.Get("User-Agent"); got != "CustomWSAgent/1.0" {
		t.Errorf("WebSocket User-Agent = %q, want %q", got, "CustomWSAgent/1.0")
	}
	if got := headers.Get("Originator"); got != "CustomWSOriginator" {
		t.Errorf("WebSocket Originator = %q, want %q", got, "CustomWSOriginator")
	}
}

func TestCodexPerCredentialDisableCloaking_WebSocket_ExplicitFalseOverridesGlobalTrue(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: true,
		},
	}

	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":                "custom-key",
			"base_url":               "https://example.com/v1",
			"header:User-Agent":      "CustomWSAgent/1.0",
			"header:Originator":      "CustomWSOriginator",
			"codex_disable_cloaking": "false",
		},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), nil, auth, "test-token", cfg, true)

	if got := headers.Get("User-Agent"); got != codexUserAgent {
		t.Errorf("WebSocket User-Agent = %q, want %q", got, codexUserAgent)
	}
	if got := headers.Get("Originator"); got != codexOriginator {
		t.Errorf("WebSocket Originator = %q, want %q", got, codexOriginator)
	}
}

func TestCodexPerCredentialDisableCloaking_OAuthUnaffectedByAPIKeyOverride(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: false,
		},
		CodexKey: []config.CodexKey{
			{
				APIKey:               "custom-key",
				BaseURL:              "https://example.com/v1",
				DisableCodexCloaking: boolPtr(true),
			},
		},
	}

	// OAuth credential (does not match codex-api-key)
	oauthAuth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{
			"account_id": "acc-123",
		},
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest failed: %v", err)
	}

	applyCodexHeadersFromSources(req, oauthAuth, "oauth-token", false, cfg, nil)

	// OAuth credential must remain cloaked with official Codex identity
	if got := req.Header.Get("User-Agent"); got != codexUserAgent {
		t.Errorf("OAuth User-Agent = %q, want %q", got, codexUserAgent)
	}
	if got := req.Header.Get("Originator"); got != codexOriginator {
		t.Errorf("OAuth Originator = %q, want %q", got, codexOriginator)
	}
}

func TestCodexPerCredentialDisableCloaking_ResolvedFromConfigWithoutAttribute(t *testing.T) {
	cfg := &config.Config{
		Codex: config.CodexConfig{
			DisableCodexCloaking: false,
		},
		CodexKey: []config.CodexKey{
			{
				APIKey:               "lookup-key",
				BaseURL:              "https://example.com/v1",
				DisableCodexCloaking: boolPtr(true),
			},
		},
	}

	// Auth object with only APIKey/BaseURL attributes (no pre-synthesized codex_disable_cloaking attribute)
	authWithoutAttr := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":           "lookup-key",
			"base_url":          "https://example.com/v1",
			"header:User-Agent": "CustomLookupAgent/1.0",
		},
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("http.NewRequest failed: %v", err)
	}

	applyCodexHeadersFromSources(req, authWithoutAttr, "lookup-key", false, cfg, nil)

	// Since resolveCodexKeyConfig finds DisableCodexCloaking == true in cfg.CodexKey, cloaking must be disabled.
	if got := req.Header.Get("User-Agent"); got != "CustomLookupAgent/1.0" {
		t.Errorf("User-Agent = %q, want %q", got, "CustomLookupAgent/1.0")
	}
}
