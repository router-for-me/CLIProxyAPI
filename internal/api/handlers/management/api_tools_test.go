package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type apiCallRoundTripFunc func(*http.Request) (*http.Response, error)

func (f apiCallRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func stubAPICallFingerprintTransport(t *testing.T, proxy *string, trip apiCallRoundTripFunc) {
	t.Helper()
	original := newAPICallFingerprintTransport
	t.Cleanup(func() {
		newAPICallFingerprintTransport = original
	})
	newAPICallFingerprintTransport = func(proxyURL string, fallback http.RoundTripper) http.RoundTripper {
		if proxy != nil {
			*proxy = proxyURL
		}
		return apiCallRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if helps.IsChatGPTUpstreamURL(req.URL) {
				return trip(req)
			}
			if fallback == nil {
				return nil, io.EOF
			}
			return fallback.RoundTrip(req)
		})
	}
}

func TestAPICallUsesRequestProxyURL(t *testing.T) {
	t.Parallel()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxyServer.Close()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:1"},
		},
	}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"method":"GET","url":"http://upstream.invalid/test","proxy_url":"` + proxyServer.URL + `"}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response apiCallResponse
	if errDecode := json.NewDecoder(recorder.Body).Decode(&response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upstream status code = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if response.Body != "proxied" {
		t.Fatalf("upstream body = %q, want %q", response.Body, "proxied")
	}
}

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"}, "")
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

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"}, "")
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

