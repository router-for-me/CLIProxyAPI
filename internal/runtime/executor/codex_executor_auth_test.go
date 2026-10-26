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
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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

func startDualCodexAndSIWCMockOAuthServer(t *testing.T, idToken string, failSIWC *bool) (proxyURL string, outerCount *int, siwcCount *int, teardown func()) {
	t.Helper()
	var oCount, sCount int
	mockAuthServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/token":
			oCount++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "outer-access-refreshed",
				"refresh_token": "outer-refresh-refreshed",
				"id_token":      idToken,
				"token_type":    "Bearer",
				"expires_in":    720000,
			})
		case "/api/accounts/oauth/token":
			sCount++
			if failSIWC != nil && *failSIWC {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","code":"refresh_token_reused"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "siwc-access-refreshed",
				"refresh_token": "siwc-refresh-refreshed",
				"id_token":      "siwc-id-refreshed",
				"expires_in":    3600,
				"scope":         codexauth.SIWCScope,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
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

	origTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	teardown = func() {
		http.DefaultTransport = origTransport
		mockProxy.Close()
		mockAuthServer.Close()
	}
	return mockProxy.URL, &oCount, &sCount, teardown
}

func TestCodexExecutorRefreshSelectiveAndDual(t *testing.T) {
	idToken := makeTestCodexRefreshJWT("plus", "acc-dual-1")
	failSIWC := false
	proxyURL, outerCount, siwcCount, teardown := startDualCodexAndSIWCMockOAuthServer(t, idToken, &failSIWC)
	defer teardown()

	executor := NewCodexExecutor(&config.Config{})
	now := time.Now()

	// 1. Only SIWC needs refresh (outer expires in 5 days, SIWC expires in 2 minutes)
	*outerCount, *siwcCount = 0, 0
	authSIWCOnly := &cliproxyauth.Auth{
		ID:       "codex-siwc-only",
		Provider: "codex",
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-keep",
			"refresh_token": "outer-ref-keep",
			"expired":       now.Add(5 * 24 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-old",
				"refresh_token": "siwc-ref-old-1",
				"client_id":     "oaiapp_test1",
				"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
				"expired":       now.Add(2 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	beforeref1 := authSIWCOnly.Clone()
	ref1, err1 := executor.Refresh(context.Background(), authSIWCOnly)
	if err1 != nil {
		t.Fatalf("Refresh (SIWC only) error: %v", err1)
	}
	if *outerCount != 0 || *siwcCount != 1 {
		t.Fatalf("counts = outer:%d siwc:%d, want 0/1", *outerCount, *siwcCount)
	}
	if ref1.Metadata["access_token"] != "outer-keep" || ref1.Metadata["use_siwc"] != true {
		t.Fatalf("outer or use_siwc mutated: %#v", ref1.Metadata)
	}
	assertCodexRefreshPatch(t, beforeref1, ref1, false, true)
	siwc1 := ref1.Metadata["siwc"].(map[string]any)
	if siwc1["access_token"] != "siwc-access-refreshed" || siwc1["device_id"] != "e61bbe28-07ef-466d-8e5d-a344f94ab305" || siwc1["client_id"] != "oaiapp_test1" {
		t.Fatalf("siwc1 unexpected: %#v", siwc1)
	}

	// 2. Only outer needs refresh (outer expires in 2 hours, SIWC expires in 30 minutes)
	*outerCount, *siwcCount = 0, 0
	authOuterOnly := &cliproxyauth.Auth{
		ID:       "codex-outer-only",
		Provider: "codex",
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-old",
			"refresh_token": "outer-ref-old-2",
			"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-keep",
				"refresh_token": "siwc-ref-keep",
				"client_id":     "oaiapp_test2",
				"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
				"expired":       now.Add(30 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	beforeref2 := authOuterOnly.Clone()
	ref2, err2 := executor.Refresh(context.Background(), authOuterOnly)
	if err2 != nil {
		t.Fatalf("Refresh (outer only) error: %v", err2)
	}
	if *outerCount != 1 || *siwcCount != 0 {
		t.Fatalf("counts = outer:%d siwc:%d, want 1/0", *outerCount, *siwcCount)
	}
	if ref2.Metadata["access_token"] != "outer-access-refreshed" || ref2.Metadata["use_siwc"] != true {
		t.Fatalf("outer or use_siwc unexpected: %#v", ref2.Metadata)
	}
	assertCodexRefreshPatch(t, beforeref2, ref2, true, false)
	siwc2 := ref2.Metadata["siwc"].(map[string]any)
	if siwc2["access_token"] != "siwc-keep" || siwc2["device_id"] != "e61bbe28-07ef-466d-8e5d-a344f94ab305" {
		t.Fatalf("siwc2 mutated: %#v", siwc2)
	}

	// 3. Both outer and SIWC need refresh simultaneously
	*outerCount, *siwcCount = 0, 0
	authBoth := &cliproxyauth.Auth{
		ID:       "codex-both",
		Provider: "codex",
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-old-3",
			"refresh_token": "outer-ref-old-3",
			"expired":       now.Add(1 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-old-3",
				"refresh_token": "siwc-ref-old-3",
				"client_id":     "oaiapp_test3",
				"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
				"expired":       now.Add(1 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	beforeref3 := authBoth.Clone()
	ref3, err3 := executor.Refresh(context.Background(), authBoth)
	if err3 != nil {
		t.Fatalf("Refresh (both) error: %v", err3)
	}
	if *outerCount != 1 || *siwcCount != 1 {
		t.Fatalf("counts = outer:%d siwc:%d, want 1/1", *outerCount, *siwcCount)
	}
	if ref3.Metadata["access_token"] != "outer-access-refreshed" || ref3.Metadata["use_siwc"] != true {
		t.Fatalf("ref3 outer/use_siwc unexpected: %#v", ref3.Metadata)
	}
	assertCodexRefreshPatch(t, beforeref3, ref3, true, true)
	siwc3 := ref3.Metadata["siwc"].(map[string]any)
	if siwc3["access_token"] != "siwc-access-refreshed" || siwc3["device_id"] != "e61bbe28-07ef-466d-8e5d-a344f94ab305" {
		t.Fatalf("siwc3 unexpected: %#v", siwc3)
	}

	// 4. Partial success when both need refresh and SIWC fails: outer rotated token must still be returned without error
	*outerCount, *siwcCount = 0, 0
	failSIWC = true
	authPartial := &cliproxyauth.Auth{
		ID:       "codex-partial",
		Provider: "codex",
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-old-4",
			"refresh_token": "outer-ref-old-4",
			"expired":       now.Add(1 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-old-4",
				"refresh_token": "siwc-ref-old-4",
				"client_id":     "oaiapp_test4",
				"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
				"expired":       now.Add(1 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	beforeref4 := authPartial.Clone()
	ref4, err4 := executor.Refresh(context.Background(), authPartial)
	if err4 != nil {
		t.Fatalf("expected partial success without error so outer rotated token is persisted, got err: %v", err4)
	}
	if ref4.Metadata["access_token"] != "outer-access-refreshed" {
		t.Fatalf("outer rotated token not preserved: %v", ref4.Metadata["access_token"])
	}
	assertCodexRefreshPatch(t, beforeref4, ref4, true, false)
	siwc4 := ref4.Metadata["siwc"].(map[string]any)
	if siwc4["access_token"] != "siwc-old-4" {
		t.Fatalf("failed siwc should keep old token for retry, got: %#v", siwc4)
	}

	// 5. When outer is due (within 24h) and SIWC is not due by time (30m left),
	// a forced refresh or rejected SIWC access token must still refresh SIWC!
	failSIWC = false
	*outerCount, *siwcCount = 0, 0
	authRejectedSIWC := &cliproxyauth.Auth{
		ID:                  "codex-rejected-siwc",
		Provider:            "codex",
		ProxyURL:            proxyURL,
		RejectedAccessToken: "siwc-revoked-early",
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-due",
			"refresh_token": "outer-ref-due",
			"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-revoked-early",
				"refresh_token": "siwc-ref-revoked",
				"client_id":     "oaiapp_test5",
				"expired":       now.Add(30 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	ref5, err5 := executor.Refresh(context.Background(), authRejectedSIWC)
	if err5 != nil {
		t.Fatalf("Refresh (rejected SIWC) error: %v", err5)
	}
	if *outerCount != 1 || *siwcCount != 1 {
		t.Fatalf("counts for rejected SIWC = outer:%d siwc:%d, want 1/1", *outerCount, *siwcCount)
	}
	if ref5.Metadata["siwc"].(map[string]any)["access_token"] != "siwc-access-refreshed" {
		t.Fatalf("rejected SIWC token was not refreshed: %#v", ref5.Metadata["siwc"])
	}

	// 6. When Auth carries a CodexTokenStorage, Refresh must attach RefreshPatch to Storage too
	// so direct Storage.SaveTokenToFile preserves concurrent on-disk SIWC binds.
	*outerCount, *siwcCount = 0, 0
	tmpDir := t.TempDir()
	storagePath := tmpDir + "/codex-storage.json"
	authWithStorage := &cliproxyauth.Auth{
		ID:       "codex-with-storage",
		Provider: "codex",
		ProxyURL: proxyURL,
		Storage: &codexauth.CodexTokenStorage{
			AccessToken:  "outer-old-s",
			RefreshToken: "outer-ref-s",
		},
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-old-s",
			"refresh_token": "outer-ref-s",
			"expired":       now.Add(2 * time.Hour).Format(time.RFC3339),
			"use_siwc":      false,
			"siwc": map[string]any{
				"access_token":  "siwc-old-s",
				"refresh_token": "siwc-ref-s",
				"client_id":     "oaiapp_s",
				"expired":       now.Add(30 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	// Simulate concurrent -siwc-bind writing a new SIWC and use_siwc=true on disk
	concurrentDisk := map[string]any{
		"type":          "codex",
		"access_token":  "outer-old-s",
		"refresh_token": "outer-ref-s",
		"use_siwc":      true,
		"siwc": map[string]any{
			"access_token":  "siwc-bound-concurrently",
			"refresh_token": "siwc-ref-bound-concurrently",
			"client_id":     "oaiapp_bound",
		},
	}
	if errWrite := codexauth.WriteJSONFileAtomic(storagePath, concurrentDisk); errWrite != nil {
		t.Fatalf("WriteJSONFileAtomic: %v", errWrite)
	}
	ref6, err6 := executor.Refresh(context.Background(), authWithStorage)
	if err6 != nil {
		t.Fatalf("Refresh (storage) error: %v", err6)
	}
	if errSave := ref6.Storage.SaveTokenToFile(storagePath); errSave != nil {
		t.Fatalf("Storage.SaveTokenToFile error: %v", errSave)
	}
	persistedRaw, errRead := os.ReadFile(storagePath)
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	var persistedMap map[string]any
	if errUnmarshal := json.Unmarshal(persistedRaw, &persistedMap); errUnmarshal != nil {
		t.Fatalf("Unmarshal: %v", errUnmarshal)
	}
	if persistedMap["access_token"] != "outer-access-refreshed" || persistedMap["use_siwc"] != true {
		t.Fatalf("Storage.SaveTokenToFile did not apply RefreshPatch properly: %#v", persistedMap)
	}
	if persistedMap["siwc"].(map[string]any)["access_token"] != "siwc-bound-concurrently" {
		t.Fatalf("Storage.SaveTokenToFile overwrote concurrent SIWC bind: %#v", persistedMap["siwc"])
	}
}

func TestCodexExecutorUseSIWCExecuteAndStream(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\n"))
	}))
	defer upstream.Close()

	auth := &cliproxyauth.Auth{
		ID:       "codex-siwc-req",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":   upstream.URL + "/v1",
			"websockets": "true",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-token-should-not-be-used",
			"account_id":   "acct-should-not-be-sent",
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token":  "siwc-direct-access-token",
				"refresh_token": "siwc-direct-refresh-token",
				"client_id":     "oaiapp_direct",
			},
		},
	}

	if !codexUseSIWC(auth) {
		t.Fatal("expected codexUseSIWC(auth) == true")
	}
	if !codexWebsocketsEnabled(auth) {
		t.Fatal("expected codexWebsocketsEnabled(auth) == true when use_siwc is true and websockets is enabled")
	}
	// Default baseURL when base_url attribute is empty must be https://api.openai.com/v1
	authDefaultURL := auth.Clone()
	delete(authDefaultURL.Attributes, "base_url")
	tok, defBase := codexCreds(authDefaultURL)
	if tok != "siwc-direct-access-token" || defBase != "https://api.openai.com/v1" {
		t.Fatalf("codexCreds = (%q, %q), want (siwc-direct-access-token, https://api.openai.com/v1)", tok, defBase)
	}

	executor := NewCodexExecutor(&config.Config{})
	reqPayload := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":1024,"temperature":0.7,"prompt_cache_retention":"24h","prompt_cache_options":{"ttl":"30m"},"prompt_cache_key":"sess-123"}`)
	clientHeaders := http.Header{}
	clientHeaders.Set("User-Agent", "curl/8.7.1")
	_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: reqPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Headers:      clientHeaders,
	})
	if errExec != nil {
		t.Fatalf("Execute error: %v", errExec)
	}

	if gotPath != "/v1/responses" {
		t.Fatalf("gotPath = %q, want /v1/responses", gotPath)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer siwc-direct-access-token" {
		t.Fatalf("Authorization = %q, want Bearer siwc-direct-access-token", got)
	}
	if got := gotHeaders.Get("Chatgpt-Account-Id"); got != "" {
		t.Fatalf("Chatgpt-Account-Id should not be sent in SIWC mode, got %q", got)
	}
	if got := gotHeaders.Get("Originator"); got != "" {
		t.Fatalf("Originator should not be sent in SIWC mode, got %q", got)
	}
	if got := gotHeaders.Get("X-Codex-Routing-Hint"); got != "" {
		t.Fatalf("X-Codex-Routing-Hint should not be sent in SIWC mode, got %q", got)
	}
	if got := gotHeaders.Get("session_id"); got != "sess-123" {
		t.Fatalf("session_id = %q, want sess-123", got)
	}
	if got := gotHeaders.Get("X-Client-Request-Id"); got != "sess-123" {
		t.Fatalf("X-Client-Request-Id = %q, want sess-123", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != codexSIWCUserAgent(nil) {
		t.Fatalf("User-Agent = %q, want %q (client UA must not override SIWC UA)", got, codexSIWCUserAgent(nil))
	}

	// Verify custom signin-with-chatgpt-identifier config overrides SIWC User-Agent
	var customCfg config.Config
	customCfg.Codex.SignInWithChatGPTIdentifier = "MyCustomSIWCAgent"
	customExec := NewCodexExecutor(&customCfg)
	_, errCustomExec := customExec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: reqPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Headers:      clientHeaders,
	})
	if errCustomExec != nil {
		t.Fatalf("Execute (custom SIWC UA) error: %v", errCustomExec)
	}
	if got := gotHeaders.Get("User-Agent"); got != codexSIWCUserAgent(&customCfg) {
		t.Fatalf("custom User-Agent = %q, want %q", got, codexSIWCUserAgent(&customCfg))
	}
	for _, forbidden := range []string{"max_output_tokens", "temperature", "prompt_cache_retention", "prompt_cache_options", "instructions", "tools"} {
		if _, exists := gotBody[forbidden]; exists {
			t.Fatalf("field %q should be omitted in SIWC mode, got %#v", forbidden, gotBody[forbidden])
		}
	}
	if gotBody["store"] != false || gotBody["stream"] != true {
		t.Fatalf("store/stream = %v/%v, want false/true", gotBody["store"], gotBody["stream"])
	}

	// Also test ExecuteStream with user payload rule to verify PayloadFinalizer is the final semantic barrier
	cfgWithRules := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5.4", Protocol: "codex"}},
					Params: map[string]any{"service_tier": "priority"},
				},
			},
		},
	}
	streamExec := NewCodexExecutor(cfgWithRules)
	sr, errStream := streamExec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: reqPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream error: %v", errStream)
	}
	for range sr.Chunks {
	}
	if gotBody["service_tier"] != "priority" {
		t.Fatalf("expected user payload rule to set service_tier=priority after SIWC body cleanup, got %v", gotBody["service_tier"])
	}
	for _, forbidden := range []string{"max_output_tokens", "temperature", "prompt_cache_retention", "prompt_cache_options", "instructions", "tools"} {
		if _, exists := gotBody[forbidden]; exists {
			t.Fatalf("ExecuteStream field %q should be omitted in SIWC mode, got %#v", forbidden, gotBody[forbidden])
		}
	}
}

func TestCodexWebsocketsExecutorUseSIWCExecuteAndStream(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var gotPath string
	var gotHeaders http.Header
	var gotFrame map[string]any

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		_ = json.Unmarshal(payload, &gotFrame)
		completed := []byte(`{"type":"response.completed","response":{"id":"resp_ws_siwc","status":"completed","model":"gpt-5.4","output":[{"type":"message","id":"msg_ws_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello via ws"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`)
		_ = conn.WriteMessage(websocket.TextMessage, completed)
	}))
	defer wsServer.Close()

	auth := &cliproxyauth.Auth{
		ID:       "codex-siwc-ws",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url":   wsServer.URL + "/v1",
			"websockets": "true",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-token-unused",
			"account_id":   "acct-unused",
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token":  "siwc-ws-access-token",
				"refresh_token": "siwc-ws-refresh-token",
				"client_id":     "oaiapp_ws",
			},
		},
	}

	// Verify default wsURL is wss://api.openai.com/v1/responses when base_url is unset
	authDefaultURL := auth.Clone()
	delete(authDefaultURL.Attributes, "base_url")
	wsExecDefault := NewCodexWebsocketsExecutor(&config.Config{})
	preparedDefault, errPrep := wsExecDefault.prepareCodexWebsocketStream(context.Background(), authDefaultURL, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errPrep != nil {
		t.Fatalf("prepareCodexWebsocketStream (default URL) error: %v", errPrep)
	}
	if preparedDefault.wsURL != "wss://api.openai.com/v1/responses" {
		t.Fatalf("default SIWC wsURL = %q, want wss://api.openai.com/v1/responses", preparedDefault.wsURL)
	}

	cfgWithRules := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "gpt-5.4", Protocol: "codex"}},
					Params: map[string]any{"service_tier": "priority"},
				},
			},
		},
	}
	cfgWithRules.Codex.SignInWithChatGPTIdentifier = "CustomSIWCWS"

	autoExec := NewCodexAutoExecutor(cfgWithRules)
	reqPayload := []byte(`{"model":"gpt-5.4","stream":true,"background":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":1024,"temperature":0.7,"prompt_cache_retention":"24h","prompt_cache_options":{"ttl":"30m"},"prompt_cache_key":"ws-sess-456"}`)
	clientHeaders := http.Header{}
	clientHeaders.Set("User-Agent", "curl/8.7.1")
	wsCtx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	_, errExec := autoExec.Execute(wsCtx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: reqPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Headers:      clientHeaders,
	})
	if errExec != nil {
		t.Fatalf("AutoExecutor.Execute (WebSocket SIWC) error: %v", errExec)
	}

	if gotPath != "/v1/responses" {
		t.Fatalf("gotPath = %q, want /v1/responses", gotPath)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer siwc-ws-access-token" {
		t.Fatalf("Authorization = %q, want Bearer siwc-ws-access-token", got)
	}
	if got := gotHeaders.Get("OpenAI-Beta"); !strings.Contains(got, "responses_websockets=") {
		t.Fatalf("OpenAI-Beta = %q, want responses_websockets=...", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != codexSIWCUserAgent(cfgWithRules) {
		t.Fatalf("User-Agent = %q, want %q", got, codexSIWCUserAgent(cfgWithRules))
	}
	if got := gotHeaders.Get("session_id"); got != "ws-sess-456" {
		t.Fatalf("session_id = %q, want ws-sess-456", got)
	}
	if got := gotHeaders.Get("X-Client-Request-Id"); got != "ws-sess-456" {
		t.Fatalf("X-Client-Request-Id = %q, want ws-sess-456", got)
	}
	for _, forbiddenHeader := range []string{"ChatGPT-Account-ID", "Originator", "X-Codex-Routing-Hint", "Conversation_id"} {
		if got := gotHeaders.Get(forbiddenHeader); got != "" {
			t.Fatalf("header %q should not be sent on SIWC WebSocket handshake, got %q", forbiddenHeader, got)
		}
	}
	if gotFrame["type"] != "response.create" || gotFrame["store"] != false || gotFrame["service_tier"] != "priority" {
		t.Fatalf("unexpected SIWC WebSocket frame: %#v", gotFrame)
	}
	for _, forbidden := range []string{"stream", "background", "max_output_tokens", "temperature", "prompt_cache_retention", "prompt_cache_options", "instructions", "tools"} {
		if _, exists := gotFrame[forbidden]; exists {
			t.Fatalf("field %q should be omitted in SIWC WebSocket frame: %#v", forbidden, gotFrame)
		}
	}

	// Stream over WebSocket with SIWC
	gotFrame = nil
	sr, errStream := autoExec.ExecuteStream(wsCtx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: reqPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Headers:      clientHeaders,
	})
	if errStream != nil {
		t.Fatalf("AutoExecutor.ExecuteStream (WebSocket SIWC) error: %v", errStream)
	}
	for range sr.Chunks {
	}
	if gotFrame["type"] != "response.create" || gotFrame["store"] != false || gotFrame["service_tier"] != "priority" {
		t.Fatalf("unexpected SIWC WebSocket stream frame: %#v", gotFrame)
	}
	for _, forbidden := range []string{"stream", "background", "max_output_tokens", "temperature", "prompt_cache_retention", "prompt_cache_options", "instructions", "tools"} {
		if _, exists := gotFrame[forbidden]; exists {
			t.Fatalf("ExecuteStream field %q should be omitted in SIWC WebSocket frame: %#v", forbidden, gotFrame)
		}
	}

	// Missing SIWC token must fail closed on WebSocket Execute and ExecuteStream
	authMissing := auth.Clone()
	delete(authMissing.Metadata, "siwc")
	if _, errMissingExec := autoExec.Execute(wsCtx, authMissing, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: reqPayload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); errMissingExec == nil {
		t.Fatal("expected WebSocket Execute to fail when use_siwc=true and siwc token is missing")
	}
	if _, errMissingStream := autoExec.ExecuteStream(wsCtx, authMissing, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: reqPayload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); errMissingStream == nil {
		t.Fatal("expected WebSocket ExecuteStream to fail when use_siwc=true and siwc token is missing")
	}
}

