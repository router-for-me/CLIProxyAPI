package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type proxyCapturingAuthenticator struct {
	provider string
	record   *coreauth.Auth
	seenCfg  *config.Config
}

func (a *proxyCapturingAuthenticator) Provider() string {
	return a.provider
}

func (a *proxyCapturingAuthenticator) Login(_ context.Context, cfg *config.Config, _ *LoginOptions) (*coreauth.Auth, error) {
	a.seenCfg = cfg
	return a.record, nil
}

func (a *proxyCapturingAuthenticator) RefreshLead() *time.Duration {
	return nil
}

func TestManagerLoginWithProxyOverridesFlowAndPersists(t *testing.T) {
	authDir := t.TempDir()
	fileName := "demo-proxy.json"
	proxyURL := "socks5://user:pass@127.0.0.1:1080"

	authenticator := &proxyCapturingAuthenticator{
		provider: "demo",
		record: &coreauth.Auth{
			ID:       fileName,
			FileName: fileName,
			Provider: "demo",
			Metadata: map[string]any{"type": "demo", "email": "user@example.com"},
		},
	}
	store := NewFileTokenStore()
	store.SetBaseDir(authDir)
	mgr := NewManager(store, authenticator)
	cfg := &config.Config{AuthDir: authDir, SDKConfig: config.SDKConfig{ProxyURL: "http://global.example:8080"}}

	record, _, err := mgr.Login(context.Background(), "demo", cfg, &LoginOptions{ProxyURL: proxyURL})
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if authenticator.seenCfg == nil || authenticator.seenCfg.ProxyURL != proxyURL {
		t.Fatalf("authenticator cfg proxy = %v, want %q", authenticator.seenCfg, proxyURL)
	}
	if cfg.ProxyURL != "http://global.example:8080" {
		t.Fatalf("caller cfg mutated: %q", cfg.ProxyURL)
	}
	if record.ProxyURL != proxyURL {
		t.Fatalf("record.ProxyURL = %q, want %q", record.ProxyURL, proxyURL)
	}

	raw, errRead := os.ReadFile(filepath.Join(authDir, fileName))
	if errRead != nil {
		t.Fatalf("read saved auth: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(raw, &saved); errUnmarshal != nil {
		t.Fatalf("unmarshal saved auth: %v", errUnmarshal)
	}
	if saved["proxy_url"] != proxyURL {
		t.Fatalf("saved proxy_url = %v, want %q", saved["proxy_url"], proxyURL)
	}
}

func TestManagerLoginWithoutProxyLeavesRecordUntouched(t *testing.T) {
	authDir := t.TempDir()
	fileName := "demo-noproxy.json"
	authenticator := &proxyCapturingAuthenticator{
		provider: "demo",
		record: &coreauth.Auth{
			ID:       fileName,
			FileName: fileName,
			Provider: "demo",
			Metadata: map[string]any{"type": "demo"},
		},
	}
	store := NewFileTokenStore()
	store.SetBaseDir(authDir)
	mgr := NewManager(store, authenticator)
	cfg := &config.Config{AuthDir: authDir}

	if _, _, err := mgr.Login(context.Background(), "demo", cfg, &LoginOptions{}); err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if authenticator.seenCfg != cfg {
		t.Fatalf("authenticator received a different cfg without a login proxy")
	}
	raw, errRead := os.ReadFile(filepath.Join(authDir, fileName))
	if errRead != nil {
		t.Fatalf("read saved auth: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(raw, &saved); errUnmarshal != nil {
		t.Fatalf("unmarshal saved auth: %v", errUnmarshal)
	}
	if _, ok := saved["proxy_url"]; ok {
		t.Fatalf("saved auth unexpectedly contains proxy_url: %v", saved["proxy_url"])
	}
}

func TestManagerLoginRejectsInvalidProxy(t *testing.T) {
	authenticator := &proxyCapturingAuthenticator{provider: "demo", record: &coreauth.Auth{ID: "x.json", Provider: "demo"}}
	mgr := NewManager(NewFileTokenStore(), authenticator)
	for _, bad := range []string{"ftp://127.0.0.1:21", "direct", "not a url", "http://127.0.0.1:99999"} {
		if _, _, err := mgr.Login(context.Background(), "demo", &config.Config{AuthDir: t.TempDir()}, &LoginOptions{ProxyURL: bad}); err == nil {
			t.Fatalf("proxy %q: expected error", bad)
		}
	}
	if authenticator.seenCfg != nil {
		t.Fatalf("authenticator ran despite invalid proxy")
	}
}