func TestAPICallTransportRequestProxyOverridesCredentialAndGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, " http://request-proxy.example.com:8080 ")
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
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://request-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportInvalidRequestProxyDoesNotFallBack(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, "bad-value")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected invalid request proxy to avoid lower-priority proxy settings")
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			XAIKey: []config.XAIKey{{
				APIKey:   "xai-key",
				ProxyURL: "http://xai-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "xai",
			auth: &coreauth.Auth{
				Provider:   "xai",
				Attributes: map[string]string{"api_key": "xai-key"},
			},
			wantProxy: "http://xai-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth, "")
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
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
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

func TestApplyChatGPTAPICallHeaderDefaults(t *testing.T) {
	t.Parallel()

	t.Run("fills missing chatgpt headers", func(t *testing.T) {
		t.Parallel()
		req, errRequest := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/subscriptions?account_id=acc", nil)
		if errRequest != nil {
			t.Fatalf("new request: %v", errRequest)
		}
		req.Header.Set("Authorization", "Bearer caller-token")
		applyChatGPTAPICallHeaderDefaults(req)

		if got := req.Header.Get("Authorization"); got != "Bearer caller-token" {
			t.Fatalf("Authorization = %q, want caller token", got)
		}
		if got := req.Header.Get("User-Agent"); got != chromeAPICallUserAgent {
			t.Fatalf("User-Agent = %q, want chrome default", got)
		}
		if got := req.Header.Get("Accept"); got != "*/*" {
			t.Fatalf("Accept = %q, want */*", got)
		}
		if got := req.Header.Get("Accept-Language"); got != "en-US,en;q=0.9" {
			t.Fatalf("Accept-Language = %q, want en-US,en;q=0.9", got)
		}
		if got := req.Header.Get("OAI-Language"); got != "en-US" {
			t.Fatalf("OAI-Language = %q, want en-US", got)
		}
		if got := req.Header.Get("Referer"); got != "https://chatgpt.com/" {
			t.Fatalf("Referer = %q, want https://chatgpt.com/", got)
		}
		if got := req.Header.Get("X-OpenAI-Target-Path"); got != "/backend-api/subscriptions" {
			t.Fatalf("X-OpenAI-Target-Path = %q, want /backend-api/subscriptions", got)
		}
		if got := req.Header.Get("X-OpenAI-Target-Route"); got != "/backend-api/subscriptions" {
			t.Fatalf("X-OpenAI-Target-Route = %q, want /backend-api/subscriptions", got)
		}
	})

	t.Run("preserves caller chatgpt headers", func(t *testing.T) {
		t.Parallel()
		req, errRequest := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/subscriptions", nil)
		if errRequest != nil {
			t.Fatalf("new request: %v", errRequest)
		}
		req.Header.Set("User-Agent", "caller-ua")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-OpenAI-Target-Path", "/custom")
		applyChatGPTAPICallHeaderDefaults(req)
		if got := req.Header.Get("User-Agent"); got != "caller-ua" {
			t.Fatalf("User-Agent = %q, want caller-ua", got)
		}
		if got := req.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q, want application/json", got)
		}
		if got := req.Header.Get("X-OpenAI-Target-Path"); got != "/custom" {
			t.Fatalf("X-OpenAI-Target-Path = %q, want /custom", got)
		}
	})

	t.Run("skips non chatgpt hosts", func(t *testing.T) {
		t.Parallel()
		req, errRequest := http.NewRequest(http.MethodGet, "https://api.example.com/v1/ping", nil)
		if errRequest != nil {
			t.Fatalf("new request: %v", errRequest)
		}
		applyChatGPTAPICallHeaderDefaults(req)
		if got := req.Header.Get("User-Agent"); got != "" {
			t.Fatalf("User-Agent = %q, want empty", got)
		}
		if got := req.Header.Get("X-OpenAI-Target-Path"); got != "" {
			t.Fatalf("X-OpenAI-Target-Path = %q, want empty", got)
		}
	})
}

func TestAPICallChatGPTUsesChromeTransportAndPassesHeaders(t *testing.T) {
	var gotProxy string
	var gotReq *http.Request
	stubAPICallFingerprintTransport(t, &gotProxy, apiCallRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotReq = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"active_until":"2099-01-01T00:00:00Z"}`)),
			Request:    req,
		}, nil
	}))

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"method":"GET","url":"https://chatgpt.com/backend-api/subscriptions?account_id=acc-1","proxy_url":"http://request-proxy.example.com:8080","header":{"Authorization":"Bearer caller-token","User-Agent":"caller-ua"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response apiCallResponse
	if errDecode := json.NewDecoder(recorder.Body).Decode(&response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upstream status code = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if response.Body != `{"active_until":"2099-01-01T00:00:00Z"}` {
		t.Fatalf("upstream body = %q, want subscription JSON", response.Body)
	}
	if gotProxy != "http://request-proxy.example.com:8080" {
		t.Fatalf("fingerprint proxy = %q, want request proxy", gotProxy)
	}
	if gotReq == nil {
		t.Fatal("expected chrome transport to receive the upstream request")
	}
	if got := gotReq.Header.Get("Authorization"); got != "Bearer caller-token" {
		t.Fatalf("Authorization = %q, want Bearer caller-token", got)
	}
	if got := gotReq.Header.Get("User-Agent"); got != "caller-ua" {
		t.Fatalf("User-Agent = %q, want caller-ua", got)
	}
	if got := gotReq.Header.Get("X-OpenAI-Target-Path"); got != "/backend-api/subscriptions" {
		t.Fatalf("X-OpenAI-Target-Path = %q, want default path", got)
	}
}

func TestAPICallNonChatGPTDoesNotUseChromeTransport(t *testing.T) {
	chromeCalled := false
	stubAPICallFingerprintTransport(t, nil, apiCallRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		chromeCalled = true
		return &http.Response{
			StatusCode: http.StatusTeapot,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("chrome")),
			Request:    req,
		}, nil
	}))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Caller"); got != "keep-me" {
			t.Errorf("X-Caller = %q, want keep-me", got)
		}
		if got := r.Header.Get("X-OpenAI-Target-Path"); got != "" {
			t.Errorf("X-OpenAI-Target-Path = %q, want empty on non-chatgpt host", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("standard"))
	}))
	defer upstream.Close()

	h := &Handler{}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"method":"GET","url":"` + upstream.URL + `/v1/ping","header":{"X-Caller":"keep-me"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response apiCallResponse
	if errDecode := json.NewDecoder(recorder.Body).Decode(&response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("upstream status code = %d, want %d", response.StatusCode, http.StatusAccepted)
	}
	if response.Body != "standard" {
		t.Fatalf("upstream body = %q, want %q", response.Body, "standard")
	}
	if chromeCalled {
		t.Fatal("chrome transport should not handle non-chatgpt hosts")
	}
}

func TestAPICallClientTransportPassesResolvedProxyToFingerprint(t *testing.T) {
	var gotProxy string
	original := newAPICallFingerprintTransport
	t.Cleanup(func() {
		newAPICallFingerprintTransport = original
	})
	newAPICallFingerprintTransport = func(proxyURL string, fallback http.RoundTripper) http.RoundTripper {
		gotProxy = proxyURL
		_ = fallback
		return apiCallRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
		})
	}

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}
	_ = h.apiCallClientTransport(auth, " http://request-proxy.example.com:8080 ")
	if gotProxy != "http://request-proxy.example.com:8080" {
		t.Fatalf("fingerprint proxy = %q, want request proxy", gotProxy)
	}

	gotProxy = ""
	_ = h.apiCallClientTransport(auth, "")
	if gotProxy != "http://credential-proxy.example.com:8080" {
		t.Fatalf("fingerprint proxy = %q, want credential proxy", gotProxy)
	}

	gotProxy = ""
	_ = h.apiCallClientTransport(&coreauth.Auth{ProxyURL: "bad-value"}, "")
	if gotProxy != "http://global-proxy.example.com:8080" {
		t.Fatalf("fingerprint proxy = %q, want global proxy", gotProxy)
	}

	gotProxy = ""
	_ = h.apiCallClientTransport(nil, "")
	if gotProxy != "http://global-proxy.example.com:8080" {
		t.Fatalf("fingerprint proxy = %q, want global proxy when auth is nil", gotProxy)
	}
}