func TestCodexExecutorUseSIWCFailsClosedWhenMissingSIWCToken(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       "codex-siwc-missing",
		Provider: "codex",
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-fallback-token",
			"use_siwc":     true,
		},
	}
	if !codexUseSIWC(auth) {
		t.Fatal("expected codexUseSIWC(auth) == true when use_siwc is true")
	}
	tok, baseURL := codexCreds(auth)
	if tok != "" || baseURL != defaultOpenAISIWCBaseURL {
		t.Fatalf("codexCreds(use_siwc=true without siwc token) = (%q, %q), want (empty, %q)", tok, baseURL, defaultOpenAISIWCBaseURL)
	}
	executor := NewCodexExecutor(&config.Config{})
	_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errExec == nil {
		t.Fatal("expected Execute to fail when use_siwc=true and siwc access_token is missing")
	}
	_, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errStream == nil {
		t.Fatal("expected ExecuteStream to fail when use_siwc=true and siwc access_token is missing")
	}

	// Even if Attributes["api_key"] is present, use_siwc=true must not fall back to api_key
	authWithAPIKey := auth.Clone()
	authWithAPIKey.Attributes = map[string]string{"api_key": "sk-outer-attr"}
	if tok2, _ := codexCreds(authWithAPIKey); tok2 != "" {
		t.Fatalf("codexCreds with api_key and missing siwc token = %q, want empty", tok2)
	}
	authWithAPIKeyAndSIWC := authWithAPIKey.Clone()
	authWithAPIKeyAndSIWC.Metadata["siwc"] = map[string]any{"access_token": "siwc-wins-over-api-key"}
	if tok3, _ := codexCreds(authWithAPIKeyAndSIWC); tok3 != "siwc-wins-over-api-key" {
		t.Fatalf("codexCreds with api_key and siwc token = %q, want siwc-wins-over-api-key", tok3)
	}
}

