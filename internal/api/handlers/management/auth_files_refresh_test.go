package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type refreshRecordExecutor struct {
	provider   string
	refreshCnt atomic.Int32
}

func (e *refreshRecordExecutor) Identifier() string {
	return e.provider
}

func (e *refreshRecordExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	e.refreshCnt.Add(1)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-token"
	auth.Metadata["refresh_token"] = "refresh-token"
	auth.Metadata["expires_in"] = int64(3600)
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func (e *refreshRecordExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshRecordExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *refreshRecordExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *refreshRecordExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRefreshAuthFiles_AllAndSpecific(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()

	fileA := filepath.Join(authDir, "antigravity-1.json")
	fileB := filepath.Join(authDir, "antigravity-2.json")
	_ = os.WriteFile(fileA, []byte(`{"type":"antigravity","refresh_token":"ref-1","access_token":"old-1"}`), 0o600)
	_ = os.WriteFile(fileB, []byte(`{"type":"antigravity","refresh_token":"ref-2","access_token":"old-2"}`), 0o600)

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &refreshRecordExecutor{provider: "antigravity"}
	manager.RegisterExecutor(exec)

	auth1 := &coreauth.Auth{
		ID:       "antigravity-1.json",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "antigravity", "refresh_token": "ref-1", "access_token": "old-1"},
	}
	auth2 := &coreauth.Auth{
		ID:          "antigravity-2.json",
		Provider:    "antigravity",
		Status:      coreauth.StatusError,
		Unavailable: true,
		LastError:   &coreauth.Error{Message: "unauthorized"},
		Metadata:    map[string]any{"type": "antigravity", "refresh_token": "ref-2", "access_token": "old-2"},
	}
	_, _ = manager.Register(context.Background(), auth1)
	_, _ = manager.Register(context.Background(), auth2)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	engine := gin.New()
	engine.POST("/auth-files/refresh", h.RefreshAuthFiles)

	// 1. Refresh all
	req := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?all=true", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("expected ok=true, got %v", resp)
	}

	// Wait briefly for refresh to execute
	time.Sleep(50 * time.Millisecond)

	if cnt := exec.refreshCnt.Load(); cnt < 2 {
		t.Fatalf("expected at least 2 refreshes, got %d", cnt)
	}

	// 2. Auth2 was in StatusError, now should be active/recovering
	a2, exists := manager.GetByID("antigravity-2.json")
	if !exists || a2.Status == coreauth.StatusError {
		t.Fatalf("expected auth2 status to be recovered from error, got %+v", a2)
	}

	// 3. Refresh single file by name
	prevCnt := exec.refreshCnt.Load()
	reqSingle := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name=antigravity-1.json", nil)
	wSingle := httptest.NewRecorder()
	engine.ServeHTTP(wSingle, reqSingle)

	if wSingle.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", wSingle.Code, wSingle.Body.String())
	}
	if newCnt := exec.refreshCnt.Load(); newCnt != prevCnt+1 {
		t.Fatalf("expected cnt to increment by 1, was %d now %d", prevCnt, newCnt)
	}

	// 4. Refresh nonexistent file
	reqMissing := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name=nonexistent.json", nil)
	wMissing := httptest.NewRecorder()
	engine.ServeHTTP(wMissing, reqMissing)

	if wMissing.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", wMissing.Code, wMissing.Body.String())
	}

	// 5. Refresh via chunked JSON request body (ContentLength = -1)
	chunkedBody := strings.NewReader(`{"name":"antigravity-1.json"}`)
	reqChunked := httptest.NewRequest(http.MethodPost, "/auth-files/refresh", chunkedBody)
	reqChunked.Header.Set("Content-Type", "application/json")
	reqChunked.TransferEncoding = []string{"chunked"}
	reqChunked.ContentLength = -1
	wChunked := httptest.NewRecorder()
	engine.ServeHTTP(wChunked, reqChunked)

	if wChunked.Code != http.StatusOK {
		t.Fatalf("expected chunked request status 200, got %d: %s", wChunked.Code, wChunked.Body.String())
	}

	// 6. Malformed JSON request body returns 400
	reqBadJSON := httptest.NewRequest(http.MethodPost, "/auth-files/refresh", strings.NewReader(`{invalid`))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	wBadJSON := httptest.NewRecorder()
	engine.ServeHTTP(wBadJSON, reqBadJSON)

	if wBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("expected malformed JSON to return 400, got %d", wBadJSON.Code)
	}
}

