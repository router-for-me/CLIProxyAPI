package management

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestApplyAPICallDefaultHeadersUsesCodexConfigUserAgent(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			CodexHeaderDefaults: config.CodexHeaderDefaults{
				UserAgent: "codex-config-ua",
			},
		},
	}
	headers := map[string]string{}

	h.applyAPICallDefaultHeaders(headers, nil, "codex")

	if got := headers["User-Agent"]; got != "codex-config-ua" {
		t.Fatalf("User-Agent = %q, want %q", got, "codex-config-ua")
	}
}

func TestApplyAPICallDefaultHeadersUsesAuthCodexUserAgentBeforeConfig(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			CodexHeaderDefaults: config.CodexHeaderDefaults{
				UserAgent: "codex-config-ua",
			},
		},
	}
	headers := map[string]string{}
	auth := &coreauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"user_agent": "auth-file-ua",
		},
	}

	h.applyAPICallDefaultHeaders(headers, auth, "")

	if got := headers["User-Agent"]; got != "auth-file-ua" {
		t.Fatalf("User-Agent = %q, want %q", got, "auth-file-ua")
	}
}

func TestApplyAPICallDefaultHeadersUsesClaudeConfigUserAgent(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
				UserAgent: "claude-config-ua",
			},
		},
	}
	headers := map[string]string{}

	h.applyAPICallDefaultHeaders(headers, nil, "claude")

	if got := headers["User-Agent"]; got != "claude-config-ua" {
		t.Fatalf("User-Agent = %q, want %q", got, "claude-config-ua")
	}
}

func TestApplyAPICallDefaultHeadersKeepsExplicitUserAgent(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			CodexHeaderDefaults: config.CodexHeaderDefaults{
				UserAgent: "codex-config-ua",
			},
		},
	}
	headers := map[string]string{
		"user-agent": "explicit-ua",
	}

	h.applyAPICallDefaultHeaders(headers, nil, "codex")

	if got := headers["user-agent"]; got != "explicit-ua" {
		t.Fatalf("user-agent = %q, want %q", got, "explicit-ua")
	}
	if got := headers["User-Agent"]; got != "" {
		t.Fatalf("User-Agent = %q, want empty", got)
	}
}
