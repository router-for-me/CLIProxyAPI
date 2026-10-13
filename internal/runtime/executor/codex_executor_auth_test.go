package executor

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func makeTestCodexRefreshJWT(planType, accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	authInfo := map[string]any{
		"chatgpt_account_id": accountID,
	}
	if planType != "" {
		authInfo["chatgpt_plan_type"] = planType
	}
	claimsMap := map[string]any{
		"email":                       "user@example.com",
		"https://api.openai.com/auth": authInfo,
	}
	payloadBytes, _ := json.Marshal(claimsMap)
	claims := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + claims + "."
}

func startCodexMockOAuthServers(t *testing.T, idToken string) (proxyURL string, teardown func()) {
	t.Helper()

	mockAuthServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respBody := map[string]any{
			"access_token":  "new-mock-access-token",
			"refresh_token": "new-mock-refresh-token",
			"id_token":      idToken,
			"token_type":    "Bearer",
			"expires_in":    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respBody)
	}))

	mockProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			destConn, errDial := net.Dial("tcp", mockAuthServer.Listener.Addr().String())
			if errDial != nil {
				http.Error(w, errDial.Error(), http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				_ = destConn.Close()
				http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
				return
			}
			clientConn, _, errHijack := hijacker.Hijack()
			if errHijack != nil {
				_ = destConn.Close()
				return
			}
			go func() {
				defer func() { _ = destConn.Close() }()
				defer func() { _ = clientConn.Close() }()
				_, _ = io.Copy(destConn, clientConn)
			}()
			go func() {
				defer func() { _ = destConn.Close() }()
				defer func() { _ = clientConn.Close() }()
				_, _ = io.Copy(clientConn, destConn)
			}()
			return
		}
		http.Error(w, "proxy only supports CONNECT", http.StatusBadRequest)
	}))

	// Allow self-signed cert for the mock TLS server in HTTP transport
	origTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	teardown = func() {
		http.DefaultTransport = origTransport
		mockProxy.Close()
		mockAuthServer.Close()
	}

	return mockProxy.URL, teardown
}

func TestCodexExecutorRefresh_MissingPlanTypeDefaultsToFree(t *testing.T) {
	idTokenWithoutPlan := makeTestCodexRefreshJWT("", "acc-codex-test")
	proxyURL, teardown := startCodexMockOAuthServers(t, idTokenWithoutPlan)
	defer teardown()

	cfg := &config.Config{}
	executor := NewCodexExecutor(cfg)

	storage := &codexauth.CodexTokenStorage{
		IDToken:      "old-id-token",
		AccessToken:  "old-access-token",
		RefreshToken: "old-refresh-token",
		PlanType:     "unknown",
	}

	auth := &cliproxyauth.Auth{
		ID:       "test-codex-refresh",
		Provider: "codex",
		Storage:  storage,
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"refresh_token": "valid-refresh-token",
		},
		Attributes: map[string]string{
			"plan_type": "unknown",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	refreshed, errRefresh := executor.Refresh(ctx, auth)
	if errRefresh != nil {
		t.Fatalf("executor.Refresh error = %v", errRefresh)
	}

	if got := refreshed.Attributes["plan_type"]; got != "free" {
		t.Errorf("refreshed.Attributes[plan_type] = %q, want free", got)
	}
	if got := refreshed.Metadata["plan_type"]; got != "free" {
		t.Errorf("refreshed.Metadata[plan_type] = %q, want free", got)
	}
	// Verify snapshot isolation: original storage remains unmodified
	if storage.PlanType != "unknown" {
		t.Errorf("original storage.PlanType mutated to %q, want unknown", storage.PlanType)
	}
	newStorage, ok := refreshed.Storage.(*codexauth.CodexTokenStorage)
	if !ok || newStorage == nil {
		t.Fatalf("refreshed.Storage is not *codexauth.CodexTokenStorage: %T", refreshed.Storage)
	}
	if newStorage.PlanType != "free" {
		t.Errorf("newStorage.PlanType = %q, want free", newStorage.PlanType)
	}

	// Verify persistence to credential file writes plan_type: free
	tempDir := t.TempDir()
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(tempDir)
	refreshed.Attributes[cliproxyauth.AttributePath] = filepath.Join(tempDir, "codex-refreshed.json")
	savedPath, errSave := store.Save(ctx, refreshed)
	if errSave != nil {
		t.Fatalf("store.Save error: %v", errSave)
	}
	savedBytes, errRead := os.ReadFile(savedPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error: %v", errRead)
	}
	var fileJSON map[string]any
	if errUnmarshal := json.Unmarshal(savedBytes, &fileJSON); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error: %v", errUnmarshal)
	}
	if got := fileJSON["plan_type"]; got != "free" {
		t.Errorf("credential file plan_type = %v, want free", got)
	}
}