func TestCodexExecutorUseSIWCImageRequestsRouteThroughResponsesSSE(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_img\",\"created_at\":1700000000,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"output_format\":\"png\"}]}}\n\n"))
	}))
	defer srv.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "codex-siwc-img",
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": srv.URL + "/v1",
		},
		Metadata: map[string]any{
			"type":     "codex",
			"use_siwc": true,
			"siwc": map[string]any{
				"access_token": "siwc-img-token",
				"client_id":    "oaiapp_img",
			},
		},
	}

	imgOpts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Metadata:     map[string]any{"request_path": "/v1/images/generations"},
	}
	// Even with gpt-image-1.5 (which normally uses direct /images/generations), use_siwc=true must route via /v1/responses SSE
	_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-1.5",
		Payload: []byte(`{"model":"gpt-image-1.5","prompt":"a cute cat"}`),
	}, imgOpts)
	if errExec != nil {
		t.Fatalf("Execute image with use_siwc error: %v", errExec)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("image request path = %q, want /v1/responses", gotPath)
	}
	if gotHeaders.Get("Authorization") != "Bearer siwc-img-token" {
		t.Fatalf("Authorization = %q, want Bearer siwc-img-token", gotHeaders.Get("Authorization"))
	}
	if _, hasInst := gotBody["instructions"]; hasInst {
		t.Fatalf("empty instructions should be stripped on SIWC image request: %#v", gotBody)
	}

	sr, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-1.5",
		Payload: []byte(`{"model":"gpt-image-1.5","prompt":"a cute cat","stream":true}`),
	}, imgOpts)
	if errStream != nil {
		t.Fatalf("ExecuteStream image with use_siwc error: %v", errStream)
	}
	for range sr.Chunks {
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("image stream request path = %q, want /v1/responses", gotPath)
	}

	// Missing SIWC token must fail closed on image requests too
	authNoSIWCToken := auth.Clone()
	delete(authNoSIWCToken.Metadata, "siwc")
	if _, errMissing := executor.Execute(context.Background(), authNoSIWCToken, cliproxyexecutor.Request{
		Model:   "gpt-image-1.5",
		Payload: []byte(`{"model":"gpt-image-1.5","prompt":"a cute cat"}`),
	}, imgOpts); errMissing == nil {
		t.Fatal("expected image Execute to fail when use_siwc=true and siwc token is missing")
	}
}

