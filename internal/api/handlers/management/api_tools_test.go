package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	executor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
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
			VertexCompatAPIKey: []config.VertexCompatKey{{
				APIKey:   "vertex-key",
				ProxyURL: "http://vertex-proxy.example.com:8080",
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
			name: "vertex",
			auth: &coreauth.Auth{
				Provider:   "vertex",
				Attributes: map[string]string{"api_key": "vertex-key"},
			},
			wantProxy: "http://vertex-proxy.example.com:8080",
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

func TestAPICallTransportCommandAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	commandAuth := &config.CommandAuthConfig{Command: "fetch-token"}
	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{{
			Auth:     commandAuth,
			ProxyURL: "http://gemini-command-proxy.example.com:8080",
		}},
	}
	cfg.SanitizeGeminiKeys()
	h := &Handler{cfg: cfg}
	auth := &coreauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			coreauth.AttrAuthKind:       coreauth.AttrAuthKindAPIKey,
			coreauth.AttrAuthSource:     coreauth.AttrAuthSourceCommand,
			coreauth.AttrAuthCommand:    "fetch-token",
			coreauth.AttrAuthCommandKey: config.CommandAuthIdentity(cfg.GeminiKey[0].Auth),
		},
	}

	transport := h.apiCallTransport(auth)
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
	if proxyURL == nil || proxyURL.String() != "http://gemini-command-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://gemini-command-proxy.example.com:8080", proxyURL)
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

func TestResolveTokenForAuthRunsCommandAuth(t *testing.T) {
	t.Parallel()

	script := writeAPICallTokenScript(t, "Bearer api-call-token")
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewOpenAICompatExecutor("openai-compatibility", nil))

	auth := &coreauth.Auth{
		ID:       "command-auth",
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":                            "https://example.com/v1",
			coreauth.AttrAuthKind:                 coreauth.AttrAuthKindAPIKey,
			coreauth.AttrAuthSource:               coreauth.AttrAuthSourceCommand,
			coreauth.AttrAuthCommand:              script,
			coreauth.AttrAuthArgsJSON:             "[]",
			coreauth.AttrAuthTimeoutMS:            "5000",
			coreauth.AttrAuthRefreshIntervalMS:    strconv.Itoa(int(time.Hour / time.Millisecond)),
			coreauth.AttrAuthInvalidatesOnNext401: "true",
		},
		Status: coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register command auth: %v", errRegister)
	}

	h := &Handler{authManager: manager}
	token, errToken := h.resolveTokenForAuth(context.Background(), auth)
	if errToken != nil {
		t.Fatalf("resolveTokenForAuth error: %v", errToken)
	}
	if token != "api-call-token" {
		t.Fatalf("token = %q, want api-call-token", token)
	}

	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected auth in manager")
	}
	if got, _ := current.Metadata["access_token"].(string); got != "api-call-token" {
		t.Fatalf("cached access_token = %q, want api-call-token", got)
	}
}

func TestResolveTokenForAuthRunsCodexCommandAuth(t *testing.T) {
	t.Parallel()

	script := writeAPICallTokenScript(t, "Bearer codex-api-call-token")
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewCodexAutoExecutor(nil))

	auth := &coreauth.Auth{
		ID:       "codex-command-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":                            "https://example.com/v2",
			coreauth.AttrAuthKind:                 coreauth.AttrAuthKindAPIKey,
			coreauth.AttrAuthSource:               coreauth.AttrAuthSourceCommand,
			coreauth.AttrAuthCommand:              script,
			coreauth.AttrAuthArgsJSON:             "[]",
			coreauth.AttrAuthTimeoutMS:            "5000",
			coreauth.AttrAuthRefreshIntervalMS:    strconv.Itoa(int(time.Hour / time.Millisecond)),
			coreauth.AttrAuthInvalidatesOnNext401: "true",
		},
		Status: coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register command auth: %v", errRegister)
	}

	h := &Handler{authManager: manager}
	token, errToken := h.resolveTokenForAuth(context.Background(), auth)
	if errToken != nil {
		t.Fatalf("resolveTokenForAuth error: %v", errToken)
	}
	if token != "codex-api-call-token" {
		t.Fatalf("token = %q, want codex-api-call-token", token)
	}
}

func TestCodexCommandAuthIndexResolvesToken(t *testing.T) {
	t.Parallel()

	script := writeAPICallTokenScript(t, "Bearer indexed-codex-token")
	cfg := &config.Config{
		CodexKey: []config.CodexKey{
			{
				Auth: &config.CommandAuthConfig{
					Command:           script,
					Args:              []string{},
					TimeoutMS:         5000,
					RefreshIntervalMS: int(time.Hour / time.Millisecond),
				},
				BaseURL:    "https://aiden-aiproxy.bytedance.net/v2",
				Websockets: true,
			},
		},
	}
	auths, errSynth := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynth != nil {
		t.Fatalf("synthesize auths: %v", errSynth)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewCodexAutoExecutor(cfg))
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auths[0]); errRegister != nil {
		t.Fatalf("register command auth: %v", errRegister)
	}

	h := &Handler{cfg: cfg, authManager: manager}
	keys := h.codexKeysWithAuthIndex()
	if len(keys) != 1 {
		t.Fatalf("codex key count = %d, want 1", len(keys))
	}
	if strings.TrimSpace(keys[0].AuthIndex) == "" {
		t.Fatalf("codex command auth-index is empty: %#v", keys[0])
	}

	auth := h.authByIndex(keys[0].AuthIndex)
	if auth == nil {
		t.Fatalf("authByIndex(%q) returned nil", keys[0].AuthIndex)
	}
	token, errToken := h.resolveTokenForAuth(context.Background(), auth)
	if errToken != nil {
		t.Fatalf("resolveTokenForAuth error: %v", errToken)
	}
	if token != "indexed-codex-token" {
		t.Fatalf("token = %q, want indexed-codex-token", token)
	}
}