func TestCodexExecutorRefresh_ExtractsPlanTypeWhenPresent(t *testing.T) {
	idTokenWithPlan := makeTestCodexRefreshJWT("team", "acc-codex-test-team")
	proxyURL, teardown := startCodexMockOAuthServers(t, idTokenWithPlan)
	defer teardown()

	cfg := &config.Config{}
	executor := NewCodexExecutor(cfg)

	storage := &codexauth.CodexTokenStorage{
		IDToken:      "old-id-token",
		AccessToken:  "old-access-token",
		RefreshToken: "old-refresh-token",
		PlanType:     "free",
	}

	auth := &cliproxyauth.Auth{
		ID:       "test-codex-refresh-team",
		Provider: "codex",
		Storage:  storage,
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"refresh_token": "valid-refresh-token-team",
		},
		Attributes: map[string]string{
			"plan_type": "free",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	refreshed, errRefresh := executor.Refresh(ctx, auth)
	if errRefresh != nil {
		t.Fatalf("executor.Refresh error = %v", errRefresh)
	}

	if got := refreshed.Attributes["plan_type"]; got != "team" {
		t.Errorf("refreshed.Attributes[plan_type] = %q, want team", got)
	}
	if got := refreshed.Metadata["plan_type"]; got != "team" {
		t.Errorf("refreshed.Metadata[plan_type] = %q, want team", got)
	}
	// Verify snapshot isolation: original storage remains unmodified
	if storage.PlanType != "free" {
		t.Errorf("original storage.PlanType mutated to %q, want free", storage.PlanType)
	}
	newStorage, ok := refreshed.Storage.(*codexauth.CodexTokenStorage)
	if !ok || newStorage == nil {
		t.Fatalf("refreshed.Storage is not *codexauth.CodexTokenStorage: %T", refreshed.Storage)
	}
	if newStorage.PlanType != "team" {
		t.Errorf("newStorage.PlanType = %q, want team", newStorage.PlanType)
	}

	// Verify persistence to credential file writes plan_type: team
	tempDir := t.TempDir()
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(tempDir)
	refreshed.Attributes[cliproxyauth.AttributePath] = filepath.Join(tempDir, "codex-team-refreshed.json")
	savedPath, errSave := store.Save(ctx, refreshed)
	if errSave != nil {
		t.Fatalf("store.Save error: %v", errSave)
	}
	savedBytes, errRead := os.ReadFile(savedPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error: %v", errRead)
	}
	var fileJSON map[string]any
	if errUnmarshal := json.Unmarshal(savedBytes, &fileJSON); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error: %v", errUnmarshal)
	}
	if got := fileJSON["plan_type"]; got != "team" {
		t.Errorf("credential file plan_type = %v, want team", got)
	}
}

func TestCodexExecutorRefresh_EmptyAttributesSnapshotIsolation(t *testing.T) {
	idTokenWithoutPlan := makeTestCodexRefreshJWT("", "acc-codex-empty-attrs")
	proxyURL, teardown := startCodexMockOAuthServers(t, idTokenWithoutPlan)
	defer teardown()

	cfg := &config.Config{}
	executor := NewCodexExecutor(cfg)

	emptyAttrs := map[string]string{}
	storage := &codexauth.CodexTokenStorage{
		IDToken:      "old-id-token",
		AccessToken:  "old-access-token",
		RefreshToken: "old-refresh-token",
	}

	auth := &cliproxyauth.Auth{
		ID:         "test-codex-empty-attrs",
		Provider:   "codex",
		Storage:    storage,
		ProxyURL:   proxyURL,
		Attributes: emptyAttrs,
		Metadata: map[string]any{
			"refresh_token": "valid-refresh-token",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	refreshed, errRefresh := executor.Refresh(ctx, auth)
	if errRefresh != nil {
		t.Fatalf("executor.Refresh error = %v", errRefresh)
	}

	if got := refreshed.Attributes["plan_type"]; got != "free" {
		t.Errorf("refreshed.Attributes[plan_type] = %q, want free", got)
	}
	// Verify original empty map was not modified
	if _, exists := emptyAttrs["plan_type"]; exists {
		t.Errorf("original empty Attributes map was modified, plan_type = %q", emptyAttrs["plan_type"])
	}
	if len(emptyAttrs) != 0 {
		t.Errorf("original empty Attributes map length = %d, want 0", len(emptyAttrs))
	}
}
