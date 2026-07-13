package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/notifications"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
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

func TestAPICallUnauthorizedPublishesOptInManagementWebhook(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	notifications.ConfigureWebhooks(nil)
	t.Cleanup(func() {
		notifications.ConfigureWebhooks(nil)
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Encountered invalidated oauth token for user, failing request"))
	}))
	defer upstream.Close()

	webhookRequests := make(chan []byte, 1)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		webhookRequests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer webhook.Close()
	notifications.ConfigureWebhooks([]config.NotificationWebhookConfig{{
		Name:        "codex-alerts",
		URL:         webhook.URL,
		Adapter:     "generic",
		Events:      []string{notifications.EventAuthManagementRequestFailed},
		Providers:   []string{"codex"},
		StatusCodes: []int{http.StatusUnauthorized},
	}})

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "test-access-token",
		},
	}
	authIndex := auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	reqBody := `{"auth_index":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `","header":{"Authorization":"Bearer $TOKEN$"}}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var apiResp apiCallResponse
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &apiResp); errUnmarshal != nil {
		t.Fatalf("unmarshal api-call response: %v", errUnmarshal)
	}
	if apiResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api-call status_code = %d, want %d", apiResp.StatusCode, http.StatusUnauthorized)
	}

	var event notifications.Event
	select {
	case payload := <-webhookRequests:
		if errUnmarshal := json.Unmarshal(payload, &event); errUnmarshal != nil {
			t.Fatalf("unmarshal webhook event: %v body=%s", errUnmarshal, string(payload))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for webhook request")
	}
	if event.Type != notifications.EventAuthManagementRequestFailed || event.Provider != "codex" || event.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unexpected webhook event: %+v", event)
	}
	if !strings.Contains(event.Message, "invalidated oauth token") {
		t.Fatalf("event message = %q, want invalidated oauth token", event.Message)
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to remain registered")
	}
	if updated.Status == coreauth.StatusError || updated.LastError != nil {
		t.Fatalf("api-call should not mark auth state as failed, got status=%q last_error=%+v", updated.Status, updated.LastError)
	}
}

func TestAPICallUnauthorizedDoesNotMatchRequestFailureWebhook(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	notifications.ConfigureWebhooks(nil)
	t.Cleanup(func() {
		notifications.ConfigureWebhooks(nil)
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalidated oauth token"))
	}))
	defer upstream.Close()

	webhookRequests := make(chan []byte, 1)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		webhookRequests <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer webhook.Close()
	notifications.ConfigureWebhooks([]config.NotificationWebhookConfig{{
		Name:        "request-failures",
		URL:         webhook.URL,
		Adapter:     "generic",
		Events:      []string{notifications.EventAuthRequestFailed, notifications.EventAuthRequestUnauthorized},
		Providers:   []string{"codex"},
		StatusCodes: []int{http.StatusUnauthorized},
	}})

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Metadata: map[string]any{
			"access_token": "test-access-token",
		},
	}
	authIndex := auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	reqBody := `{"auth_index":"` + authIndex + `","method":"GET","url":"` + upstream.URL + `","header":{"Authorization":"Bearer $TOKEN$"}}`
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	select {
	case payload := <-webhookRequests:
		t.Fatalf("unexpected webhook request: %s", string(payload))
	case <-time.After(100 * time.Millisecond):
	}
}
