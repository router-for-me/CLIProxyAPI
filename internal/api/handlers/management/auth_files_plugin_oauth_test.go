package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPluginLoginPollAuthsExpandsMultipleAuths(t *testing.T) {
	host := pluginhost.New()
	resp := pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auths: []pluginapi.AuthData{
			{
				Provider:    "gemini-cli",
				ID:          "geminicli.json",
				FileName:    "geminicli.json",
				StorageJSON: []byte(`{"type":"gemini-cli"}`),
			},
			{
				Provider:    "gemini-cli",
				ID:          "geminicli-project-a.json",
				FileName:    "geminicli-project-a.json",
				StorageJSON: []byte(`{"type":"gemini-cli","project_id":"project-a"}`),
				Metadata:    map[string]any{"project_id": "project-a"},
			},
		},
	}

	records := pluginLoginPollAuths(host, resp)
	if len(records) != 2 {
		t.Fatalf("pluginLoginPollAuths() len = %d, want two records", len(records))
	}
	if records[0].ID != "geminicli.json" || records[1].ID != "geminicli-project-a.json" {
		t.Fatalf("records = %#v, want both plugin auths", records)
	}
	if gotProject := records[1].Metadata["project_id"]; gotProject != "project-a" {
		t.Fatalf("project_id = %#v, want project-a", gotProject)
	}
}

func TestSavePluginLoginRecordsRollsBackSavedAuthsOnFailure(t *testing.T) {
	store := &pluginLoginRollbackStore{failAt: 2}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.tokenStore = store

	records := []*coreauth.Auth{
		{
			ID:       "geminicli.json",
			FileName: "geminicli.json",
			Provider: "gemini-cli",
			Metadata: map[string]any{"type": "gemini-cli"},
		},
		{
			ID:       "geminicli-project-a.json",
			FileName: "geminicli-project-a.json",
			Provider: "gemini-cli",
			Metadata: map[string]any{"type": "gemini-cli", "project_id": "project-a"},
		},
	}

	errSave := h.savePluginLoginRecords(context.Background(), records)
	if errSave == nil {
		t.Fatal("savePluginLoginRecords() error = nil, want rollback-triggering error")
	}
	if len(store.saved) != 2 {
		t.Fatalf("saved len = %d, want two attempted saves", len(store.saved))
	}
	if !store.deleted["geminicli.json"] || !store.deleted["geminicli-project-a.json"] {
		t.Fatalf("deleted = %#v, want both saved auths rolled back", store.deleted)
	}
}

func TestPatchPluginVirtualAuthStatusReturnsConflictForVirtualChild(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := pluginVirtualAuthForTest(t.TempDir(), "source.json", "auth-1")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register virtual auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"auth-1","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestPatchPluginVirtualSourceStatusDisablesAllExpandedAuths(t *testing.T) {
	authDir := t.TempDir()
	fileName := "source.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"gemini-cli","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write source auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"source.json", "virtual-project-a"} {
		auth := pluginVirtualAuthForTest(authDir, fileName, id)
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register virtual auth %s: %v", id, errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"source.json","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("read source auth file: %v", errRead)
	}
	if !strings.Contains(string(raw), `"disabled":true`) {
		t.Fatalf("source auth file = %s, want disabled:true", string(raw))
	}
	for _, id := range []string{"source.json", "virtual-project-a"} {
		auth, ok := manager.GetByID(id)
		if !ok || auth == nil {
			t.Fatalf("expected auth %s to remain registered", id)
		}
		if !auth.Disabled || auth.Status != coreauth.StatusDisabled {
			t.Fatalf("auth %s disabled/status = %v/%s, want disabled", id, auth.Disabled, auth.Status)
		}
	}
}

func TestPatchPluginVirtualAuthFieldsReturnsConflict(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := pluginVirtualAuthForTest(t.TempDir(), "source.json", "auth-1")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register virtual auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(`{"name":"auth-1","note":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestDeletePluginVirtualSourceRemovesExpandedRuntimeAuths(t *testing.T) {
	authDir := t.TempDir()
	fileName := "source.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"gemini-cli"}`), 0o600); errWrite != nil {
		t.Fatalf("write source auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"auth-1", "auth-2"} {
		auth := pluginVirtualAuthForTest(authDir, fileName, id)
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register virtual auth %s: %v", id, errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil)
	ctx.Request = req

	h.DeleteAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, errStat := os.Stat(filePath); !os.IsNotExist(errStat) {
		t.Fatalf("expected source auth file to be removed, stat err: %v", errStat)
	}
	for _, id := range []string{"auth-1", "auth-2"} {
		if _, ok := manager.GetByID(id); ok {
			t.Fatalf("expected virtual auth %s to be removed", id)
		}
	}
}

func pluginVirtualAuthForTest(authDir, fileName, id string) *coreauth.Auth {
	filePath := filepath.Join(authDir, fileName)
	auth := &coreauth.Auth{
		ID:       id,
		FileName: fileName,
		Provider: "gemini-cli",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type": "gemini-cli",
		},
	}
	coreauth.MarkPluginVirtualAuth(auth, filePath, 0)
	return auth
}

type pluginLoginRollbackStore struct {
	failAt  int
	saved   []string
	deleted map[string]bool
}

