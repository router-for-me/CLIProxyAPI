package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

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
			MetaKey: []config.MetaKey{{
				APIKey:   "meta-key",
				ProxyURL: "http://meta-proxy.example.com:8080",
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
			name: "meta",
			auth: &coreauth.Auth{
				Provider:   "meta",
				Attributes: map[string]string{"api_key": "meta-key"},
			},
			wantProxy: "http://meta-proxy.example.com:8080",
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

func TestAPICallResolvesMetaToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"api_key": "LLM|api-call-minted-key",
			})
			return
		}
		if r.URL.Path == "/test" {
			authHeader := r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"auth_header": authHeader,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	t.Setenv("META_MINT_URL", server.URL+"/key")

	manager := coreauth.NewManager(nil, nil, nil)
	metaAuth := &coreauth.Auth{
		ID:       "meta:oauth:user1",
		Provider: "meta",
		Metadata: map[string]any{
			"dca_token": "dca:tool-test-token",
		},
	}
	if _, err := manager.Register(context.Background(), metaAuth); err != nil {
		t.Fatalf("register meta auth: %v", err)
	}
	authIndex := metaAuth.EnsureIndex()

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
		tokenStore:  &memoryAuthStore{},
	}
	router := gin.New()
	router.POST("/api/call", h.APICall)

	body := `{"auth_index":"` + authIndex + `","method":"GET","url":"` + server.URL + `/test","header":{"Authorization":"Bearer $TOKEN$"}}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/call", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response apiCallResponse
	if errDecode := json.NewDecoder(recorder.Body).Decode(&response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}

	var upstreamBody map[string]string
	if err := json.Unmarshal([]byte(response.Body), &upstreamBody); err != nil {
		t.Fatalf("decode upstream response: %v", err)
	}
	if upstreamBody["auth_header"] != "Bearer LLM|api-call-minted-key" {
		t.Errorf("expected header 'Bearer LLM|api-call-minted-key', got %q", upstreamBody["auth_header"])
	}
}

func TestResolveMetaTokenUpdatesManagerAndStore(t *testing.T) {
	mints := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mints++
		_ = json.NewEncoder(w).Encode(map[string]string{"api_key": "LLM|minted", "base_url": " https://regional.meta.example/v1 "})
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL)
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: "meta-management", Provider: "meta", Metadata: map[string]any{"dca_token": "dca:test", "base_url": "https://previous.meta.example/v1"}, Attributes: map[string]string{"base_url": "https://previous.meta.example/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{}, authManager: manager, tokenStore: store}
	for i := 0; i < 2; i++ {
		token, err := h.resolveTokenForAuth(context.Background(), h.authByIndex(auth.Index), "")
		if err != nil {
			t.Fatal(err)
		}
		if token != "LLM|minted" {
			t.Fatalf("token = %q", token)
		}
	}
	if mints != 1 {
		t.Errorf("minted %d times for sequential calls, want 1", mints)
	}
	live := h.authByIndex(auth.Index)
	if live.Metadata["api_key"] != "LLM|minted" {
		t.Error("live manager did not retain minted key")
	}
	if live.Metadata["base_url"] != "https://regional.meta.example/v1" || live.Attributes["base_url"] != "https://regional.meta.example/v1" {
		t.Error("live manager did not retain minted base URL")
	}
	saved, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Metadata["api_key"] != "LLM|minted" || saved[0].Metadata["base_url"] != "https://regional.meta.example/v1" {
		t.Fatalf("configured store did not retain minted credentials: %#v", saved)
	}
}

type failingMetaTokenStore struct {
	memoryAuthStore
	err error
}

func (s *failingMetaTokenStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", s.err
}

func TestResolveMetaTokenPropagatesStoreFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"api_key":"LLM|minted"}`))
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL)
	saveErr := errors.New("test store unavailable")
	store := &failingMetaTokenStore{err: saveErr}
	manager := coreauth.NewManager(store, nil, nil)
	auth, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: "meta-save-failure", Provider: "meta", Metadata: map[string]any{"dca_token": "dca:test"}})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: &config.Config{}, authManager: manager, tokenStore: store}
	token, err := h.resolveTokenForAuth(context.Background(), h.authByIndex(auth.Index), "")
	if !errors.Is(err, saveErr) {
		t.Fatalf("error = %v, want store failure", err)
	}
	if token != "" {
		t.Error("returned a token despite failed persistence")
	}
	if h.authByIndex(auth.Index).Metadata["api_key"] != nil {
		t.Error("failed save installed a token in the manager")
	}
}

func TestResolveMetaToken_ConcurrentSingleflight(t *testing.T) {
	var mints int64
	serverStarted := make(chan struct{})
	releaseServer := make(chan struct{})
	var startOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&mints, 1)
		startOnce.Do(func() { close(serverStarted) })
		<-releaseServer
		_ = json.NewEncoder(w).Encode(map[string]string{
			"api_key":  "LLM|minted-concurrent",
			"base_url": "https://regional.meta.example/v1",
		})
	}))
	defer server.Close()

	t.Setenv("META_MINT_URL", server.URL)
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	auth, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
		ID:       "meta-mgmt-concurrent",
		Provider: "meta",
		Metadata: map[string]any{"dca_token": "dca:concurrent-test"},
	})
	if err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: &config.Config{}, authManager: manager, tokenStore: store}

	const callers = 5
	var entryWg sync.WaitGroup
	entryWg.Add(callers)
	ready := make(chan struct{})
	var inFlight sync.WaitGroup
	inFlight.Add(callers)
	var doneWg sync.WaitGroup
	doneWg.Add(callers)

	for i := 0; i < callers; i++ {
		go func() {
			defer doneWg.Done()
			entryWg.Done()
			<-ready
			inFlight.Done()
			token, errToken := h.resolveTokenForAuth(context.Background(), h.authByIndex(auth.Index), "")
			if errToken != nil {
				t.Errorf("resolveTokenForAuth error: %v", errToken)
			}
			if token != "LLM|minted-concurrent" {
				t.Errorf("expected LLM|minted-concurrent, got %q", token)
			}
		}()
	}

	entryWg.Wait()
	close(ready)

	<-serverStarted
	inFlight.Wait()
	for i := 0; i < 50; i++ {
		runtime.Gosched()
	}
	close(releaseServer)
	doneWg.Wait()

	if totalMints := atomic.LoadInt64(&mints); totalMints != 1 {
		t.Errorf("expected 1 mint request, got %d", totalMints)
	}
	live := h.authByIndex(auth.Index)
	if live.Metadata["api_key"] != "LLM|minted-concurrent" {
		t.Error("live manager did not retain minted key")
	}
	if live.Attributes["api_key"] != "LLM|minted-concurrent" {
		t.Error("live manager did not retain minted key in attributes")
	}
}
