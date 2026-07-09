package auth

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestManagerImplicitProxyBindingCyclesPerAccountCategory(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			ProxyURL:  "socks5://fallback-proxy.example.com:1080",
			ProxyURLs: []string{"socks5://proxy-a.example.com:1080", "socks5://proxy-b.example.com:1080"},
		},
	})

	auths := []*Auth{
		newProxyBindingTestAuth("codex-oauth-1", "codex", AuthKindOAuth, "001"),
		newProxyBindingTestAuth("codex-oauth-2", "codex", AuthKindOAuth, "002"),
		newProxyBindingTestAuth("codex-oauth-3", "codex", AuthKindOAuth, "003"),
		newProxyBindingTestAuth("claude-oauth-1", "claude", AuthKindOAuth, "001"),
		newProxyBindingTestAuth("codex-apikey-1", "codex", AuthKindAPIKey, "001"),
	}
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}

	assertEffectiveProxyURL(t, manager, "codex-oauth-1", "socks5://proxy-a.example.com:1080")
	assertEffectiveProxyURL(t, manager, "codex-oauth-2", "socks5://proxy-b.example.com:1080")
	assertEffectiveProxyURL(t, manager, "codex-oauth-3", "socks5://proxy-a.example.com:1080")
	assertEffectiveProxyURL(t, manager, "claude-oauth-1", "socks5://proxy-a.example.com:1080")
	assertEffectiveProxyURL(t, manager, "codex-apikey-1", "socks5://proxy-a.example.com:1080")
}

func TestManagerImplicitProxyBindingSkipsExplicitAuthProxy(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			ProxyURL:  "socks5://fallback-proxy.example.com:1080",
			ProxyURLs: []string{"socks5://proxy-a.example.com:1080", "socks5://proxy-b.example.com:1080"},
		},
	})

	explicit := newProxyBindingTestAuth("codex-oauth-explicit", "codex", AuthKindOAuth, "001")
	explicit.ProxyURL = "direct"
	if _, errRegister := manager.Register(context.Background(), explicit); errRegister != nil {
		t.Fatalf("Register(explicit) error = %v", errRegister)
	}
	implicit := newProxyBindingTestAuth("codex-oauth-implicit", "codex", AuthKindOAuth, "002")
	if _, errRegister := manager.Register(context.Background(), implicit); errRegister != nil {
		t.Fatalf("Register(implicit) error = %v", errRegister)
	}

	assertEffectiveProxyURL(t, manager, "codex-oauth-explicit", "direct")
	assertEffectiveProxyURL(t, manager, "codex-oauth-implicit", "socks5://proxy-a.example.com:1080")
}

func TestManagerImplicitProxyBindingUsesSingleProxyURLsEntry(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			ProxyURL:  "socks5://fallback-proxy.example.com:1080",
			ProxyURLs: []string{"socks5://proxy-a.example.com:1080"},
		},
	})

	auth := newProxyBindingTestAuth("codex-oauth-1", "codex", AuthKindOAuth, "001")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	assertEffectiveProxyURL(t, manager, "codex-oauth-1", "socks5://proxy-a.example.com:1080")
}

func TestManagerImplicitProxyBindingFallsBackToGlobalProxyURLWhenProxyURLsEmpty(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			ProxyURL: "socks5://fallback-proxy.example.com:1080",
		},
	})

	auth := newProxyBindingTestAuth("codex-oauth-1", "codex", AuthKindOAuth, "001")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	assertEffectiveProxyURLWithGlobal(t, manager, "codex-oauth-1", "socks5://fallback-proxy.example.com:1080", "socks5://fallback-proxy.example.com:1080")
}

func TestImplicitProxyBindingDoesNotSerialize(t *testing.T) {
	auth := &Auth{ID: "auth-1", Provider: "codex"}
	auth.SetImplicitProxyURL("socks5://proxy-a.example.com:1080")
	auth.SetImplicitProxyOrder("001")

	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(data), "proxy-a.example.com") {
		t.Fatalf("serialized auth leaked implicit proxy: %s", data)
	}
	if strings.Contains(string(data), "implicit") {
		t.Fatalf("serialized auth leaked implicit field name: %s", data)
	}
}

func newProxyBindingTestAuth(id, provider, kind, order string) *Auth {
	auth := &Auth{
		ID:       id,
		Provider: provider,
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind: kind,
		},
	}
	auth.SetImplicitProxyOrder(order)
	return auth
}

func assertEffectiveProxyURL(t *testing.T, manager *Manager, id string, want string) {
	t.Helper()
	assertEffectiveProxyURLWithGlobal(t, manager, id, "", want)
}

func assertEffectiveProxyURLWithGlobal(t *testing.T, manager *Manager, id string, globalProxyURL string, want string) {
	t.Helper()
	auth, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %s not found", id)
	}
	if got := EffectiveProxyURL(globalProxyURL, auth); got != want {
		t.Fatalf("EffectiveProxyURL(%s) = %q, want %q", id, got, want)
	}
}