func TestResolveTokenForAuthRefreshesExpiredCommandAuth(t *testing.T) {
	t.Parallel()

	script := writeAPICallTokenScript(t, "Bearer fresh-token")
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewOpenAICompatExecutor("openai-compatibility", nil))

	auth := &coreauth.Auth{
		ID:       "expired-command-auth",
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":                         "https://example.com/v1",
			coreauth.AttrAuthKind:              coreauth.AttrAuthKindAPIKey,
			coreauth.AttrAuthSource:            coreauth.AttrAuthSourceCommand,
			coreauth.AttrAuthCommand:           script,
			coreauth.AttrAuthArgsJSON:          "[]",
			coreauth.AttrAuthTimeoutMS:         "5000",
			coreauth.AttrAuthRefreshIntervalMS: strconv.Itoa(int(time.Hour / time.Millisecond)),
		},
		Metadata:         map[string]any{"access_token": "stale-token"},
		NextRefreshAfter: time.Now().Add(-time.Minute),
		Status:           coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register command auth: %v", errRegister)
	}

	h := &Handler{authManager: manager}
	token, errToken := h.resolveTokenForAuth(context.Background(), auth)
	if errToken != nil {
		t.Fatalf("resolveTokenForAuth error: %v", errToken)
	}
	if token != "fresh-token" {
		t.Fatalf("token = %q, want fresh-token", token)
	}
}

func TestAPICallInfersCommandAuthFromURL(t *testing.T) {
	t.Parallel()

	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer upstream.Close()

	script := writeAPICallTokenScript(t, "Bearer inferred-token")
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewCodexAutoExecutor(nil))
	auth := commandAPICallAuth("codex-command-auth", upstream.URL+"/v2", script)
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register command auth: %v", errRegister)
	}

	body := map[string]any{
		"method": "GET",
		"url":    upstream.URL + "/v2/models",
	}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = jsonRequest(t, body)

	(&Handler{authManager: manager}).APICall(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer inferred-token" {
		t.Fatalf("Authorization = %q, want Bearer inferred-token", gotAuth)
	}
}

func TestAPICallDoesNotInferCommandAuthForMismatchedURL(t *testing.T) {
	t.Parallel()

	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	script := writeAPICallTokenScript(t, "Bearer inferred-token")
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewCodexAutoExecutor(nil))
	auth := commandAPICallAuth("codex-command-auth", upstream.URL+"/v2", script)
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register command auth: %v", errRegister)
	}

	body := map[string]any{
		"method": "GET",
		"url":    upstream.URL + "/other/models",
	}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = jsonRequest(t, body)

	(&Handler{authManager: manager}).APICall(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuth)
	}
}

func TestCommandAuthForAPICallURLReturnsNilWhenAmbiguous(t *testing.T) {
	t.Parallel()

	script := writeAPICallTokenScript(t, "Bearer inferred-token")
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewCodexAutoExecutor(nil))
	for _, id := range []string{"codex-command-auth-1", "codex-command-auth-2"} {
		auth := commandAPICallAuth(id, "https://aiden-aiproxy.bytedance.net/v2", script)
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("register command auth: %v", errRegister)
		}
	}

	target, errParse := url.Parse("https://aiden-aiproxy.bytedance.net/v2/models")
	if errParse != nil {
		t.Fatalf("parse url: %v", errParse)
	}

	if auth := (&Handler{authManager: manager}).commandAuthForAPICallURL(target); auth != nil {
		t.Fatalf("commandAuthForAPICallURL returned %q, want nil", auth.ID)
	}
}

func commandAPICallAuth(id, baseURL, script string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":                         baseURL,
			coreauth.AttrAuthKind:              coreauth.AttrAuthKindAPIKey,
			coreauth.AttrAuthSource:            coreauth.AttrAuthSourceCommand,
			coreauth.AttrAuthCommand:           script,
			coreauth.AttrAuthArgsJSON:          "[]",
			coreauth.AttrAuthTimeoutMS:         "5000",
			coreauth.AttrAuthRefreshIntervalMS: strconv.Itoa(int(time.Hour / time.Millisecond)),
		},
		Status: coreauth.StatusActive,
	}
}

func jsonRequest(t *testing.T, body any) *http.Request {
	t.Helper()
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func writeAPICallTokenScript(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "token.sh")
	body := "#!/bin/sh\nprintf '%s\\n' " + shellSingleQuote(token) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write token script: %v", err)
	}
	return path
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
