package util

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestResolveProxyURL_ProviderTakesPrecedence(t *testing.T) {
	t.Parallel()

	cfg := &config.SDKConfig{
		ProxyURL: "http://global.example.com:8080",
		ProxyByProvider: map[string]string{
			"claude": "socks5://provider.example.com:1080",
		},
	}

	got := ResolveProxyURL(cfg, "Claude")
	want := "socks5://provider.example.com:1080"
	if got != want {
		t.Fatalf("ResolveProxyURL() = %q, want %q", got, want)
	}
}

func TestResolveProxyURL_FallsBackToGlobal(t *testing.T) {
	t.Parallel()

	cfg := &config.SDKConfig{
		ProxyURL: "http://global.example.com:8080",
		ProxyByProvider: map[string]string{
			"claude": "socks5://provider.example.com:1080",
		},
	}

	got := ResolveProxyURL(cfg, "codex")
	want := "http://global.example.com:8080"
	if got != want {
		t.Fatalf("ResolveProxyURL() = %q, want %q", got, want)
	}
}

func TestResolveProxyURL_DirectOverride(t *testing.T) {
	t.Parallel()

	cfg := &config.SDKConfig{
		ProxyURL: "http://global.example.com:8080",
		ProxyByProvider: map[string]string{
			"gemini-cli": "direct",
		},
	}

	got := ResolveProxyURL(cfg, "gemini-cli")
	if got != "direct" {
		t.Fatalf("ResolveProxyURL() = %q, want %q", got, "direct")
	}
}

func TestResolveProxyURL_NilConfig(t *testing.T) {
	t.Parallel()

	if got := ResolveProxyURL(nil, "claude"); got != "" {
		t.Fatalf("ResolveProxyURL(nil) = %q, want empty", got)
	}
}