func (s *pluginLoginRollbackStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *pluginLoginRollbackStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	path := strings.TrimSpace(auth.FileName)
	if path == "" {
		path = strings.TrimSpace(auth.ID)
	}
	s.saved = append(s.saved, path)
	if len(s.saved) == s.failAt {
		return path, errors.New("save failed after write")
	}
	return path, nil
}

func (s *pluginLoginRollbackStore) Delete(_ context.Context, id string) error {
	if s.deleted == nil {
		s.deleted = make(map[string]bool)
	}
	s.deleted[id] = true
	return nil
}

func (s *pluginLoginRollbackStore) SetBaseDir(string) {}

type testAuthProvider struct {
	identifier  string
	identifiers []string
	startLogin  func(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error)
	pollLogin   func(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error)
}

func (p *testAuthProvider) Identifier() string {
	return p.identifier
}

func (p *testAuthProvider) Identifiers() []string {
	return append([]string(nil), p.identifiers...)
}

func (p *testAuthProvider) ParseAuth(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return pluginapi.AuthParseResponse{}, nil
}

func (p *testAuthProvider) StartLogin(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	if p.startLogin != nil {
		return p.startLogin(ctx, req)
	}
	return pluginapi.AuthLoginStartResponse{}, nil
}

func (p *testAuthProvider) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	if p.pollLogin != nil {
		return p.pollLogin(ctx, req)
	}
	return pluginapi.AuthLoginPollResponse{}, nil
}

func (p *testAuthProvider) RefreshAuth(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return pluginapi.AuthRefreshResponse{}, nil
}

func TestServePluginAuthURLPassesQueryParamsAsMetadata(t *testing.T) {
	host := pluginhost.New()
	var capturedReq pluginapi.AuthLoginStartRequest
	callCount := 0
	provider := &testAuthProvider{
		identifier: "custom-sso",
		startLogin: func(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
			callCount++
			capturedReq = req
			return pluginapi.AuthLoginStartResponse{
				Provider:  req.Provider,
				URL:       "https://login.example.com",
				State:     fmt.Sprintf("state-1234567890-%d", callCount),
				ExpiresAt: time.Now().Add(time.Hour),
			}, nil
		},
	}
	host.RegisterPluginForTest("custom-sso-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: provider,
		},
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Port: 8080}, nil)
	h.SetPluginHost(host)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/custom-sso-auth-url?idc_region=eu-west-1&idc_start_url=https%3A%2F%2Fsso.example.com&scopes=read&scopes=write", nil)
	ctx.Request = req

	handled := h.ServePluginAuthURL(ctx)
	if !handled {
		t.Fatal("ServePluginAuthURL returned false, want true")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("ServePluginAuthURL status = %d, want %d body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if capturedReq.Metadata == nil {
		t.Fatal("capturedReq.Metadata is nil, want query parameters as metadata")
	}
	if got := capturedReq.Metadata["idc_region"]; got != "eu-west-1" {
		t.Fatalf("capturedReq.Metadata[idc_region] = %#v, want eu-west-1", got)
	}
	if got := capturedReq.Metadata["idc_start_url"]; got != "https://sso.example.com" {
		t.Fatalf("capturedReq.Metadata[idc_start_url] = %#v, want https://sso.example.com", got)
	}
	if gotScopes, ok := capturedReq.Metadata["scopes"].([]string); !ok || len(gotScopes) != 2 || gotScopes[0] != "read" || gotScopes[1] != "write" {
		t.Fatalf("capturedReq.Metadata[scopes] = %#v, want [read write]", capturedReq.Metadata["scopes"])
	}

	// Verify no query parameters passes nil metadata
	capturedReq = pluginapi.AuthLoginStartRequest{}
	recNoQuery := httptest.NewRecorder()
	ctxNoQuery, _ := gin.CreateTestContext(recNoQuery)
	reqNoQuery := httptest.NewRequest(http.MethodGet, "/v0/management/custom-sso-auth-url", nil)
	ctxNoQuery.Request = reqNoQuery

	handledNoQuery := h.ServePluginAuthURL(ctxNoQuery)
	if !handledNoQuery {
		t.Fatal("ServePluginAuthURL without query returned false, want true")
	}
	if recNoQuery.Code != http.StatusOK {
		t.Fatalf("ServePluginAuthURL status = %d, want %d body = %s", recNoQuery.Code, http.StatusOK, recNoQuery.Body.String())
	}
	if capturedReq.Metadata != nil {
		t.Fatalf("capturedReq.Metadata = %#v, want nil for request without query", capturedReq.Metadata)
	}
}

