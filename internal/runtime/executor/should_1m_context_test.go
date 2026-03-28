package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestShould1MContext_GlobalEnabledAccountEnabledWhitelistedModel(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
			Models:  []string{"claude-opus-4-6", "claude-sonnet-4-6"},
		},
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"enable_1m_context": "true"},
	}
	if !should1MContext(auth, cfg, "claude-opus-4-6") {
		t.Fatal("expected true for enabled config + enabled account + whitelisted model")
	}
}

func TestShould1MContext_AccountDisabled(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
			Models:  []string{"claude-opus-4-6"},
		},
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
	}
	if should1MContext(auth, cfg, "claude-opus-4-6") {
		t.Fatal("expected false when account does not have enable_1m_context")
	}
}

func TestShould1MContext_GlobalDisabled(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: false,
			Models:  []string{"claude-opus-4-6"},
		},
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"enable_1m_context": "true"},
	}
	if should1MContext(auth, cfg, "claude-opus-4-6") {
		t.Fatal("expected false when global config is disabled")
	}
}

func TestShould1MContext_NonWhitelistedModel(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
			Models:  []string{"claude-opus-4-6"},
		},
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"enable_1m_context": "true"},
	}
	if should1MContext(auth, cfg, "claude-sonnet-4-5-20250929") {
		t.Fatal("expected false for model not in whitelist")
	}
}

func TestShould1MContext_EmptyWhitelistAllowsAll(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
			Models:  nil,
		},
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"enable_1m_context": "true"},
	}
	if !should1MContext(auth, cfg, "any-model-name") {
		t.Fatal("expected true when models whitelist is empty (allow all)")
	}
}

func TestShould1MContext_MetadataFallback_Bool(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
		},
	}
	// UploadAuthFile path: Attributes is empty, Metadata has the value
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"enable_1m_context": true},
	}
	if !should1MContext(auth, cfg, "claude-opus-4-6") {
		t.Fatal("expected true when enable_1m_context is in Metadata as bool")
	}
}

func TestShould1MContext_MetadataFallback_String(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
		},
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"enable_1m_context": "true"},
	}
	if !should1MContext(auth, cfg, "claude-opus-4-6") {
		t.Fatal("expected true when enable_1m_context is in Metadata as string")
	}
}

func TestShould1MContext_MetadataFalse(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
		},
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"enable_1m_context": false},
	}
	if should1MContext(auth, cfg, "claude-opus-4-6") {
		t.Fatal("expected false when Metadata enable_1m_context is false")
	}
}

func TestShould1MContext_NilAuth(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
		},
	}
	if should1MContext(nil, cfg, "claude-opus-4-6") {
		t.Fatal("expected false for nil auth")
	}
}

func TestShould1MContext_NilConfig(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"enable_1m_context": "true"},
	}
	if should1MContext(auth, nil, "claude-opus-4-6") {
		t.Fatal("expected false for nil config")
	}
}

func TestShould1MContext_AttributesTakesPrecedenceOverMetadata(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
		},
	}
	// Attributes says true, Metadata says false — Attributes wins
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"enable_1m_context": "true"},
		Metadata:   map[string]any{"enable_1m_context": false},
	}
	if !should1MContext(auth, cfg, "claude-opus-4-6") {
		t.Fatal("expected true: Attributes should take precedence over Metadata")
	}
}

func TestShould1MContext_CaseInsensitiveModel(t *testing.T) {
	cfg := &config.Config{
		Claude1MContext: config.Claude1MContext{
			Enabled: true,
			Models:  []string{"Claude-Opus-4-6"},
		},
	}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"enable_1m_context": "true"},
	}
	if !should1MContext(auth, cfg, "claude-opus-4-6") {
		t.Fatal("expected true: model whitelist matching should be case-insensitive")
	}
}