func TestCodexExecutorRefreshHomeModeRefreshesSIWCWhenOnlySIWCDue(t *testing.T) {
	idToken := makeTestCodexRefreshJWT("plus", "acc-home-1")
	failSIWC := false
	proxyURL, outerCount, siwcCount, teardown := startDualCodexAndSIWCMockOAuthServer(t, idToken, &failSIWC)
	defer teardown()

	now := time.Now().UTC()
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	executor := NewCodexExecutor(cfg)

	// Outer is valid for 5 days (not due), SIWC is due in 2m (< 5m lead)
	authSIWCOnly := &cliproxyauth.Auth{
		ID:       "codex-home-siwc-only",
		Provider: "codex",
		ProxyURL: proxyURL,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-home-keep",
			"refresh_token": "outer-home-ref",
			"expired":       now.Add(5 * 24 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-home-old",
				"refresh_token": "siwc-home-ref-old",
				"client_id":     "oaiapp_home",
				"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
				"expired":       now.Add(2 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	ref, err := executor.Refresh(context.Background(), authSIWCOnly)
	if err != nil {
		t.Fatalf("Refresh (Home mode SIWC-only due) error: %v", err)
	}
	if *outerCount != 0 || *siwcCount != 1 {
		t.Fatalf("Home mode SIWC-only counts = outer:%d siwc:%d, want 0/1", *outerCount, *siwcCount)
	}
	if ref.Metadata["siwc"].(map[string]any)["access_token"] != "siwc-access-refreshed" {
		t.Fatalf("Home mode SIWC-only did not refresh siwc: %#v", ref.Metadata["siwc"])
	}

	// Verify that a Home-refreshed outer token (without SIWC) carries CodexRefreshPatch
	// so saving via FileTokenStore preserves the new outer token instead of reverting to disk's old token.
	tmpDir := t.TempDir()
	homeFile := filepath.Join(tmpDir, "codex-home-outer.json")
	homeDiskInitial := map[string]any{
		"type":          "codex",
		"access_token":  "outer-home-disk-old",
		"refresh_token": "outer-home-ref-disk-old",
		"last_refresh":  now.Add(-2 * time.Hour).Format(time.RFC3339),
	}
	if err := codexauth.WriteJSONFileAtomic(homeFile, homeDiskInitial); err != nil {
		t.Fatalf("WriteJSONFileAtomic homeFile: %v", err)
	}
	homeAfter := map[string]any{
		"type":          "codex",
		"access_token":  "outer-home-refreshed-new",
		"refresh_token": "outer-home-ref-refreshed-new",
		"last_refresh":  now.Format(time.RFC3339),
	}
	homeAuth := &cliproxyauth.Auth{
		ID:                "codex-home-outer.json",
		FileName:          "codex-home-outer.json",
		Provider:          "codex",
		Attributes:        map[string]string{cliproxyauth.AttributePath: homeFile},
		Metadata:          homeAfter,
		CodexRefreshPatch: codexauth.NewCredentialRefreshPatchWithDiskBaseline(homeDiskInitial, homeDiskInitial, homeAfter, true, false),
	}
	fStore := fileauth.NewFileTokenStore()
	fStore.SetBaseDir(tmpDir)
	if _, errSave := fStore.Save(context.Background(), homeAuth); errSave != nil {
		t.Fatalf("FileTokenStore.Save homeAuth error: %v", errSave)
	}
	if homeAuth.Metadata["access_token"] != "outer-home-refreshed-new" {
		t.Fatalf("FileTokenStore overwrote Home-refreshed outer token: %#v", homeAuth.Metadata)
	}
}

func TestCodexExecutorRefreshSerializesUnderFileLockAndSkipsAlreadyRotatedToken(t *testing.T) {
	idToken := makeTestCodexRefreshJWT("plus", "acc-lock-1")
	failSIWC := false
	proxyURL, outerCount, siwcCount, teardown := startDualCodexAndSIWCMockOAuthServer(t, idToken, &failSIWC)
	defer teardown()

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "codex-shared.json")
	now := time.Now().UTC()
	diskInitial := map[string]any{
		"type":          "codex",
		"access_token":  "outer-shared-keep",
		"refresh_token": "outer-shared-ref",
		"expired":       now.Add(5 * 24 * time.Hour).Format(time.RFC3339),
		"use_siwc":      true,
		"siwc": map[string]any{
			"access_token":  "siwc-shared-old",
			"refresh_token": "siwc-shared-ref-old",
			"client_id":     "oaiapp_shared",
			"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
			"expired":       now.Add(2 * time.Minute).Format(time.RFC3339),
		},
	}
	if err := codexauth.WriteJSONFileAtomic(authPath, diskInitial); err != nil {
		t.Fatalf("WriteJSONFileAtomic: %v", err)
	}

	exec1 := NewCodexExecutor(&config.Config{AuthDir: tmpDir})
	exec2 := NewCodexExecutor(&config.Config{AuthDir: tmpDir})

	auth1 := &cliproxyauth.Auth{
		ID:         "codex-shared.json",
		FileName:   "codex-shared.json",
		Provider:   "codex",
		ProxyURL:   proxyURL,
		Attributes: map[string]string{cliproxyauth.AttributePath: authPath},
		Metadata:   diskInitial,
	}
	auth2 := auth1.Clone()

	// First process refreshes and updates diskPath inside WithFileLock
	ref1, err1 := exec1.Refresh(context.Background(), auth1)
	if err1 != nil {
		t.Fatalf("exec1.Refresh error: %v", err1)
	}
	// Second process holding the stale in-memory snapshot refreshes against the same file;
	// it must re-read disk inside WithFileLock, see the rotated SIWC token, and skip a duplicate OAuth call!
	ref2, err2 := exec2.Refresh(context.Background(), auth2)
	if err2 != nil {
		t.Fatalf("exec2.Refresh error: %v", err2)
	}
	if *outerCount != 0 || *siwcCount != 1 {
		t.Fatalf("expected exactly 1 upstream SIWC refresh across both callers, got outer:%d siwc:%d", *outerCount, *siwcCount)
	}
	if ref1.Metadata["siwc"].(map[string]any)["access_token"] != "siwc-access-refreshed" ||
		ref2.Metadata["siwc"].(map[string]any)["access_token"] != "siwc-access-refreshed" {
		t.Fatalf("both callers should hold the refreshed SIWC token: ref1=%#v ref2=%#v", ref1.Metadata["siwc"], ref2.Metadata["siwc"])
	}

	// Now test A -> B -> C: memory holds version A, disk holds version B which is ALSO due for refresh,
	// and Refresh rotates B -> C and persists C to both disk and memory without losing C!
	*outerCount, *siwcCount = 0, 0
	pathABC := filepath.Join(tmpDir, "codex-abc.json")
	diskVersionB := map[string]any{
		"type":          "codex",
		"access_token":  "outer-B",
		"refresh_token": "outer-ref-B",
		"expired":       now.Add(1 * time.Hour).Format(time.RFC3339),
		"use_siwc":      true,
		"siwc": map[string]any{
			"access_token":  "siwc-B",
			"refresh_token": "siwc-ref-B",
			"client_id":     "oaiapp_B",
			"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
			"expired":       now.Add(1 * time.Minute).Format(time.RFC3339),
		},
	}
	if err := codexauth.WriteJSONFileAtomic(pathABC, diskVersionB); err != nil {
		t.Fatalf("WriteJSONFileAtomic B: %v", err)
	}
	memVersionA := &cliproxyauth.Auth{
		ID:         "codex-abc.json",
		FileName:   "codex-abc.json",
		Provider:   "codex",
		ProxyURL:   proxyURL,
		Attributes: map[string]string{cliproxyauth.AttributePath: pathABC},
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-A",
			"refresh_token": "outer-ref-A",
			"expired":       now.Add(1 * time.Hour).Format(time.RFC3339),
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-A",
				"refresh_token": "siwc-ref-A",
				"client_id":     "oaiapp_A",
				"device_id":     "e61bbe28-07ef-466d-8e5d-a344f94ab305",
				"expired":       now.Add(1 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	refC, errC := exec1.Refresh(context.Background(), memVersionA)
	if errC != nil {
		t.Fatalf("Refresh A->B->C error: %v", errC)
	}
	if refC.Metadata["access_token"] != "outer-access-refreshed" ||
		refC.Metadata["siwc"].(map[string]any)["access_token"] != "siwc-access-refreshed" {
		t.Fatalf("A->B->C lost refreshed C tokens in memory: %#v", refC.Metadata)
	}
	rawABC, errReadABC := os.ReadFile(pathABC)
	if errReadABC != nil {
		t.Fatalf("ReadFile ABC: %v", errReadABC)
	}
	var savedABC map[string]any
	if errUnmarshal := json.Unmarshal(rawABC, &savedABC); errUnmarshal != nil {
		t.Fatalf("Unmarshal ABC: %v", errUnmarshal)
	}
	if savedABC["access_token"] != "outer-access-refreshed" ||
		savedABC["siwc"].(map[string]any)["access_token"] != "siwc-access-refreshed" {
		t.Fatalf("A->B->C lost refreshed C tokens on disk: %#v", savedABC)
	}

	// Corrupted JSON on disk must fail before making any network refresh request
	*outerCount, *siwcCount = 0, 0
	corruptPath := filepath.Join(tmpDir, "codex-corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("WriteFile corrupt: %v", err)
	}
	authCorrupt := memVersionA.Clone()
	authCorrupt.Attributes = map[string]string{cliproxyauth.AttributePath: corruptPath}
	if _, errCorrupt := exec1.Refresh(context.Background(), authCorrupt); errCorrupt == nil {
		t.Fatal("expected error when credential file on disk contains corrupt JSON")
	}
	if *outerCount != 0 || *siwcCount != 0 {
		t.Fatalf("corrupt disk JSON should abort before upstream OAuth call, got outer:%d siwc:%d", *outerCount, *siwcCount)
	}
}

func assertCodexRefreshPatch(t *testing.T, before, refreshed *cliproxyauth.Auth, outer, siwc bool) {
	t.Helper()
	if refreshed.CodexRefreshPatch == nil {
		t.Fatal("refresh did not provide an explicit credential patch")
	}
	disk := before.Clone().Metadata
	disk["use_siwc"] = false
	disk["new_property"] = "keep"
	if !outer {
		disk["access_token"] = "outer-concurrent"
		disk["refresh_token"] = "outer-ref-concurrent"
	}
	if !siwc {
		disk["siwc"].(map[string]any)["access_token"] = "siwc-concurrent"
		disk["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-concurrent"
	}
	saved := refreshed.CodexRefreshPatch.Apply(disk)
	wantOuter, wantSIWC := disk["refresh_token"], disk["siwc"].(map[string]any)["refresh_token"]
	if outer {
		wantOuter = refreshed.Metadata["refresh_token"]
	}
	if siwc {
		wantSIWC = refreshed.Metadata["siwc"].(map[string]any)["refresh_token"]
	}
	if saved["refresh_token"] != wantOuter || saved["siwc"].(map[string]any)["refresh_token"] != wantSIWC || saved["use_siwc"] != false || saved["new_property"] != "keep" {
		t.Fatalf("refresh patch changed an unrefreshed partition or configuration: %#v", saved)
	}
}

func TestCodexExecutorCodexClientIgnoresUseSIWC(t *testing.T) {
	codexUserAgents := []string{
		"codex-desktop/0.159.0 (Mac OS 26.5.2; arm64)",
		"Codex Desktop/26.803.41515",
		"codex-rs/0.154.0 (Mac OS 26.5.2; arm64)",
		"codex_cli_rs/0.154.0 (Mac OS 26.5.2; arm64)",
		"codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)",
		"codex_exec/0.153.2 (Mac OS 26.6.2; arm64)",
	}

	for _, ua := range codexUserAgents {
		t.Run(ua, func(t *testing.T) {
			var gotPath string
			var gotHeaders http.Header
			var gotBody map[string]any

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotHeaders = r.Header.Clone()
				raw, _ := io.ReadAll(r.Body)
				gotBody = nil
				_ = json.Unmarshal(raw, &gotBody)
				if strings.HasSuffix(r.URL.Path, "/compact") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
			}))
			defer upstream.Close()

			auth := &cliproxyauth.Auth{
				ID:       "codex-siwc-client-check",
				Provider: "codex",
				Attributes: map[string]string{
					"base_url":   upstream.URL,
					"use_siwc":   "true",
					"websockets": "true",
				},
				Metadata: map[string]any{
					"type":         "codex",
					"access_token": "outer-non-siwc-token",
					"account_id":   "acct-non-siwc",
					"use_siwc":     true,
					"siwc": map[string]any{
						"access_token":  "siwc-token-must-not-be-used",
						"refresh_token": "siwc-refresh-token",
						"client_id":     "oaiapp_direct",
					},
				},
			}

			cfg := &config.Config{
				Payload: config.PayloadConfig{
					Override: []config.PayloadRule{
						{
							Models: []config.PayloadModelRule{{Name: "gpt-5.4", Protocol: "codex"}},
							Params: map[string]any{"service_tier": "priority"},
						},
					},
				},
			}
			cfg.Codex.SignInWithChatGPTIdentifier = "codex-desktop"
			executor := NewCodexExecutor(cfg)
			reqPayload := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"prompt_cache_options":{"ttl":"30m"},"prompt_cache_key":"sess-codex"}`)
			headers := http.Header{"User-Agent": []string{ua}}

			// 1. HTTP Execute with Codex client UA must use outer non-SIWC credentials and rules.
			_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: reqPayload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      headers,
			})
			if errExec != nil {
				t.Fatalf("Execute error: %v", errExec)
			}
			if got := gotHeaders.Get("Authorization"); got != "Bearer outer-non-siwc-token" {
				t.Fatalf("Authorization = %q, want Bearer outer-non-siwc-token", got)
			}
			if got := gotHeaders.Get("Chatgpt-Account-Id"); got != "acct-non-siwc" {
				t.Fatalf("Chatgpt-Account-Id = %q, want acct-non-siwc", got)
			}
			if got := gotHeaders.Get("Originator"); got == "" {
				t.Fatal("Originator should be set for non-SIWC Codex client request")
			}
			if got := gotHeaders.Get("X-Codex-Routing-Hint"); got != "model=gpt-5.4;tier=priority" {
				t.Fatalf("X-Codex-Routing-Hint = %q, want model=gpt-5.4;tier=priority", got)
			}
			if _, hasTools := gotBody["tools"]; !hasTools {
				t.Fatalf("non-SIWC Codex client request should inject image_generation tool, got tools=%#v", gotBody["tools"])
			}
			if _, hasInstructions := gotBody["instructions"]; !hasInstructions {
				t.Fatalf("non-SIWC Codex client request should normalize instructions, got %#v", gotBody["instructions"])
			}
			if gotBody["service_tier"] != "priority" {
				t.Fatalf("PayloadFinalizer override missing: service_tier=%v", gotBody["service_tier"])
			}

			// 1b. Non-Codex client on the same credential must still use SIWC token and SIWC body rules.
			_, errNonCodex := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: reqPayload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      http.Header{"User-Agent": []string{"curl/8.7.1"}},
			})
			if errNonCodex != nil {
				t.Fatalf("Non-Codex Execute error: %v", errNonCodex)
			}
			if got := gotHeaders.Get("Authorization"); got != "Bearer siwc-token-must-not-be-used" {
				t.Fatalf("Non-Codex Authorization = %q, want Bearer siwc-token-must-not-be-used", got)
			}
			if got := gotHeaders.Get("Chatgpt-Account-Id"); got != "" {
				t.Fatalf("Non-Codex Chatgpt-Account-Id = %q, want empty", got)
			}
			if _, hasTools := gotBody["tools"]; hasTools {
				t.Fatalf("Non-Codex SIWC request should not inject image_generation tool, got %#v", gotBody["tools"])
			}
			if _, hasInstructions := gotBody["instructions"]; hasInstructions {
				t.Fatalf("Non-Codex SIWC request should strip empty instructions, got %#v", gotBody["instructions"])
			}

			// 2. HTTP ExecuteStream with Codex client UA (even when SIWC token is missing!).
			authWithoutSIWCToken := auth.Clone()
			delete(authWithoutSIWCToken.Metadata, "siwc")
			sr, errStream := executor.ExecuteStream(context.Background(), authWithoutSIWCToken, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: reqPayload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      headers,
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream without siwc section for Codex client failed: %v", errStream)
			}
			for range sr.Chunks {
			}
			if got := gotHeaders.Get("Authorization"); got != "Bearer outer-non-siwc-token" {
				t.Fatalf("ExecuteStream Authorization = %q, want Bearer outer-non-siwc-token", got)
			}

			// 3. Compact with Codex client UA must use outer non-SIWC credentials and rules.
			_, errCompact := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","temperature":0.5,"input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Alt:          "responses/compact",
				Headers:      headers,
			})
			if errCompact != nil {
				t.Fatalf("Execute compact error: %v", errCompact)
			}
			if gotPath != "/responses/compact" {
				t.Fatalf("compact path = %q, want /responses/compact", gotPath)
			}
			if got := gotHeaders.Get("Authorization"); got != "Bearer outer-non-siwc-token" {
				t.Fatalf("compact Authorization = %q, want Bearer outer-non-siwc-token", got)
			}
			if got := gotHeaders.Get("Chatgpt-Account-Id"); got != "acct-non-siwc" {
				t.Fatalf("compact Chatgpt-Account-Id = %q, want acct-non-siwc", got)
			}
			if gotBody["temperature"] != 0.5 {
				t.Fatalf("compact temperature should be preserved for Codex client, got %v", gotBody["temperature"])
			}

			// 4. Default base URL resolution: Codex client uses https://chatgpt.com/backend-api/codex,
			// while non-Codex client uses https://api.openai.com/v1 on the exact same auth.
			authDefaultBase := auth.Clone()
			delete(authDefaultBase.Attributes, "base_url")
			var capturedURLs []string
			var capturedAuths []string
			rt := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				capturedURLs = append(capturedURLs, req.URL.String())
				capturedAuths = append(capturedAuths, req.Header.Get("Authorization"))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")),
					Request:    req,
				}, nil
			})
			ctxRT := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)
			if _, errCodex := executor.Execute(ctxRT, authDefaultBase, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","input":[]}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      headers,
			}); errCodex != nil {
				t.Fatalf("Execute (default URL Codex client) error: %v", errCodex)
			}
			if _, errNonCodex := executor.Execute(ctxRT, authDefaultBase, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","input":[]}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      http.Header{"User-Agent": []string{"curl/8.7.1"}},
			}); errNonCodex != nil {
				t.Fatalf("Execute (default URL non-Codex client) error: %v", errNonCodex)
			}
			if len(capturedURLs) != 2 {
				t.Fatalf("capturedURLs = %v, want 2 calls", capturedURLs)
			}
			if capturedURLs[0] != "https://chatgpt.com/backend-api/codex/responses" || capturedAuths[0] != "Bearer outer-non-siwc-token" {
				t.Fatalf("Codex client request = (%s, %s), want (https://chatgpt.com/backend-api/codex/responses, Bearer outer-non-siwc-token)", capturedURLs[0], capturedAuths[0])
			}
			if capturedURLs[1] != "https://api.openai.com/v1/responses" || capturedAuths[1] != "Bearer siwc-token-must-not-be-used" {
				t.Fatalf("non-Codex client request = (%s, %s), want (https://api.openai.com/v1/responses, Bearer siwc-token-must-not-be-used)", capturedURLs[1], capturedAuths[1])
			}

			// 5. WebSocket prepare & execute with Codex client UA must use wss://chatgpt.com/backend-api/codex/responses and outer token.
			wsExec := NewCodexWebsocketsExecutor(cfg)
			preparedWS, errPrepWS := wsExec.prepareCodexWebsocketStream(context.Background(), authDefaultBase, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: reqPayload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      headers,
			})
			if errPrepWS != nil {
				t.Fatalf("prepareCodexWebsocketStream error: %v", errPrepWS)
			}
			if preparedWS.wsURL != "wss://chatgpt.com/backend-api/codex/responses" {
				t.Fatalf("wsURL = %q, want wss://chatgpt.com/backend-api/codex/responses", preparedWS.wsURL)
			}
			if got := preparedWS.wsHeaders.Get("Authorization"); got != "Bearer outer-non-siwc-token" {
				t.Fatalf("wsHeaders Authorization = %q, want Bearer outer-non-siwc-token", got)
			}
			if got := headerValueCaseInsensitive(preparedWS.wsHeaders, "ChatGPT-Account-ID"); got != "acct-non-siwc" {
				t.Fatalf("wsHeaders ChatGPT-Account-ID = %q, want acct-non-siwc", got)
			}

			// 5b. WebSocket session reuse: a SIWC WebSocket connection opened by a non-Codex client
			// must NOT be reused by a subsequent Codex client request in the same execution session.
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			var wsHandshakes []string
			wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wsHandshakes = append(wsHandshakes, r.Header.Get("Authorization"))
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				for {
					_, _, errRead := conn.ReadMessage()
					if errRead != nil {
						return
					}
					completed := []byte(`{"type":"response.completed","response":{"id":"resp_ws","status":"completed","model":"gpt-5.4","output":[{"type":"message","id":"msg_ws","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
					if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
						return
					}
				}
			}))
			defer wsSrv.Close()

			authWSReuse := auth.Clone()
			authWSReuse.Attributes["base_url"] = wsSrv.URL
			sessionMeta := map[string]any{
				cliproxyexecutor.ExecutionSessionMetadataKey: "shared-ws-session-" + ua,
			}
			wsCtx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
			// First request: non-Codex client (curl) dials with SIWC token
			if _, errWS1 := wsExec.Execute(wsCtx, authWSReuse, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: reqPayload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      http.Header{"User-Agent": []string{"curl/8.7.1"}},
				Metadata:     sessionMeta,
			}); errWS1 != nil {
				t.Fatalf("wsExec.Execute (non-Codex) error: %v", errWS1)
			}
			// Second request: Codex client on the same session must detach SIWC conn and dial with outer token
			if _, errWS2 := wsExec.Execute(wsCtx, authWSReuse, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: reqPayload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      headers,
				Metadata:     sessionMeta,
			}); errWS2 != nil {
				t.Fatalf("wsExec.Execute (Codex client) error: %v", errWS2)
			}
			if len(wsHandshakes) != 2 || wsHandshakes[0] != "Bearer siwc-token-must-not-be-used" || wsHandshakes[1] != "Bearer outer-non-siwc-token" {
				t.Fatalf("wsHandshakes = %v, want [Bearer siwc-token-must-not-be-used, Bearer outer-non-siwc-token]", wsHandshakes)
			}

			// 6. Missing non-SIWC access_token on a use_siwc=true credential must fail closed (401) for Codex clients and never fall back to SIWC.
			authMissingOuter := authDefaultBase.Clone()
			delete(authMissingOuter.Metadata, "access_token")
			if _, errMissingOuter := executor.Execute(ctxRT, authMissingOuter, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","input":[]}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Headers:      headers,
			}); errMissingOuter == nil {
				t.Fatal("expected Codex client request to fail when outer non-SIWC access_token is missing")
			}
		})
	}
}
