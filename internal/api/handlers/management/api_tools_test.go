package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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

			transport := h.apiCallTransport(tc.auth)
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

func TestFindAuthByStableIndexWorksWithoutEnsureIndex(t *testing.T) {
	t.Parallel()

	auth := &coreauth.Auth{
		ID:       "gemini-no-index",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	if auth.Index != "" {
		t.Fatalf("expected empty initial index, got %q", auth.Index)
	}

	wantIndex := auth.StableIndex()
	if wantIndex == "" {
		t.Fatal("expected stable index")
	}

	got := findAuthByStableIndex([]*coreauth.Auth{auth}, wantIndex)
	if got == nil {
		t.Fatal("expected auth by stable index")
	}
	if got != auth {
		t.Fatal("helper should return original auth pointer")
	}
	if auth.Index != "" {
		t.Fatalf("helper should not assign index, got %q", auth.Index)
	}
}

func TestAPICallPublishesEventAfterSuccessResponse(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "ready")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	h := &Handler{apiCallEvents: newManagementAPICallEventBus()}
	events := make(chan ManagementAPICallEvent, 1)
	h.RegisterManagementAPICallListener(ManagementAPICallListenerFunc(func(_ context.Context, evt ManagementAPICallEvent) {
		events <- evt
	}))

	r := gin.New()
	r.POST("/api-call", h.APICall)

	targetURL := upstream.URL + "/v1/warmup"
	reqBody := `{"method":"GET","url":"` + targetURL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api-call", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var gotResp apiCallResponse
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &gotResp); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal returned error: %v", errUnmarshal)
	}
	if gotResp.StatusCode != http.StatusCreated {
		t.Fatalf("response status_code = %d, want %d", gotResp.StatusCode, http.StatusCreated)
	}
	if gotResp.Body != `{"ok":true}` {
		t.Fatalf("response body = %q, want %q", gotResp.Body, `{"ok":true}`)
	}
	if got := gotResp.Header["X-Upstream"]; len(got) != 1 || got[0] != "ready" {
		t.Fatalf("response header X-Upstream = %v, want [ready]", got)
	}

	select {
	case evt := <-events:
		if evt.AuthIndex != "" {
			t.Fatalf("event auth_index = %q, want empty", evt.AuthIndex)
		}
		if evt.Method != http.MethodGet {
			t.Fatalf("event method = %q, want %q", evt.Method, http.MethodGet)
		}
		if evt.URL != targetURL {
			t.Fatalf("event url = %q, want %q", evt.URL, targetURL)
		}
		if evt.StatusCode != http.StatusCreated {
			t.Fatalf("event status_code = %d, want %d", evt.StatusCode, http.StatusCreated)
		}
		if evt.RespHeader.Get("X-Upstream") != "ready" {
			t.Fatalf("event header X-Upstream = %q, want %q", evt.RespHeader.Get("X-Upstream"), "ready")
		}
		if string(evt.RespBody) != `{"ok":true}` {
			t.Fatalf("event body = %q, want %q", string(evt.RespBody), `{"ok":true}`)
		}
	case <-time.After(time.Second):
		t.Fatal("expected APICall event after success response")
	}
}

func TestAPICallPublishesEventWithDetachedContext(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	h := &Handler{apiCallEvents: newManagementAPICallEventBus()}
	ctxErrs := make(chan error, 1)
	h.RegisterManagementAPICallListener(ManagementAPICallListenerFunc(func(ctx context.Context, evt ManagementAPICallEvent) {
		time.Sleep(50 * time.Millisecond)
		ctxErrs <- ctx.Err()
	}))

	r := gin.New()
	r.POST("/api-call", h.APICall)

	reqBody := `{"method":"GET","url":"` + upstream.URL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api-call", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	select {
	case err := <-ctxErrs:
		if err != nil {
			t.Fatalf("listener context err = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expected listener context result")
	}
}

func TestAPICallDoesNotPublishEventOnRequestFailure(t *testing.T) {
	t.Parallel()

	h := &Handler{apiCallEvents: newManagementAPICallEventBus()}
	events := make(chan ManagementAPICallEvent, 1)
	h.RegisterManagementAPICallListener(ManagementAPICallListenerFunc(func(_ context.Context, evt ManagementAPICallEvent) {
		events <- evt
	}))

	r := gin.New()
	r.POST("/api-call", h.APICall)

	reqBody := `{"method":"GET","url":"http://127.0.0.1:1/unreachable"}`
	req := httptest.NewRequest(http.MethodPost, "/api-call", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}

	select {
	case evt := <-events:
		t.Fatalf("unexpected event published: %+v", evt)
	case <-time.After(200 * time.Millisecond):
	}
}
