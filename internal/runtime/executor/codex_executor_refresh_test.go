package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestCodexAuthServiceReusesProxyBucket(t *testing.T) {
	t.Parallel()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ProxyURL: "http://proxy.example.com:8080"}

	first := executor.codexAuthService(auth)
	second := executor.codexAuthService(auth)

	if first != second {
		t.Fatal("expected codex auth service to be reused for the same proxy URL")
	}
}

func TestCodexAuthServiceSeparatesProxyBuckets(t *testing.T) {
	t.Parallel()

	executor := NewCodexExecutor(&config.Config{})

	first := executor.codexAuthService(&cliproxyauth.Auth{ProxyURL: "http://proxy-a.example.com:8080"})
	second := executor.codexAuthService(&cliproxyauth.Auth{ProxyURL: "http://proxy-b.example.com:8080"})

	if first == second {
		t.Fatal("expected distinct codex auth services for different proxy URLs")
	}
}

func TestCodexAuthServiceFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	executor := NewCodexExecutor(&config.Config{
		SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
	})

	first := executor.codexAuthService(&cliproxyauth.Auth{})
	second := executor.codexAuthService(nil)

	if first != second {
		t.Fatal("expected empty auth proxy URLs to share the config proxy bucket")
	}
}