func TestRefreshAuthFiles_PreservesPathInList_Issue6119(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "custom-plugin.json")
	_ = os.WriteFile(filePath, []byte(`{"type":"custom-plugin","token":"test"}`), 0o600)

	host := pluginhost.New()
	provider := &pluginRefreshSimProvider{
		identifier: "custom-plugin",
		refreshAuth: func(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
			// Plugin returns custom attributes without repeating path
			return pluginapi.AuthRefreshResponse{
				Auth: pluginapi.AuthData{
					Metadata:   map[string]any{"type": "custom-plugin", "token": "refreshed-token"},
					Attributes: map[string]string{"priority": "1"},
				},
			}, nil
		},
	}
	host.RegisterPluginForTest("custom-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: provider,
		},
	})

	manager := coreauth.NewManager(nil, nil, nil)
	exec := &pluginRefreshHostExecutor{provider: "custom-plugin", host: host}
	manager.RegisterExecutor(exec)

	auth := &coreauth.Auth{
		ID:       "custom-plugin.json",
		FileName: "custom-plugin.json",
		Provider: "custom-plugin",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributePath:          filePath,
			coreauth.AttributeSource:        filePath,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{"type": "custom-plugin", "token": "test"},
	}
	_, _ = manager.Register(context.Background(), auth)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	engine := gin.New()
	engine.POST("/auth-files/refresh", h.RefreshAuthFiles)
	engine.GET("/auth-files", h.ListAuthFiles)

	// Refresh the auth file
	reqRefresh := httptest.NewRequest(http.MethodPost, "/auth-files/refresh?name=custom-plugin.json", nil)
	wRefresh := httptest.NewRecorder()
	engine.ServeHTTP(wRefresh, reqRefresh)
	if wRefresh.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", wRefresh.Code, wRefresh.Body.String())
	}

	// Verify it still appears in GET /auth-files
	reqList := httptest.NewRequest(http.MethodGet, "/auth-files", nil)
	wList := httptest.NewRecorder()
	engine.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", wList.Code, wList.Body.String())
	}
	var listResp map[string]any
	if err := json.Unmarshal(wList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	files, _ := listResp["files"].([]any)
	found := false
	for _, f := range files {
		m, ok := f.(map[string]any)
		if ok && m["name"] == "custom-plugin.json" {
			found = true
			if fmt.Sprint(m["priority"]) != "1" {
				t.Errorf("expected priority '1' in auth file entry, got %v", m["priority"])
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected custom-plugin.json to appear in /auth-files list, but got: %v", listResp)
	}
}

type pluginRefreshSimProvider struct {
	identifier  string
	refreshAuth func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error)
}

func (p *pluginRefreshSimProvider) Identifier() string {
	return p.identifier
}

func (p *pluginRefreshSimProvider) ParseAuth(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return pluginapi.AuthParseResponse{}, nil
}

func (p *pluginRefreshSimProvider) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{}, nil
}

func (p *pluginRefreshSimProvider) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{}, nil
}

func (p *pluginRefreshSimProvider) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	if p.refreshAuth != nil {
		return p.refreshAuth(ctx, req)
	}
	return pluginapi.AuthRefreshResponse{}, nil
}

type pluginRefreshHostExecutor struct {
	provider string
	host     *pluginhost.Host
}

func (e *pluginRefreshHostExecutor) Identifier() string {
	return e.provider
}

func (e *pluginRefreshHostExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if e.host != nil {
		refreshed, handled, errRefresh := e.host.RefreshAuth(ctx, auth)
		if handled {
			return refreshed, errRefresh
		}
	}
	return auth, nil
}

func (e *pluginRefreshHostExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *pluginRefreshHostExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *pluginRefreshHostExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *pluginRefreshHostExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}