func TestV8PluginOAuthUsesIndependentCallback(t *testing.T) {
	host := pluginhost.New()
	var captured pluginapi.AuthLoginStartRequest
	callCount := 0
	host.RegisterPluginForTest("v8-login-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{AuthProvider: &testAuthProvider{
			identifier: "custom-sso",
			startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
				captured = req
				callCount++
				state := fmt.Sprintf("state-v8-1234567890-%d", callCount)
				t.Cleanup(func() { CancelOAuthSession(state) })
				return pluginapi.AuthLoginStartResponse{Provider: req.Provider, URL: "https://login.example.com", State: state, ExpiresAt: time.Now().Add(time.Hour)}, nil
			},
		}},
	})
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Port: 8317}, nil)
	h.SetPluginHost(host)
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	for _, tc := range []struct {
		query    string
		metadata map[string]any
	}{
		{"provider=custom-sso&region=eu&scopes=read&scopes=write", map[string]any{"region": "eu", "scopes": []string{"read", "write"}}},
		{"provider=%20CUSTOM-SSO%20", nil},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?"+tc.query, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if captured.BaseURL != "http://127.0.0.1:8317/v8/management/oauth/callback" || captured.Provider != "custom-sso" || !reflect.DeepEqual(captured.Metadata, tc.metadata) {
			t.Fatalf("incorrect v8 login request: %#v", captured)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider=unknown", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status=%d", response.Code)
	}
	if callCount != 2 {
		t.Fatalf("plugin login calls=%d, want 2", callCount)
	}
}

