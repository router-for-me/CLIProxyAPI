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
func TestAPICallTransport_InheritsEnvironmentProxyWhenNoExplicitProxyConfigured(t *testing.T) {
        t.Setenv("HTTP_PROXY", "http://127.0.0.1:18080")
        t.Setenv("HTTPS_PROXY", "http://127.0.0.1:18443")
        t.Setenv("NO_PROXY", "")

        h := &Handler{
                cfg: &config.Config{
                        SDKConfig: sdkconfig.SDKConfig{ProxyURL: ""},
                },
        }

        rt := h.apiCallTransport(&coreauth.Auth{ProxyURL: ""})
        transport, ok := rt.(*http.Transport)
        if ok && transport.Proxy != nil {
                tests := []struct {
                        name string
                        raw  string
                        want string
                }{
                        {name: "http request uses HTTP_PROXY", raw: "http://example.com", want: "http://127.0.0.1:18080"},
                        {name: "https request uses HTTPS_PROXY", raw: "https://example.com", want: "http://127.0.0.1:18443"},
                }
                for _, tt := range tests {
                        t.Run(tt.name, func(t *testing.T) {
                                req, err := http.NewRequest("GET", tt.raw, nil)
                                if err != nil {
                                        t.Fatalf("new request: %v", err)
                                }
                                proxyURL, err := transport.Proxy(req)
                                if err != nil {
                                        t.Fatalf("proxy func returned error: %v", err)
                                }
                                if proxyURL == nil {
                                        t.Fatal("expected environment proxy to be inherited, got nil")
                                }
                                if got := proxyURL.String(); got != tt.want {
                                        t.Fatalf("expected proxy %q, got %q", tt.want, got)
                                }
                        })
                }
                return
        }
        req, err := http.NewRequest("GET", "https://example.com", nil)
        if err != nil {
                t.Fatalf("new request: %v", err)
        }
        proxyURL, err := http.ProxyFromEnvironment(req)
        if err != nil {
                t.Fatalf("ProxyFromEnvironment error: %v", err)
        }
        if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:18443" {
                t.Fatalf("expected environment proxy http://127.0.0.1:18443, got %v", proxyURL)
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
