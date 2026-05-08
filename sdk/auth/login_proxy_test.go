package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestCloneCfgWithProxy_NilCfg(t *testing.T) {
	if got := CloneCfgWithProxy(nil, "socks5://x:1"); got != nil {
		t.Fatalf("expected nil pass-through when cfg is nil")
	}
}

func TestCloneCfgWithProxy_EmptyProxyReturnsSamePointer(t *testing.T) {
	cfg := &config.Config{}
	cfg.SDKConfig.ProxyURL = "http://global:8080"

	if got := CloneCfgWithProxy(cfg, ""); got != cfg {
		t.Fatalf("expected same cfg pointer when proxyURL is empty")
	}
	if got := CloneCfgWithProxy(cfg, "   \t"); got != cfg {
		t.Fatalf("expected same cfg pointer when proxyURL is whitespace")
	}
}

func TestCloneCfgWithProxy_NonEmptyOverrides(t *testing.T) {
	cfg := &config.Config{}
	cfg.SDKConfig.ProxyURL = "http://global:8080"

	got := CloneCfgWithProxy(cfg, "socks5://login:1080")
	if got == nil {
		t.Fatalf("expected non-nil clone")
	}
	if got == cfg {
		t.Fatalf("expected fresh copy, got same pointer")
	}
	if got.ProxyURL != "socks5://login:1080" {
		t.Fatalf("expected ProxyURL=socks5://login:1080 on clone, got %q", got.ProxyURL)
	}
	if cfg.ProxyURL != "http://global:8080" {
		t.Fatalf("original cfg.ProxyURL should not be mutated, got %q", cfg.ProxyURL)
	}
}

func TestCloneCfgWithProxy_TrimsWhitespace(t *testing.T) {
	cfg := &config.Config{}
	got := CloneCfgWithProxy(cfg, "  socks5://h:1  ")
	if got == nil || got.ProxyURL != "socks5://h:1" {
		t.Fatalf("expected trimmed ProxyURL=socks5://h:1, got %v", got)
	}
}

func TestCloneCfgWithProxy_DirectKeywordPersistedVerbatim(t *testing.T) {
	cfg := &config.Config{}
	cfg.SDKConfig.ProxyURL = "http://global:8080"

	got := CloneCfgWithProxy(cfg, "direct")
	if got.ProxyURL != "direct" {
		t.Fatalf("expected ProxyURL='direct' to be preserved, got %q", got.ProxyURL)
	}
}