func TestV8PluginMultiPortalOAuthListLoginPollAndRelogin(t *testing.T) {
	authDir := t.TempDir()
	pluginsDir := writeManagementPluginFile(t, "omc-zcode-oauth")
	enabled := true
	host := pluginhost.New()
	callCount := 0
	host.RegisterPluginForTest("omc-zcode-oauth", pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             "OMC ZCode OAuth",
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
		},
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &testAuthProvider{
				identifier:  "zcode-zai",
				identifiers: []string{"zcode-zai", "zcode-bigmodel"},
				startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
					callCount++
					state := fmt.Sprintf("state-zcode-%s-%d", req.Provider, callCount)
					t.Cleanup(func() { CancelOAuthSession(state) })
					loginURL := "https://chat.z.ai/api/oauth/authorize"
					if req.Provider == "zcode-bigmodel" {
						loginURL = "https://bigmodel.cn/login?appId=zcode"
					}
					return pluginapi.AuthLoginStartResponse{
						Provider:  req.Provider,
						URL:       loginURL,
						State:     state,
						ExpiresAt: time.Now().Add(time.Hour),
					}, nil
				},
				pollLogin: func(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
					return pluginapi.AuthLoginPollResponse{
						Status: pluginapi.AuthLoginStatusSuccess,
						Auth: pluginapi.AuthData{
							Provider:    "zcode",
							ID:          "zcode-account.json",
							FileName:    "zcode-account.json",
							Label:       "ZCode Account (" + req.Provider + ")",
							StorageJSON: []byte(`{"type":"zcode","access_token":"tok"}`),
							Metadata:    map[string]any{"type": "zcode", "portal": req.Provider, "email": "user@example.com"},
						},
					}, nil
				},
			},
		},
	})

	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		AuthDir: authDir,
		Port:    8317,
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{
				"omc-zcode-oauth": {Enabled: &enabled},
			},
		},
	}, manager)
	h.SetPluginHost(host)

	router := gin.New()
	router.GET("/v8/management/plugins", h.ListPlugins)
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v8/management/get-auth-status", h.GetAuthStatus)

	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/v8/management/plugins", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/plugins status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listBody struct {
		Plugins []struct {
			ID             string   `json:"id"`
			SupportsOAuth  bool     `json:"supports_oauth"`
			OAuthProvider  string   `json:"oauth_provider"`
			OAuthProviders []string `json:"oauth_providers"`
		} `json:"plugins"`
	}
	if errUnmarshal := json.Unmarshal(listRec.Body.Bytes(), &listBody); errUnmarshal != nil {
		t.Fatalf("decode plugins list: %v", errUnmarshal)
	}
	if len(listBody.Plugins) != 1 || !listBody.Plugins[0].SupportsOAuth || listBody.Plugins[0].OAuthProvider != "zcode-zai" {
		t.Fatalf("plugins list = %#v, want SupportsOAuth=true OAuthProvider=zcode-zai", listBody.Plugins)
	}
	if !reflect.DeepEqual(listBody.Plugins[0].OAuthProviders, []string{"zcode-zai", "zcode-bigmodel"}) {
		t.Fatalf("oauth_providers = %#v, want [zcode-zai zcode-bigmodel]", listBody.Plugins[0].OAuthProviders)
	}

	// Start and complete login via second portal (zcode-bigmodel)
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/auth-url?provider=zcode-bigmodel", nil))
	if startRec.Code != http.StatusOK {
		t.Fatalf("start zcode-bigmodel status=%d body=%s", startRec.Code, startRec.Body.String())
	}
	var startPayload struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(startRec.Body.Bytes(), &startPayload); errUnmarshal != nil {
		t.Fatalf("decode start payload: %v", errUnmarshal)
	}
	if startPayload.URL != "https://bigmodel.cn/login?appId=zcode" {
		t.Fatalf("start URL = %q, want bigmodel portal URL", startPayload.URL)
	}

	pollRec := httptest.NewRecorder()
	router.ServeHTTP(pollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+startPayload.State, nil))
	if pollRec.Code != http.StatusOK || !strings.Contains(pollRec.Body.String(), `"status":"ok"`) {
		t.Fatalf("poll zcode-bigmodel status=%d body=%s", pollRec.Code, pollRec.Body.String())
	}

	// Simulate a cold-started host (no prior in-memory PollLogin state) with the saved "zcode" credential
	pollEmail := "user@example.com"
	coldHost := pluginhost.New()
	coldHost.RegisterPluginForTest("omc-zcode-oauth", pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             "OMC ZCode OAuth",
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
		},
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &testAuthProvider{
				identifier:  "zcode-zai",
				identifiers: []string{"zcode-zai", "zcode-bigmodel"},
				startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
					callCount++
					state := fmt.Sprintf("state-zcode-cold-%s-%d", req.Provider, callCount)
					t.Cleanup(func() { CancelOAuthSession(state) })
					return pluginapi.AuthLoginStartResponse{
						Provider:  req.Provider,
						URL:       "https://portal.example/" + req.Provider,
						State:     state,
						ExpiresAt: time.Now().Add(time.Hour),
					}, nil
				},
				pollLogin: func(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
					return pluginapi.AuthLoginPollResponse{
						Status: pluginapi.AuthLoginStatusSuccess,
						Auth: pluginapi.AuthData{
							Provider:    "zcode",
							ID:          "zcode-zai-different-file.json",
							FileName:    "zcode-zai-different-file.json",
							Label:       "ZCode Relogin (" + req.Provider + ")",
							StorageJSON: []byte(`{"type":"zcode","access_token":"tok-relogin"}`),
							Metadata:    map[string]any{"type": "zcode", "email": pollEmail, "access_token": "tok-relogin"},
							Attributes:  map[string]string{"path": filepath.Join(authDir, "zcode-zai-different-file.json")},
						},
					}, nil
				},
			},
		},
	})
	h.SetPluginHost(coldHost)

	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "zcode-account.json",
		FileName: "zcode-account.json",
		Provider: "zcode",
		Metadata: map[string]any{"email": "user@example.com", "oauth_provider": "zcode-bigmodel"},
	})
	// 1. Re-login specifying an explicit portal ("zcode-zai") against canonical credential provider ("zcode") on cold start
	reloginRec := httptest.NewRecorder()
	router.ServeHTTP(reloginRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"zcode-zai","name":"zcode-account.json"}`)))
	if reloginRec.Code != http.StatusOK {
		t.Fatalf("relogin via zcode-zai portal for zcode credential status=%d body=%s, want 200", reloginRec.Code, reloginRec.Body.String())
	}
	var reloginStart struct {
		State        string `json:"state"`
		TargetAuthID string `json:"target_auth_id"`
	}
	if errUnmarshal := json.Unmarshal(reloginRec.Body.Bytes(), &reloginStart); errUnmarshal != nil {
		t.Fatalf("decode relogin start payload: %v", errUnmarshal)
	}
	if reloginStart.TargetAuthID != "zcode-account.json" {
		t.Fatalf("relogin target_auth_id = %q, want zcode-account.json", reloginStart.TargetAuthID)
	}
	reloginPollRec := httptest.NewRecorder()
	router.ServeHTTP(reloginPollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+reloginStart.State, nil))
	if reloginPollRec.Code != http.StatusOK || !strings.Contains(reloginPollRec.Body.String(), `"status":"ok"`) {
		t.Fatalf("poll relogin status=%d body=%s, want ok", reloginPollRec.Code, reloginPollRec.Body.String())
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "zcode-zai-different-file.json")); !os.IsNotExist(errStat) {
		t.Fatalf("expected relogin to replace target file instead of writing zcode-zai-different-file.json, stat err=%v", errStat)
	}
	rawSaved, errRead := os.ReadFile(filepath.Join(authDir, "zcode-account.json"))
	if errRead != nil {
		t.Fatalf("read replaced target file: %v", errRead)
	}
	if !strings.Contains(string(rawSaved), `"oauth_provider":"zcode-zai"`) || !strings.Contains(string(rawSaved), `"access_token":"tok-relogin"`) {
		t.Fatalf("replaced target file content = %s, want oauth_provider=zcode-zai and access_token=tok-relogin", string(rawSaved))
	}
	if strings.Contains(string(rawSaved), `"portal":"zcode-bigmodel"`) {
		t.Fatalf("replaced target file retained stale portal metadata: %s", string(rawSaved))
	}

	// 2. Re-login omitting "provider" (only passing credential "name") on cold start, and verify mismatched email is rejected
	pollEmail = "wrong-user@example.com"
	reloginNoProviderRec := httptest.NewRecorder()
	router.ServeHTTP(reloginNoProviderRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"name":"zcode-account.json"}`)))
	if reloginNoProviderRec.Code != http.StatusOK {
		t.Fatalf("relogin omitting provider for zcode credential status=%d body=%s, want 200", reloginNoProviderRec.Code, reloginNoProviderRec.Body.String())
	}
	var reloginNoProvStart struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(reloginNoProviderRec.Body.Bytes(), &reloginNoProvStart); errUnmarshal != nil {
		t.Fatalf("decode reloginNoProvStart: %v", errUnmarshal)
	}
	mismatchPollRec := httptest.NewRecorder()
	router.ServeHTTP(mismatchPollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+reloginNoProvStart.State, nil))
	if mismatchPollRec.Code != http.StatusOK || !strings.Contains(mismatchPollRec.Body.String(), "Authenticated account does not match target credential") {
		t.Fatalf("mismatch poll status=%d body=%s, want mismatch error", mismatchPollRec.Code, mismatchPollRec.Body.String())
	}

	// 3. Target credential without email/account_id must reject blind overwrite when filename differs
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:       "zcode-anonymous.json",
		FileName: "zcode-anonymous.json",
		Provider: "zcode",
		Metadata: map[string]any{"oauth_provider": "zcode-bigmodel"},
	})
	pollEmail = "user@example.com"
	anonStartRec := httptest.NewRecorder()
	router.ServeHTTP(anonStartRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"name":"zcode-anonymous.json"}`)))
	if anonStartRec.Code != http.StatusOK {
		t.Fatalf("anon relogin start status=%d body=%s, want 200", anonStartRec.Code, anonStartRec.Body.String())
	}
	var anonStart struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(anonStartRec.Body.Bytes(), &anonStart); errUnmarshal != nil {
		t.Fatalf("decode anonStart: %v", errUnmarshal)
	}
	anonPollRec := httptest.NewRecorder()
	router.ServeHTTP(anonPollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+anonStart.State, nil))
	if anonPollRec.Code != http.StatusOK || !strings.Contains(anonPollRec.Body.String(), "Authenticated account does not match target credential") {
		t.Fatalf("anon poll status=%d body=%s, want mismatch error when target has no identity and filename differs", anonPollRec.Code, anonPollRec.Body.String())
	}
}

func TestV8PluginReloginPreservesSubdirTargetAndRollsBackOverwriteSafely(t *testing.T) {
	authDir := t.TempDir()
	rootFile := filepath.Join(authDir, "account.json")
	teamDir := filepath.Join(authDir, "team")
	if errMkdir := os.MkdirAll(teamDir, 0o700); errMkdir != nil {
		t.Fatalf("mkdir team: %v", errMkdir)
	}
	teamFile := filepath.Join(teamDir, "account.json")
	rootOriginal := `{"type":"zcode","email":"root@example.com","access_token":"root-token"}`
	teamOriginal := `{"type":"zcode","email":"user@example.com","oauth_provider":"zcode-bigmodel","access_token":"old-team-token"}`
	if errWrite := os.WriteFile(rootFile, []byte(rootOriginal), 0o600); errWrite != nil {
		t.Fatalf("write root file: %v", errWrite)
	}
	if errWrite := os.WriteFile(teamFile, []byte(teamOriginal), 0o600); errWrite != nil {
		t.Fatalf("write team file: %v", errWrite)
	}

	callCount := 0
	currentToken := "team-relogin-token"
	host := pluginhost.New()
	host.RegisterPluginForTest("omc-zcode-oauth", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &testAuthProvider{
				identifier:  "zcode-zai",
				identifiers: []string{"zcode-zai", "zcode-bigmodel"},
				startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
					callCount++
					state := fmt.Sprintf("state-subdir-%d", callCount)
					t.Cleanup(func() { CancelOAuthSession(state) })
					return pluginapi.AuthLoginStartResponse{
						Provider:  req.Provider,
						URL:       "https://portal.example/" + req.Provider,
						State:     state,
						ExpiresAt: time.Now().Add(time.Hour),
					}, nil
				},
				pollLogin: func(_ context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
					return pluginapi.AuthLoginPollResponse{
						Status: pluginapi.AuthLoginStatusSuccess,
						Auth: pluginapi.AuthData{
							Provider:    "zcode",
							ID:          "plugin-generated.json",
							FileName:    "plugin-generated.json",
							StorageJSON: []byte(fmt.Sprintf(`{"type":"zcode","access_token":%q}`, currentToken)),
							Metadata:    map[string]any{"type": "zcode", "email": "user@example.com", "access_token": currentToken},
							Attributes:  map[string]string{"path": filepath.Join(authDir, "plugin-generated.json")},
						},
					}, nil
				},
			},
		},
	})

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:         "team/account.json",
		FileName:   "account.json",
		Provider:   "zcode",
		Metadata:   map[string]any{"email": "user@example.com", "oauth_provider": "zcode-bigmodel", "access_token": "old-team-token"},
		Attributes: map[string]string{"path": teamFile},
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, manager)
	h.SetPluginHost(host)

	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v8/management/get-auth-status", h.GetAuthStatus)

	// 1. Re-login targeting subdirectory credential "team/account.json" must update team/account.json, leave root account.json untouched,
	// and pass persistedRecord.ID == "team/account.json" (not "account.json") to postAuthPersistHook.
	var persistedIDs []string
	h.postAuthPersistHook = func(_ context.Context, auth *coreauth.Auth) error {
		if auth != nil {
			persistedIDs = append(persistedIDs, auth.ID)
		}
		return nil
	}
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"zcode-zai","name":"team/account.json"}`)))
	if startRec.Code != http.StatusOK {
		t.Fatalf("start subdir relogin status=%d body=%s", startRec.Code, startRec.Body.String())
	}
	var startPayload struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(startRec.Body.Bytes(), &startPayload); errUnmarshal != nil {
		t.Fatalf("decode start payload: %v", errUnmarshal)
	}
	pollRec := httptest.NewRecorder()
	router.ServeHTTP(pollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+startPayload.State, nil))
	if pollRec.Code != http.StatusOK || !strings.Contains(pollRec.Body.String(), `"status":"ok"`) {
		t.Fatalf("poll subdir relogin status=%d body=%s, want ok", pollRec.Code, pollRec.Body.String())
	}
	if !reflect.DeepEqual(persistedIDs, []string{"team/account.json"}) {
		t.Fatalf("postAuthPersistHook received IDs = %#v, want [team/account.json]", persistedIDs)
	}
	rootBytes, errReadRoot := os.ReadFile(rootFile)
	if errReadRoot != nil || string(rootBytes) != rootOriginal {
		t.Fatalf("root account.json was modified: %q (err=%v), want %q", string(rootBytes), errReadRoot, rootOriginal)
	}
	teamBytes, errReadTeam := os.ReadFile(teamFile)
	if errReadTeam != nil || !strings.Contains(string(teamBytes), `"access_token":"team-relogin-token"`) {
		t.Fatalf("team/account.json content = %q (err=%v), want updated team-relogin-token", string(teamBytes), errReadTeam)
	}

	// 2. If postAuthPersistHook fails after overwriting team/account.json, rollback must restore the previous file and runtime auth instead of deleting them.
	h.postAuthPersistHook = func(_ context.Context, _ *coreauth.Auth) error {
		return fmt.Errorf("simulated post-persist failure")
	}
	currentToken = "broken-token"
	failStartRec := httptest.NewRecorder()
	router.ServeHTTP(failStartRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"zcode-zai","name":"team/account.json"}`)))
	if failStartRec.Code != http.StatusOK {
		t.Fatalf("failStartRec status=%d body=%s", failStartRec.Code, failStartRec.Body.String())
	}
	var failStart struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(failStartRec.Body.Bytes(), &failStart); errUnmarshal != nil {
		t.Fatalf("decode failStart: %v", errUnmarshal)
	}
	failPollRec := httptest.NewRecorder()
	router.ServeHTTP(failPollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+failStart.State, nil))
	if failPollRec.Code != http.StatusOK || !strings.Contains(failPollRec.Body.String(), "Failed to save authentication tokens") {
		t.Fatalf("failPollRec status=%d body=%s, want save failure", failPollRec.Code, failPollRec.Body.String())
	}
	restoredBytes, errReadRestored := os.ReadFile(teamFile)
	if errReadRestored != nil {
		t.Fatalf("expected team/account.json to be preserved after failed overwrite, got err=%v", errReadRestored)
	}
	if !strings.Contains(string(restoredBytes), `"access_token":"team-relogin-token"`) || strings.Contains(string(restoredBytes), `"broken-token"`) {
		t.Fatalf("restored team/account.json = %q, want previous team-relogin-token", string(restoredBytes))
	}
	if _, okAuth := manager.GetByID("team/account.json"); !okAuth {
		t.Fatal("expected team/account.json to remain in runtime auth manager after failed overwrite")
	}

	// 3. If another session concurrently commits a newer credential before rollback runs, rollback must not clobber the newer file or runtime state.
	concurrentWinnerJSON := `{"access_token":"concurrent-winner-token","disabled":false,"email":"user@example.com","oauth_provider":"zcode-zai","type":"zcode"}`
	h.postAuthPersistHook = func(ctx context.Context, _ *coreauth.Auth) error {
		if errWrite := os.WriteFile(teamFile, []byte(concurrentWinnerJSON), 0o600); errWrite != nil {
			return errWrite
		}
		_, _ = manager.Update(ctx, &coreauth.Auth{
			ID:         "team/account.json",
			FileName:   "account.json",
			Provider:   "zcode",
			Metadata:   map[string]any{"email": "user@example.com", "oauth_provider": "zcode-zai", "access_token": "concurrent-winner-token"},
			Attributes: map[string]string{"path": teamFile},
		})
		return fmt.Errorf("session A persist hook failed after concurrent session B succeeded")
	}
	currentToken = "session-a-stale-token"
	raceStartRec := httptest.NewRecorder()
	router.ServeHTTP(raceStartRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"zcode-zai","name":"team/account.json"}`)))
	if raceStartRec.Code != http.StatusOK {
		t.Fatalf("raceStartRec status=%d body=%s", raceStartRec.Code, raceStartRec.Body.String())
	}
	var raceStart struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(raceStartRec.Body.Bytes(), &raceStart); errUnmarshal != nil {
		t.Fatalf("decode raceStart: %v", errUnmarshal)
	}
	racePollRec := httptest.NewRecorder()
	router.ServeHTTP(racePollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+raceStart.State, nil))
	if racePollRec.Code != http.StatusOK || !strings.Contains(racePollRec.Body.String(), "Failed to save authentication tokens") {
		t.Fatalf("racePollRec status=%d body=%s, want save failure", racePollRec.Code, racePollRec.Body.String())
	}
	winnerBytes, errReadWinner := os.ReadFile(teamFile)
	if errReadWinner != nil || !strings.Contains(string(winnerBytes), `"concurrent-winner-token"`) {
		t.Fatalf("team/account.json after concurrent session = %q (err=%v), want concurrent-winner-token preserved", string(winnerBytes), errReadWinner)
	}
	if currentAuth, okAuth := manager.GetByID("team/account.json"); !okAuth || currentAuth == nil || currentAuth.Metadata["access_token"] != "concurrent-winner-token" {
		t.Fatalf("runtime auth after concurrent session = %#v, want access_token=concurrent-winner-token", currentAuth)
	}

	// 4. When a plugin puts access_token only in StorageJSON (not Metadata) and a batch save fails on the second record,
	// both disk and runtime auth for the first record must be restored to the previous state.
	h.postAuthPersistHook = nil
	batchRecords := []*coreauth.Auth{
		host.AuthDataToCoreAuth(pluginapi.AuthData{
			Provider:    "zcode",
			ID:          "team/account.json",
			FileName:    "account.json",
			StorageJSON: []byte(`{"type":"zcode","email":"user@example.com","access_token":"storage-only-token"}`),
			Metadata:    map[string]any{"type": "zcode", "email": "user@example.com", "oauth_provider": "zcode-zai"},
			Attributes:  map[string]string{"path": teamFile},
		}, teamFile, "account.json"),
		{
			ID:       "invalid-second-record",
			Provider: "zcode",
		},
	}
	if errBatch := h.savePluginLoginRecords(context.Background(), batchRecords); errBatch == nil {
		t.Fatal("expected batch savePluginLoginRecords to fail on second record")
	}
	if currentAuth, okAuth := manager.GetByID("team/account.json"); !okAuth || currentAuth == nil || currentAuth.Metadata["access_token"] != "concurrent-winner-token" {
		t.Fatalf("runtime auth after StorageJSON-only batch rollback = %#v, want access_token=concurrent-winner-token", currentAuth)
	}

	// 5. Targeted re-login succeeds when email is only present in StorageJSON (omitted from AuthData.Metadata).
	records := []*coreauth.Auth{
		host.AuthDataToCoreAuth(pluginapi.AuthData{
			Provider:    "zcode",
			ID:          "other-id.json",
			FileName:    "other-id.json",
			StorageJSON: []byte(`{"type":"zcode","email":"user@example.com","access_token":"storage-email-only"}`),
			Metadata:    map[string]any{"type": "zcode"},
		}, "", "other-id.json"),
	}
	if !applyPluginOAuthRecoveryTarget(oauthRecoveryTarget{
		AuthID:   "team/account.json",
		FileName: "account.json",
		Path:     teamFile,
		Provider: "zcode",
		Email:    "user@example.com",
	}, "zcode-zai", records) {
		t.Fatal("applyPluginOAuthRecoveryTarget rejected matching email stored only in StorageJSON")
	}

	// 6. When the existing target credential itself only stores email in StorageJSON (omitted from Metadata),
	// StartOAuthV8 must still populate target.Email from RawJSON so sibling-portal relogin with a different filename succeeds.
	storageOnlyTarget := host.AuthDataToCoreAuth(pluginapi.AuthData{
		Provider:    "zcode",
		ID:          "storage-only-target.json",
		FileName:    "storage-only-target.json",
		StorageJSON: []byte(`{"type":"zcode","email":"user@example.com","oauth_provider":"zcode-bigmodel","access_token":"old-tok"}`),
		Metadata:    map[string]any{"type": "zcode", "oauth_provider": "zcode-bigmodel"},
	}, filepath.Join(authDir, "storage-only-target.json"), "storage-only-target.json")
	_, _ = manager.Register(context.Background(), storageOnlyTarget)
	currentToken = "storage-target-relogin-tok"
	storageTargetStartRec := httptest.NewRecorder()
	router.ServeHTTP(storageTargetStartRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"zcode-zai","name":"storage-only-target.json"}`)))
	if storageTargetStartRec.Code != http.StatusOK {
		t.Fatalf("storageTargetStartRec status=%d body=%s", storageTargetStartRec.Code, storageTargetStartRec.Body.String())
	}
	var storageTargetStart struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(storageTargetStartRec.Body.Bytes(), &storageTargetStart); errUnmarshal != nil {
		t.Fatalf("decode storageTargetStart: %v", errUnmarshal)
	}
	storageTargetPollRec := httptest.NewRecorder()
	router.ServeHTTP(storageTargetPollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+storageTargetStart.State, nil))
	if storageTargetPollRec.Code != http.StatusOK || !strings.Contains(storageTargetPollRec.Body.String(), `"status":"ok"`) {
		t.Fatalf("storageTargetPollRec status=%d body=%s, want ok", storageTargetPollRec.Code, storageTargetPollRec.Body.String())
	}

	// 7. When a newly created credential in a batch has record.ID != record.FileName and the second record fails,
	// rollback must remove the runtime auth registered under the file-derived ID ("different-file-name.json").
	mismatchIDRecords := []*coreauth.Auth{
		host.AuthDataToCoreAuth(pluginapi.AuthData{
			Provider:    "zcode",
			ID:          "custom-logical-id",
			FileName:    "different-file-name.json",
			StorageJSON: []byte(`{"type":"zcode","email":"new@example.com","access_token":"temp-tok"}`),
			Metadata:    map[string]any{"type": "zcode", "email": "new@example.com"},
		}, "", "different-file-name.json"),
		{
			ID:       "invalid-second-record",
			Provider: "zcode",
		},
	}
	if errBatch := h.savePluginLoginRecords(context.Background(), mismatchIDRecords); errBatch == nil {
		t.Fatal("expected mismatchIDRecords batch save to fail on second record")
	}
	if _, ok := manager.GetByID("different-file-name.json"); ok {
		t.Fatal("expected different-file-name.json to be removed from runtime manager after batch rollback")
	}
	if _, ok := manager.GetByID("custom-logical-id"); ok {
		t.Fatal("expected custom-logical-id to be absent from runtime manager after batch rollback")
	}

	// 8. When postAuthPersistHook is nil and an existing target credential has logical ID != FileName,
	// relogin must update the existing logical ID in manager without creating a duplicate file-derived ID.
	logicalTargetFile := filepath.Join(authDir, "custom-target-file.json")
	if errWrite := os.WriteFile(logicalTargetFile, []byte(`{"type":"zcode","email":"user@example.com","oauth_provider":"zcode-bigmodel","access_token":"old-logical-tok"}`), 0o600); errWrite != nil {
		t.Fatalf("write logicalTargetFile: %v", errWrite)
	}
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:         "custom-target-id",
		FileName:   "custom-target-file.json",
		Provider:   "zcode",
		Metadata:   map[string]any{"email": "user@example.com", "oauth_provider": "zcode-bigmodel", "access_token": "old-logical-tok"},
		Attributes: map[string]string{"path": logicalTargetFile},
	})
	currentToken = "updated-logical-tok"
	logicalStartRec := httptest.NewRecorder()
	router.ServeHTTP(logicalStartRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"zcode-zai","name":"custom-target-id"}`)))
	if logicalStartRec.Code != http.StatusOK {
		t.Fatalf("logicalStartRec status=%d body=%s", logicalStartRec.Code, logicalStartRec.Body.String())
	}
	var logicalStart struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(logicalStartRec.Body.Bytes(), &logicalStart); errUnmarshal != nil {
		t.Fatalf("decode logicalStart: %v", errUnmarshal)
	}
	logicalPollRec := httptest.NewRecorder()
	router.ServeHTTP(logicalPollRec, httptest.NewRequest(http.MethodGet, "/v8/management/get-auth-status?state="+logicalStart.State, nil))
	if logicalPollRec.Code != http.StatusOK || !strings.Contains(logicalPollRec.Body.String(), `"status":"ok"`) {
		t.Fatalf("logicalPollRec status=%d body=%s, want ok", logicalPollRec.Code, logicalPollRec.Body.String())
	}
	if updatedLogical, ok := manager.GetByID("custom-target-id"); !ok || updatedLogical == nil || updatedLogical.Metadata["access_token"] != "updated-logical-tok" {
		t.Fatalf("expected custom-target-id updated in manager, got %#v", updatedLogical)
	}
	if _, okDup := manager.GetByID("custom-target-file.json"); okDup {
		t.Fatal("expected no duplicate file-derived ID custom-target-file.json in manager")
	}

	// 9. When an existing file-backed credential ("team/account.json") is overwritten by a batch record with a new logical ID
	// ("new-logical-id-for-team") and the second record fails, rollback must remove "new-logical-id-for-team" and preserve exact disk bytes.
	if errWrite := os.WriteFile(teamFile, []byte(`{"type":"zcode","email":"user@example.com","access_token":"newer-disk-token","custom_disk_only":"keep"}`), 0o600); errWrite != nil {
		t.Fatalf("write teamFile: %v", errWrite)
	}
	overwriteWithNewID := []*coreauth.Auth{
		host.AuthDataToCoreAuth(pluginapi.AuthData{
			Provider:    "zcode",
			ID:          "new-logical-id-for-team",
			FileName:    "account.json",
			StorageJSON: []byte(`{"type":"zcode","email":"user@example.com","access_token":"failed-batch-token"}`),
			Metadata:    map[string]any{"type": "zcode", "email": "user@example.com"},
			Attributes:  map[string]string{"path": teamFile},
		}, teamFile, "account.json"),
		{
			ID:       "invalid-second-record",
			Provider: "zcode",
		},
	}
	if errBatch := h.savePluginLoginRecords(context.Background(), overwriteWithNewID); errBatch == nil {
		t.Fatal("expected overwriteWithNewID batch save to fail on second record")
	}
	if _, okNewLogical := manager.GetByID("new-logical-id-for-team"); okNewLogical {
		t.Fatal("expected new-logical-id-for-team to be removed from runtime manager after failed overwrite")
	}
	afterDiskBytes, errReadAfter := os.ReadFile(teamFile)
	if errReadAfter != nil || !strings.Contains(string(afterDiskBytes), `"custom_disk_only":"keep"`) || !strings.Contains(string(afterDiskBytes), `"newer-disk-token"`) {
		t.Fatalf("expected exact pre-save disk bytes preserved, got %q (err=%v)", string(afterDiskBytes), errReadAfter)
	}
}
