package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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
	identifier string
	startLogin func(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error)
	pollLogin  func(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error)
}

func (p *testAuthProvider) Identifier() string {
	return p.identifier
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
