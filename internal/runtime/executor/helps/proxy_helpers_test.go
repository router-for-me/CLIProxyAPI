package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

// TestNewProxyAwareHTTPClientFallsBackToConfigTimeout verifies that when a caller
// passes timeout 0 (the legacy no-timeout value), the client picks up a
// globally configured RequestTimeoutSeconds. This bounds how long a single
// upstream request can hang on a flaky connection, which is the necessary
// condition to prevent the process-wide wedge described in issue #3944.
func TestNewProxyAwareHTTPClientFallsBackToConfigTimeout(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{RequestTimeoutSeconds: 42},
		nil,
		0, // caller keeps legacy no-timeout behavior
	)

	if client.Timeout != 42*time.Second {
		t.Fatalf("client.Timeout = %v, want %v (cfg.RequestTimeoutSeconds fallback)", client.Timeout, 42*time.Second)
	}
}

// TestNewProxyAwareHTTPClientExplicitTimeoutWinsOverConfig ensures a caller
// that passes a positive timeout still takes precedence over the config value.
func TestNewProxyAwareHTTPClientExplicitTimeoutWinsOverConfig(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{RequestTimeoutSeconds: 42},
		nil,
		7*time.Second,
	)

	if client.Timeout != 7*time.Second {
		t.Fatalf("client.Timeout = %v, want %v (explicit caller timeout must win)", client.Timeout, 7*time.Second)
	}
}

// TestNewProxyAwareHTTPClientZeroTimeoutNoConfigKeepsLegacy ensures the legacy
// no-timeout behavior is preserved when neither the caller nor the config set a
// timeout (backward compatible default).
func TestNewProxyAwareHTTPClientZeroTimeoutNoConfigKeepsLegacy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{}, // RequestTimeoutSeconds == 0
		nil,
		0,
	)

	if client.Timeout != 0 {
		t.Fatalf("client.Timeout = %v, want 0 (legacy no-timeout when unconfigured)", client.Timeout)
	}
}
