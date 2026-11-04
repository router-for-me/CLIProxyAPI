package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type targetedTestAntigravityOAuthService struct {
	email       string
	accessToken string
}

func (s *targetedTestAntigravityOAuthService) BuildAuthURL(state, _ string) string {
	return "https://accounts.google.com/o/oauth2/v2/auth?state=" + state
}

func (s *targetedTestAntigravityOAuthService) ExchangeCodeForTokens(_ context.Context, code, _ string) (*antigravity.TokenResponse, error) {
	tok := s.accessToken
	if tok == "" {
		tok = "ag-fresh-access-" + code
	}
	return &antigravity.TokenResponse{
		AccessToken:  tok,
		RefreshToken: "ag-fresh-refresh-" + code,
		ExpiresIn:    3600,
		TokenType:    "Bearer",
	}, nil
}

func (s *targetedTestAntigravityOAuthService) FetchUserInfo(_ context.Context, _ string) (string, error) {
	return s.email, nil
}

func (s *targetedTestAntigravityOAuthService) FetchProjectID(_ context.Context, _ string) (string, error) {
	return "ag-project-1", nil
}

type targetedTestKimiOAuthService struct {
	domain       string
	accessToken  string
	refreshToken string
	deviceID     string
}

func (s *targetedTestKimiOAuthService) StartDeviceFlow(_ context.Context) (*kimi.DeviceCodeResponse, error) {
	return &kimi.DeviceCodeResponse{
		DeviceCode:              "kimi-dev-code",
		UserCode:                "KIMI-1234",
		VerificationURI:         "https://auth.kimi.com/verify",
		VerificationURIComplete: "https://auth.kimi.com/verify?user_code=KIMI-1234",
		ExpiresIn:               600,
		Interval:                1,
	}, nil
}

func (s *targetedTestKimiOAuthService) WaitForAuthorization(_ context.Context, _ *kimi.DeviceCodeResponse) (*kimi.KimiAuthBundle, error) {
	return &kimi.KimiAuthBundle{
		TokenData: &kimi.KimiTokenData{
			AccessToken:  s.accessToken,
			RefreshToken: s.refreshToken,
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(time.Hour).Unix(),
			Scope:        "coding",
		},
		DeviceID: s.deviceID,
	}, nil
}

func (s *targetedTestKimiOAuthService) CreateTokenStorage(bundle *kimi.KimiAuthBundle) *kimi.KimiTokenStorage {
	return kimi.NewKimiAuthWithDomain(nil, s.domain).CreateTokenStorage(bundle)
}

type targetedTestClaudeOAuthService struct {
	authDir     string
	email       string
	accountUUID string
	orgUUID     string
	accessToken string
}

func (s *targetedTestClaudeOAuthService) GenerateAuthURL(state string, _ *claude.PKCECodes) (string, string, error) {
	if s.authDir != "" {
		waitFile := filepath.Join(s.authDir, ".oauth-anthropic-"+state+".oauth")
		payload, _ := json.Marshal(map[string]string{"state": state, "code": "claude-code-" + state})
		_ = os.WriteFile(waitFile, payload, 0o600)
	}
	return "https://claude.ai/oauth/authorize?state=" + state, state, nil
}

func (s *targetedTestClaudeOAuthService) ExchangeCodeForTokens(_ context.Context, code, _ string, _ *claude.PKCECodes) (*claude.ClaudeAuthBundle, error) {
	tok := s.accessToken
	if tok == "" {
		tok = "claude-access-" + code
	}
	return &claude.ClaudeAuthBundle{
		TokenData: claude.ClaudeTokenData{
			AccessToken:      tok,
			RefreshToken:     "claude-refresh-" + code,
			Email:            s.email,
			AccountUUID:      s.accountUUID,
			OrganizationUUID: s.orgUUID,
			Expire:           time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
		LastRefresh: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *targetedTestClaudeOAuthService) CreateTokenStorage(bundle *claude.ClaudeAuthBundle) *claude.ClaudeTokenStorage {
	return &claude.ClaudeTokenStorage{
		AccessToken:      bundle.TokenData.AccessToken,
		RefreshToken:     bundle.TokenData.RefreshToken,
		Email:            bundle.TokenData.Email,
		AccountUUID:      bundle.TokenData.AccountUUID,
		OrganizationUUID: bundle.TokenData.OrganizationUUID,
		Expire:           bundle.TokenData.Expire,
		LastRefresh:      bundle.LastRefresh,
		Type:             "claude",
	}
}

type targetedTestXAIOAuthService struct {
	email       string
	subject     string
	accessToken string
}

func (s *targetedTestXAIOAuthService) StartDeviceFlow(_ context.Context) (*xaiauth.DeviceCodeResponse, error) {
	return &xaiauth.DeviceCodeResponse{
		DeviceCode:              "xai-dev-code",
		UserCode:                "XAI-1234",
		VerificationURI:         "https://auth.x.ai/device",
		VerificationURIComplete: "https://auth.x.ai/device?user_code=XAI-1234",
		ExpiresIn:               600,
		Interval:                1,
	}, nil
}

func (s *targetedTestXAIOAuthService) WaitForAuthorization(_ context.Context, _ *xaiauth.DeviceCodeResponse) (*xaiauth.AuthBundle, error) {
	return &xaiauth.AuthBundle{
		TokenData: xaiauth.TokenData{
			AccessToken:  s.accessToken,
			RefreshToken: "xai-refresh-fresh",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			Expire:       time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			Email:        s.email,
			Subject:      s.subject,
		},
		LastRefresh: time.Now().UTC().Format(time.RFC3339),
		BaseURL:     xaiauth.DefaultAPIBaseURL,
	}, nil
}

func (s *targetedTestXAIOAuthService) CreateTokenStorage(bundle *xaiauth.AuthBundle) *xaiauth.TokenStorage {
	return &xaiauth.TokenStorage{
		Type:         "xai",
		AuthKind:     "oauth",
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		TokenType:    bundle.TokenData.TokenType,
		ExpiresIn:    bundle.TokenData.ExpiresIn,
		Expire:       bundle.TokenData.Expire,
		LastRefresh:  bundle.LastRefresh,
		BaseURL:      bundle.BaseURL,
		Email:        bundle.TokenData.Email,
		Subject:      bundle.TokenData.Subject,
	}
}

type targetedTestMetaOAuthService struct {
	email       string
	accessToken string
}

func (s *targetedTestMetaOAuthService) StartDeviceFlow(_ context.Context) (*metaauth.DeviceCodeResponse, error) {
	return &metaauth.DeviceCodeResponse{
		DeviceCode:              "meta-dev-code",
		UserCode:                "META-1234",
		VerificationURI:         "https://auth.meta.com/device",
		VerificationURIComplete: "https://auth.meta.com/device?user_code=META-1234",
		ExpiresIn:               600,
		Interval:                1,
	}, nil
}

func (s *targetedTestMetaOAuthService) WaitForAuthorization(_ context.Context, _ *metaauth.DeviceCodeResponse) (*metaauth.MetaAuthBundle, error) {
	return &metaauth.MetaAuthBundle{
		TokenData: &metaauth.TokenData{
			AccessToken: s.accessToken,
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		},
		MintedKey: &metaauth.MintedKeyResponse{
			APIKey:    "meta-api-key-" + s.accessToken,
			BaseURL:   metaauth.DefaultAPIBaseURL,
			UserEmail: s.email,
		},
		Email: s.email,
	}, nil
}

func (s *targetedTestMetaOAuthService) CreateTokenStorage(bundle *metaauth.MetaAuthBundle) *metaauth.MetaTokenStorage {
	return &metaauth.MetaTokenStorage{
		Type:        "meta",
		AuthKind:    "oauth",
		AccessToken: bundle.TokenData.AccessToken,
		DCAToken:    bundle.TokenData.AccessToken,
		APIKey:      bundle.MintedKey.APIKey,
		TokenType:   bundle.TokenData.TokenType,
		ExpiresIn:   bundle.TokenData.ExpiresIn,
		Expired:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		BaseURL:     metaauth.DefaultAPIBaseURL,
		Email:       bundle.Email,
	}
}

func TestStartOAuthV8TargetedSessionRegistrationAcrossBuiltinProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.URL.Path, "openid-configuration"):
			_, _ = rec.WriteString(`{"device_authorization_endpoint":"https://auth.x.ai/device","token_endpoint":"https://auth.x.ai/token"}`)
		case strings.Contains(req.URL.Path, "device"):
			_, _ = rec.WriteString(`{"device_code":"dev-code","user_code":"ABCD-1234","verification_uri":"https://example.test/verify","verification_uri_complete":"https://example.test/verify?code=ABCD-1234","expires_in":600,"interval":5}`)
		default:
			_, _ = rec.WriteString(`{"error":"authorization_pending"}`)
		}
		return rec.Result(), nil
	})
	t.Cleanup(func() { http.DefaultTransport = origTransport })

	origCodexFactory := newCodexOAuthService
	newCodexOAuthService = func(*config.Config) codexOAuthService {
		return &targetedRecoveryTestCodexOAuthService{authDir: t.TempDir(), email: "a@x.test", accountID: "acc-1"}
	}
	t.Cleanup(func() { newCodexOAuthService = origCodexFactory })

	origAgFactory := newAntigravityOAuthService
	newAntigravityOAuthService = func(*config.Config) antigravityOAuthService {
		return &targetedTestAntigravityOAuthService{email: "a@x.test"}
	}
	t.Cleanup(func() { newAntigravityOAuthService = origAgFactory })

	origDevinFactory := newDevinOAuthService
	newDevinOAuthService = func(*config.Config) devinOAuthService {
		return &fakeDevinOAuthService{}
	}
	t.Cleanup(func() { newDevinOAuthService = origDevinFactory })

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)

	targets := []struct {
		provider string
		fileName string
		meta     map[string]any
	}{
		{provider: "codex", fileName: "codex-target.json", meta: map[string]any{"type": "codex", "email": "a@x.test", "account_id": "acc-1"}},
		{provider: "antigravity", fileName: "antigravity-target.json", meta: map[string]any{"type": "antigravity", "email": "a@x.test"}},
		{provider: "kimi", fileName: "kimi-target.json", meta: map[string]any{"type": "kimi", "domain": "kimi.com"}},
		{provider: "kimi-ai", fileName: "kimi-ai-target.json", meta: map[string]any{"type": "kimi-ai", "domain": "kimi.ai"}},
		{provider: "claude", fileName: "claude-target.json", meta: map[string]any{"type": "claude", "email": "a@x.test", "account_uuid": "uuid-1"}},
		{provider: "xai", fileName: "xai-target.json", meta: map[string]any{"type": "xai", "email": "a@x.test", "sub": "sub-1"}},
		{provider: "meta", fileName: "meta-target.json", meta: map[string]any{"type": "meta", "email": "a@x.test"}},
		{provider: "devin", fileName: "devin-target.json", meta: map[string]any{"type": "devin", "email": "a@x.test", "user_id": "usr-1"}},
	}

	for _, tc := range targets {
		raw, _ := json.Marshal(tc.meta)
		fullPath := filepath.Join(authDir, tc.fileName)
		if errWrite := os.WriteFile(fullPath, raw, 0o600); errWrite != nil {
			t.Fatalf("write %s: %v", tc.fileName, errWrite)
		}
		_, _ = manager.Register(context.Background(), &coreauth.Auth{
			ID:         tc.fileName,
			FileName:   tc.fileName,
			Provider:   tc.provider,
			Metadata:   tc.meta,
			Attributes: map[string]string{coreauth.AttributePath: fullPath},
		})
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, manager)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)

	for _, tc := range targets {
		t.Run(tc.provider, func(t *testing.T) {
			reqBody, _ := json.Marshal(map[string]any{
				"provider": tc.provider,
				"name":     tc.fileName,
				"verify":   true,
			})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(reqBody)))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: StartOAuthV8 status = %d, want 200: %s", tc.provider, rec.Code, rec.Body.String())
			}
			var startResp map[string]any
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &startResp); errUnmarshal != nil {
				t.Fatalf("%s: unmarshal response: %v", tc.provider, errUnmarshal)
			}
			state, _ := startResp["state"].(string)
			if state == "" {
				t.Fatalf("%s: expected non-empty state in response: %v", tc.provider, startResp)
			}
			t.Cleanup(func() { CancelOAuthSession(state) })

			if gotTargetID, _ := startResp["target_auth_id"].(string); gotTargetID != tc.fileName {
				t.Fatalf("%s: start response target_auth_id = %q, want %q (full response: %v)", tc.provider, gotTargetID, tc.fileName, startResp)
			}
			sessionTarget, okTarget := GetOAuthSessionTarget(state)
			if !okTarget || sessionTarget.AuthID != tc.fileName || !sessionTarget.VerifyAfterLogin {
				t.Fatalf("%s: GetOAuthSessionTarget(%q) = (%+v, %v), want AuthID=%q VerifyAfterLogin=true", tc.provider, state, sessionTarget, okTarget, tc.fileName)
			}
		})
	}
}

func TestTargetedOAuthReloginAntigravityRejectsMismatchedAccountAndUpdatesMatchingTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	targetFile := "antigravity-a@x.test.json"
	targetPath := filepath.Join(authDir, targetFile)
	initialContent := `{"type":"antigravity","email":"a@x.test","access_token":"old-access","refresh_token":"old-refresh"}`
	if errWrite := os.WriteFile(targetPath, []byte(initialContent), 0o600); errWrite != nil {
		t.Fatalf("write target file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:             targetFile,
		FileName:       targetFile,
		Provider:       "antigravity",
		RequiresSignIn: true,
		Metadata: map[string]any{
			"type":          "antigravity",
			"email":         "a@x.test",
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
		},
		Attributes: map[string]string{
			coreauth.AttributePath: targetPath,
			"email":                "a@x.test",
		},
	})

	agSvc := &targetedTestAntigravityOAuthService{email: "b@x.test"}
	origAgFactory := newAntigravityOAuthService
	newAntigravityOAuthService = func(*config.Config) antigravityOAuthService {
		return agSvc
	}
	t.Cleanup(func() { newAntigravityOAuthService = origAgFactory })

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, manager)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v8/management/oauth/status", h.GetAuthStatus)

	// 1. Mismatched account (b@x.test) must be rejected and must NOT write antigravity-b@x.test.json.
	wMismatch := httptest.NewRecorder()
	reqMismatch := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"antigravity","name":"antigravity-a@x.test.json"}`))
	reqMismatch.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wMismatch, reqMismatch)
	if wMismatch.Code != http.StatusOK {
		t.Fatalf("StartOAuthV8 status = %d: %s", wMismatch.Code, wMismatch.Body.String())
	}
	var mismatchStart struct {
		State        string `json:"state"`
		TargetAuthID string `json:"target_auth_id"`
	}
	_ = json.Unmarshal(wMismatch.Body.Bytes(), &mismatchStart)
	t.Cleanup(func() { CancelOAuthSession(mismatchStart.State) })
	if mismatchStart.TargetAuthID != targetFile {
		t.Fatalf("target_auth_id = %q, want %q", mismatchStart.TargetAuthID, targetFile)
	}
	if _, errCb := WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", mismatchStart.State, "code-mismatch", ""); errCb != nil {
		t.Fatalf("write callback: %v", errCb)
	}
	mismatchStatus := pollOAuthStatusUntilDone(t, router, mismatchStart.State)
	if mismatchStatus["status"] != "error" || !strings.Contains(strings.ToLower(mismatchStatus["error"].(string)), "does not match target credential") {
		t.Fatalf("expected account mismatch error, got %#v", mismatchStatus)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "antigravity-b@x.test.json")); !os.IsNotExist(errStat) {
		t.Fatalf("mismatched login must not create antigravity-b@x.test.json, stat err = %v", errStat)
	}
	afterMismatchBytes, _ := os.ReadFile(targetPath)
	if string(afterMismatchBytes) != initialContent {
		t.Fatalf("target file changed after mismatched login: %s", string(afterMismatchBytes))
	}

	// 2. Matching account (a@x.test) updates target file in place and reports adopted status.
	agSvc.email = "a@x.test"
	agSvc.accessToken = "ag-fresh-token-match"
	wMatch := httptest.NewRecorder()
	reqMatch := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"antigravity","name":"antigravity-a@x.test.json"}`))
	reqMatch.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wMatch, reqMatch)
	if wMatch.Code != http.StatusOK {
		t.Fatalf("StartOAuthV8 (match) status = %d: %s", wMatch.Code, wMatch.Body.String())
	}
	var matchStart struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(wMatch.Body.Bytes(), &matchStart)
	t.Cleanup(func() { CancelOAuthSession(matchStart.State) })
	if _, errCb := WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", matchStart.State, "code-match", ""); errCb != nil {
		t.Fatalf("write match callback: %v", errCb)
	}
	matchStatus := pollOAuthStatusUntilDone(t, router, matchStart.State)
	if matchStatus["status"] != "ok" || matchStatus["auth_id"] != targetFile || matchStatus["saved"] != true || matchStatus["adopted"] != true {
		t.Fatalf("expected saved & adopted ok status for %s, got %#v", targetFile, matchStatus)
	}
	repaired, okRepaired := manager.GetByID(targetFile)
	if !okRepaired || repaired == nil || repaired.RequiresSignIn {
		t.Fatalf("expected %s repaired in manager, got %+v", targetFile, repaired)
	}
	savedRaw, errRead := os.ReadFile(targetPath)
	if errRead != nil || !strings.Contains(string(savedRaw), "ag-fresh-token-match") {
		t.Fatalf("expected target file updated with fresh token, got %q (err=%v)", string(savedRaw), errRead)
	}
}

func TestTargetedOAuthReloginKimiUpdatesTargetFileWithoutAddingNewFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	targetFile := "kimi-1700000000000.json"
	targetPath := filepath.Join(authDir, targetFile)
	initialContent := `{"type":"kimi","domain":"kimi.com","access_token":"old-kimi-access","refresh_token":"old-kimi-refresh","device_id":"dev-old"}`
	if errWrite := os.WriteFile(targetPath, []byte(initialContent), 0o600); errWrite != nil {
		t.Fatalf("write kimi target file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	_, _ = manager.Register(context.Background(), &coreauth.Auth{
		ID:             targetFile,
		FileName:       targetFile,
		Provider:       "kimi",
		RequiresSignIn: true,
		Metadata: map[string]any{
			"type":          "kimi",
			"domain":        "kimi.com",
			"access_token":  "old-kimi-access",
			"refresh_token": "old-kimi-refresh",
		},
		Attributes: map[string]string{
			coreauth.AttributePath: targetPath,
			"domain":               "kimi.com",
		},
	})

	kimiSvc := &targetedTestKimiOAuthService{
		domain:       kimi.KimiDefaultDomain,
		accessToken:  "kimi-access-1",
		refreshToken: "kimi-refresh-1",
		deviceID:     "dev-1",
	}
	origKimiFactory := newKimiOAuthService
	newKimiOAuthService = func(_ *config.Config, domain string) kimiOAuthService {
		kimiSvc.domain = domain
		return kimiSvc
	}
	t.Cleanup(func() { newKimiOAuthService = origKimiFactory })

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, manager)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v8/management/oauth/status", h.GetAuthStatus)

	// Domain mismatch ("kimi" with domain "kimi.ai" targeting a kimi.com credential) must return 400.
	wDomainMismatch := httptest.NewRecorder()
	reqDomainMismatch := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"kimi","domain":"kimi.ai","name":"kimi-1700000000000.json"}`))
	reqDomainMismatch.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wDomainMismatch, reqDomainMismatch)
	if wDomainMismatch.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for kimi.ai domain against kimi.com target, got %d: %s", wDomainMismatch.Code, wDomainMismatch.Body.String())
	}

	// Two targeted logins in a row must both update kimi-1700000000000.json in place and keep file count at 1.
	for i, tok := range []string{"kimi-access-relogin-1", "kimi-access-relogin-2"} {
		kimiSvc.accessToken = tok
		kimiSvc.refreshToken = "kimi-refresh-" + tok
		wStart := httptest.NewRecorder()
		reqStart := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"kimi","name":"kimi-1700000000000.json"}`))
		reqStart.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(wStart, reqStart)
		if wStart.Code != http.StatusOK {
			t.Fatalf("run %d: StartOAuthV8 status = %d: %s", i+1, wStart.Code, wStart.Body.String())
		}
		var startResp struct {
			State        string `json:"state"`
			TargetAuthID string `json:"target_auth_id"`
		}
		if errUnmarshal := json.Unmarshal(wStart.Body.Bytes(), &startResp); errUnmarshal != nil {
			t.Fatalf("run %d: unmarshal startResp: %v", i+1, errUnmarshal)
		}
		t.Cleanup(func() { CancelOAuthSession(startResp.State) })
		if startResp.TargetAuthID != targetFile {
			t.Fatalf("run %d: target_auth_id = %q, want %q", i+1, startResp.TargetAuthID, targetFile)
		}

		status := pollOAuthStatusUntilDone(t, router, startResp.State)
		if status["status"] != "ok" || status["auth_id"] != targetFile || status["saved"] != true || status["adopted"] != true {
			t.Fatalf("run %d: unexpected status: %#v", i+1, status)
		}

		entries, errDir := os.ReadDir(authDir)
		if errDir != nil {
			t.Fatalf("run %d: ReadDir: %v", i+1, errDir)
		}
		var jsonFiles []string
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				jsonFiles = append(jsonFiles, entry.Name())
			}
		}
		if len(jsonFiles) != 1 || jsonFiles[0] != targetFile {
			t.Fatalf("run %d: expected only [%s] in authDir, got %v", i+1, targetFile, jsonFiles)
		}

		rawSaved, errRead := os.ReadFile(targetPath)
		if errRead != nil || !strings.Contains(string(rawSaved), tok) {
			t.Fatalf("run %d: expected target file to contain %q, got %s (err=%v)", i+1, tok, string(rawSaved), errRead)
		}
	}
}

func TestTargetedOAuthReloginClaudeXAIMetaDevinAndAuthIndexVerify(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)

	claudeSvc := &targetedTestClaudeOAuthService{authDir: authDir, email: "a@x.test", accountUUID: "uuid-a", orgUUID: "org-a"}
	origClaude := newClaudeOAuthService
	newClaudeOAuthService = func(*config.Config, string) claudeOAuthService { return claudeSvc }
	t.Cleanup(func() { newClaudeOAuthService = origClaude })

	xaiSvc := &targetedTestXAIOAuthService{email: "a@x.test", subject: "sub-a", accessToken: "xai-fresh-1"}
	origXAI := newXAIOAuthService
	newXAIOAuthService = func(*config.Config, string) xaiOAuthService { return xaiSvc }
	t.Cleanup(func() { newXAIOAuthService = origXAI })

	metaSvc := &targetedTestMetaOAuthService{email: "a@x.test", accessToken: "meta-fresh-1"}
	origMeta := newMetaOAuthService
	newMetaOAuthService = func(*config.Config, string) metaOAuthService { return metaSvc }
	t.Cleanup(func() { newMetaOAuthService = origMeta })

	devinEmail := "a@x.test"
	devinUserID := "usr-a"
	devinToken := "devin-fresh-1"
	origDevin := newDevinOAuthService
	newDevinOAuthService = func(*config.Config) devinOAuthService {
		return &fakeDevinOAuthService{
			exchange: func(context.Context, string, string) (string, error) { return devinToken, nil },
			create: func(_ context.Context, token string) (*coreauth.Auth, error) {
				return &coreauth.Auth{
					ID:       "devin-generated.json",
					FileName: "devin-generated.json",
					Provider: "devin",
					Metadata: map[string]any{
						"type":          "devin",
						"api_key":       devin.FormatSessionToken(token),
						"session_token": devin.FormatSessionToken(token),
						"email":         devinEmail,
						"user_id":       devinUserID,
						"auth_kind":     "oauth",
					},
					Attributes: map[string]string{
						"api_key":       devin.FormatSessionToken(token),
						"session_token": devin.FormatSessionToken(token),
						"email":         devinEmail,
						"user_id":       devinUserID,
						"auth_kind":     "oauth",
					},
				}, nil
			},
		}
	}
	t.Cleanup(func() { newDevinOAuthService = origDevin })

	setupTarget := func(provider, fileName string, meta map[string]any) *coreauth.Auth {
		raw, _ := json.Marshal(meta)
		path := filepath.Join(authDir, fileName)
		if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
			t.Fatalf("write %s: %v", fileName, errWrite)
		}
		auth := &coreauth.Auth{
			ID:             fileName,
			FileName:       fileName,
			Provider:       provider,
			RequiresSignIn: true,
			Metadata:       meta,
			Attributes:     map[string]string{coreauth.AttributePath: path},
		}
		auth.EnsureIndex()
		registered, errReg := manager.Register(context.Background(), auth)
		if errReg != nil {
			t.Fatalf("register %s: %v", fileName, errReg)
		}
		return registered
	}

	claudeAuth := setupTarget("claude", "claude-custom.json", map[string]any{"type": "claude", "email": "a@x.test", "account_uuid": "uuid-a", "access_token": "old", "refresh_token": "old"})
	_ = setupTarget("xai", "xai-custom.json", map[string]any{"type": "xai", "email": "a@x.test", "sub": "sub-a", "access_token": "old", "refresh_token": "old"})
	_ = setupTarget("meta", "meta-custom.json", map[string]any{"type": "meta", "email": "a@x.test", "access_token": "old"})
	_ = setupTarget("devin", "devin-custom.json", map[string]any{"type": "devin", "email": "a@x.test", "user_id": "usr-a", "api_key": "devin-session-token$old"})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, manager)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.POST("/v8/management/oauth/callback", h.PostOAuthCallbackV8)
	router.GET("/v8/management/oauth/status", h.GetAuthStatus)

	// 1. Claude targeted relogin via auth_index (omitting name & provider) rejects mismatched account_uuid, then succeeds on match.
	claudeSvc.accountUUID = "uuid-wrong"
	wClaudeMismatch := httptest.NewRecorder()
	reqClaudeMismatch := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"auth_index":"`+claudeAuth.Index+`"}`))
	reqClaudeMismatch.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wClaudeMismatch, reqClaudeMismatch)
	if wClaudeMismatch.Code != http.StatusOK {
		t.Fatalf("claude StartOAuthV8 by auth_index status = %d: %s", wClaudeMismatch.Code, wClaudeMismatch.Body.String())
	}
	var claudeMismatchStart struct {
		State        string `json:"state"`
		TargetAuthID string `json:"target_auth_id"`
	}
	_ = json.Unmarshal(wClaudeMismatch.Body.Bytes(), &claudeMismatchStart)
	if claudeMismatchStart.TargetAuthID != "claude-custom.json" {
		t.Fatalf("claude target_auth_id = %q, want claude-custom.json", claudeMismatchStart.TargetAuthID)
	}
	if st := pollOAuthStatusUntilDone(t, router, claudeMismatchStart.State); st["status"] != "error" {
		t.Fatalf("claude mismatch status = %#v, want error", st)
	}

	claudeSvc.accountUUID = "uuid-a"
	claudeSvc.accessToken = "claude-fresh-ok"
	wClaudeMatch := httptest.NewRecorder()
	reqClaudeMatch := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"auth_index":"`+claudeAuth.Index+`"}`))
	reqClaudeMatch.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wClaudeMatch, reqClaudeMatch)
	var claudeMatchStart struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(wClaudeMatch.Body.Bytes(), &claudeMatchStart)
	if st := pollOAuthStatusUntilDone(t, router, claudeMatchStart.State); st["status"] != "ok" || st["auth_id"] != "claude-custom.json" || st["adopted"] != true {
		t.Fatalf("claude match status = %#v, want ok & adopted", st)
	}

	// 2. xAI mismatch rejected, match updates xai-custom.json in place.
	xaiSvc.subject = "sub-wrong"
	wXAIMismatch := httptest.NewRecorder()
	router.ServeHTTP(wXAIMismatch, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"xai","name":"xai-custom.json"}`)))
	var xaiMismatchStart struct{ State string }
	_ = json.Unmarshal(wXAIMismatch.Body.Bytes(), &xaiMismatchStart)
	if st := pollOAuthStatusUntilDone(t, router, xaiMismatchStart.State); st["status"] != "error" {
		t.Fatalf("xai mismatch status = %#v, want error", st)
	}

	xaiSvc.subject = "sub-a"
	wXAIMatch := httptest.NewRecorder()
	router.ServeHTTP(wXAIMatch, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"xai","name":"xai-custom.json"}`)))
	var xaiMatchStart struct{ State string }
	_ = json.Unmarshal(wXAIMatch.Body.Bytes(), &xaiMatchStart)
	if st := pollOAuthStatusUntilDone(t, router, xaiMatchStart.State); st["status"] != "ok" || st["auth_id"] != "xai-custom.json" || st["adopted"] != true {
		t.Fatalf("xai match status = %#v, want ok & adopted", st)
	}

	// 3. Meta mismatch rejected, match updates meta-custom.json in place.
	metaSvc.email = "wrong@x.test"
	wMetaMismatch := httptest.NewRecorder()
	router.ServeHTTP(wMetaMismatch, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"meta","name":"meta-custom.json"}`)))
	var metaMismatchStart struct{ State string }
	_ = json.Unmarshal(wMetaMismatch.Body.Bytes(), &metaMismatchStart)
	if st := pollOAuthStatusUntilDone(t, router, metaMismatchStart.State); st["status"] != "error" {
		t.Fatalf("meta mismatch status = %#v, want error", st)
	}

	metaSvc.email = "a@x.test"
	wMetaMatch := httptest.NewRecorder()
	router.ServeHTTP(wMetaMatch, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"meta","name":"meta-custom.json"}`)))
	var metaMatchStart struct{ State string }
	_ = json.Unmarshal(wMetaMatch.Body.Bytes(), &metaMatchStart)
	if st := pollOAuthStatusUntilDone(t, router, metaMatchStart.State); st["status"] != "ok" || st["auth_id"] != "meta-custom.json" || st["adopted"] != true {
		t.Fatalf("meta match status = %#v, want ok & adopted", st)
	}

	// 4. Devin mismatch rejected, match updates devin-custom.json in place.
	devinUserID = "usr-wrong"
	wDevinMismatch := httptest.NewRecorder()
	router.ServeHTTP(wDevinMismatch, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"devin","name":"devin-custom.json"}`)))
	var devinMismatchStart struct{ State string }
	_ = json.Unmarshal(wDevinMismatch.Body.Bytes(), &devinMismatchStart)
	_, _ = WriteOAuthCallbackFileForPendingSession(authDir, "devin", devinMismatchStart.State, "devin-code-1", "")
	if st := pollOAuthStatusUntilDone(t, router, devinMismatchStart.State); st["status"] != "error" {
		t.Fatalf("devin mismatch status = %#v, want error", st)
	}

	devinUserID = "usr-a"
	wDevinMatch := httptest.NewRecorder()
	router.ServeHTTP(wDevinMatch, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"devin","name":"devin-custom.json"}`)))
	var devinMatchStart struct{ State string }
	_ = json.Unmarshal(wDevinMatch.Body.Bytes(), &devinMatchStart)
	_, _ = WriteOAuthCallbackFileForPendingSession(authDir, "devin", devinMatchStart.State, "devin-code-2", "")
	if st := pollOAuthStatusUntilDone(t, router, devinMatchStart.State); st["status"] != "ok" || st["auth_id"] != "devin-custom.json" || st["adopted"] != true {
		t.Fatalf("devin match status = %#v, want ok & adopted", st)
	}
}

func TestTargetedOAuthReloginReviewEdgeCasesAndVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)

	// 1. Kimi domain isolation when provider is omitted or auth_index is used.
	kimiComAuth := &coreauth.Auth{
		ID:             "kimi-com-target.json",
		FileName:       "kimi-com-target.json",
		Provider:       "kimi",
		RequiresSignIn: true,
		Attributes:     map[string]string{"domain": "kimi.com"},
		Metadata:       map[string]any{"type": "kimi", "domain": "kimi.com", "access_token": "old-com", "refresh_token": "old-com"},
	}
	kimiComAuth.EnsureIndex()
	registeredKimiCom, errRegKimiCom := manager.Register(context.Background(), kimiComAuth)
	if errRegKimiCom != nil {
		t.Fatalf("register kimi-com: %v", errRegKimiCom)
	}
	kimiAIAuth := &coreauth.Auth{
		ID:             "kimi-ai-target.json",
		FileName:       "kimi-ai-target.json",
		Provider:       "kimi-ai",
		RequiresSignIn: true,
		Attributes:     map[string]string{"domain": "kimi.ai"},
		Metadata:       map[string]any{"type": "kimi-ai", "domain": "kimi.ai", "access_token": "old-ai", "refresh_token": "old-ai"},
	}
	kimiAIAuth.EnsureIndex()
	if _, errRegKimiAI := manager.Register(context.Background(), kimiAIAuth); errRegKimiAI != nil {
		t.Fatalf("register kimi-ai: %v", errRegKimiAI)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, manager)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v8/management/oauth/status", h.GetAuthStatus)

	for _, body := range []string{
		`{"name":"kimi-com-target.json","domain":"kimi.ai"}`,
		`{"auth_index":"` + registeredKimiCom.Index + `","domain":"kimi.ai"}`,
		`{"name":"kimi-ai-target.json","domain":"kimi.com"}`,
		`{"provider":"kimi-ai","name":"kimi-ai-target.json","domain":"kimi.com"}`,
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want 400: %s", body, w.Code, w.Body.String())
		}
	}

	// Commit-time guard also rejects cross-domain Kimi overwrite.
	crossDomainState := "kimi-cross-domain-commit"
	RegisterOAuthSession(crossDomainState, "kimi")
	SetOAuthSessionTarget(crossDomainState, oauthRecoveryTarget{
		AuthID:   "kimi-com-target.json",
		FileName: "kimi-com-target.json",
		Provider: "kimi",
	})
	t.Cleanup(func() { CompleteOAuthSession(crossDomainState) })
	if _, ok := h.commitAndCompleteBuiltinOAuthSession(context.Background(), crossDomainState, "kimi-ai", "", &coreauth.Auth{
		ID:       "kimi-ai-fresh.json",
		FileName: "kimi-ai-fresh.json",
		Provider: "kimi-ai",
		Metadata: map[string]any{"type": "kimi-ai", "access_token": "ai-tok"},
	}, "save failed", false); ok {
		t.Fatalf("expected commitAndCompleteBuiltinOAuthSession to reject cross-domain kimi-ai record for kimi target")
	}

	// 2. Claude organization_uuid isolation: same email + account_uuid in a different organization_uuid must be rejected.
	claudeOrgSvc := &targetedTestClaudeOAuthService{
		authDir:     authDir,
		email:       "a@x.test",
		accountUUID: "uuid-a",
		orgUUID:     "org-b",
		accessToken: "claude-org-b-token",
	}
	origClaude := newClaudeOAuthService
	newClaudeOAuthService = func(*config.Config, string) claudeOAuthService { return claudeOrgSvc }
	t.Cleanup(func() { newClaudeOAuthService = origClaude })

	claudeOrgPath := filepath.Join(authDir, "claude-org-a.json")
	claudeOrgMeta := map[string]any{
		"type":              "claude",
		"email":             "a@x.test",
		"account_uuid":      "uuid-a",
		"organization_uuid": "org-a",
		"access_token":      "old-org-a-token",
		"refresh_token":     "old-org-a-refresh",
	}
	rawClaudeOrg, _ := json.Marshal(claudeOrgMeta)
	if errWrite := os.WriteFile(claudeOrgPath, rawClaudeOrg, 0o600); errWrite != nil {
		t.Fatalf("write claude-org-a.json: %v", errWrite)
	}
	if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
		ID:             "claude-org-a.json",
		FileName:       "claude-org-a.json",
		Provider:       "claude",
		RequiresSignIn: true,
		Metadata:       claudeOrgMeta,
		Attributes:     map[string]string{coreauth.AttributePath: claudeOrgPath},
	}); errReg != nil {
		t.Fatalf("register claude-org-a.json: %v", errReg)
	}

	wOrgMismatch := httptest.NewRecorder()
	router.ServeHTTP(wOrgMismatch, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"claude","name":"claude-org-a.json"}`)))
	var orgMismatchStart struct{ State string }
	_ = json.Unmarshal(wOrgMismatch.Body.Bytes(), &orgMismatchStart)
	if st := pollOAuthStatusUntilDone(t, router, orgMismatchStart.State); st["status"] != "error" || st["error"] != "Authenticated account does not match target credential" {
		t.Fatalf("claude org mismatch status = %#v, want error", st)
	}

	claudeOrgSvc.orgUUID = "org-a"
	claudeOrgSvc.accessToken = "claude-org-a-fresh"
	wOrgMatch := httptest.NewRecorder()
	router.ServeHTTP(wOrgMatch, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"claude","name":"claude-org-a.json"}`)))
	var orgMatchStart struct{ State string }
	_ = json.Unmarshal(wOrgMatch.Body.Bytes(), &orgMatchStart)
	if st := pollOAuthStatusUntilDone(t, router, orgMatchStart.State); st["status"] != "ok" || st["auth_id"] != "claude-org-a.json" || st["adopted"] != true {
		t.Fatalf("claude org match status = %#v, want ok & adopted", st)
	}

	// 3. xAI optional identity fields: subject-only or email-only re-login succeeds and preserves the other field; conflicting field fails.
	xaiSvc := &targetedTestXAIOAuthService{
		email:       "",
		subject:     "sub-a",
		accessToken: "xai-sub-only-fresh",
	}
	origXAI := newXAIOAuthService
	newXAIOAuthService = func(*config.Config, string) xaiOAuthService { return xaiSvc }
	t.Cleanup(func() { newXAIOAuthService = origXAI })

	xaiPath := filepath.Join(authDir, "xai-both.json")
	xaiMeta := map[string]any{
		"type":          "xai",
		"email":         "a@x.test",
		"sub":           "sub-a",
		"access_token":  "old-xai",
		"refresh_token": "old-xai-refresh",
	}
	rawXAI, _ := json.Marshal(xaiMeta)
	if errWrite := os.WriteFile(xaiPath, rawXAI, 0o600); errWrite != nil {
		t.Fatalf("write xai-both.json: %v", errWrite)
	}
	if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
		ID:             "xai-both.json",
		FileName:       "xai-both.json",
		Provider:       "xai",
		RequiresSignIn: true,
		Metadata:       xaiMeta,
		Attributes:     map[string]string{coreauth.AttributePath: xaiPath},
	}); errReg != nil {
		t.Fatalf("register xai-both.json: %v", errReg)
	}

	wXAISubOnly := httptest.NewRecorder()
	router.ServeHTTP(wXAISubOnly, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"xai","name":"xai-both.json"}`)))
	var xaiSubOnlyStart struct{ State string }
	_ = json.Unmarshal(wXAISubOnly.Body.Bytes(), &xaiSubOnlyStart)
	if st := pollOAuthStatusUntilDone(t, router, xaiSubOnlyStart.State); st["status"] != "ok" || st["auth_id"] != "xai-both.json" || st["adopted"] != true {
		t.Fatalf("xai sub-only status = %#v, want ok & adopted", st)
	}
	savedXAIRaw, errReadXAI := os.ReadFile(xaiPath)
	if errReadXAI != nil {
		t.Fatalf("read xai-both.json: %v", errReadXAI)
	}
	var savedXAIMap map[string]any
	_ = json.Unmarshal(savedXAIRaw, &savedXAIMap)
	if savedXAIMap["email"] != "a@x.test" || savedXAIMap["sub"] != "sub-a" {
		t.Fatalf("saved xai-both.json after sub-only login = %#v, want preserved email=a@x.test and sub=sub-a", savedXAIMap)
	}

	// Email-only login also succeeds and preserves sub.
	xaiSvc.email = "a@x.test"
	xaiSvc.subject = ""
	xaiSvc.accessToken = "xai-email-only-fresh"
	wXAIEmailOnly := httptest.NewRecorder()
	router.ServeHTTP(wXAIEmailOnly, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"xai","name":"xai-both.json"}`)))
	var xaiEmailOnlyStart struct{ State string }
	_ = json.Unmarshal(wXAIEmailOnly.Body.Bytes(), &xaiEmailOnlyStart)
	if st := pollOAuthStatusUntilDone(t, router, xaiEmailOnlyStart.State); st["status"] != "ok" || st["auth_id"] != "xai-both.json" || st["adopted"] != true {
		t.Fatalf("xai email-only status = %#v, want ok & adopted", st)
	}
	savedXAIRaw2, errReadXAI2 := os.ReadFile(xaiPath)
	if errReadXAI2 != nil {
		t.Fatalf("read xai-both.json after email-only: %v", errReadXAI2)
	}
	var savedXAIMap2 map[string]any
	_ = json.Unmarshal(savedXAIRaw2, &savedXAIMap2)
	if savedXAIMap2["email"] != "a@x.test" || savedXAIMap2["sub"] != "sub-a" {
		t.Fatalf("saved xai-both.json after email-only login = %#v, want preserved email=a@x.test and sub=sub-a", savedXAIMap2)
	}

	// Conflicting email with matching sub must be rejected.
	xaiSvc.email = "conflict@x.test"
	xaiSvc.subject = "sub-a"
	wXAIConflict := httptest.NewRecorder()
	router.ServeHTTP(wXAIConflict, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"xai","name":"xai-both.json"}`)))
	var xaiConflictStart struct{ State string }
	_ = json.Unmarshal(wXAIConflict.Body.Bytes(), &xaiConflictStart)
	if st := pollOAuthStatusUntilDone(t, router, xaiConflictStart.State); st["status"] != "error" {
		t.Fatalf("xai conflicting email status = %#v, want error", st)
	}

	// 4. Targeted completion with verify=true runs VerifyInference and reports verification fields.
	agPath := filepath.Join(authDir, "ag-verify.json")
	agMeta := map[string]any{
		"type":          "antigravity",
		"email":         "verify@x.test",
		"access_token":  "old-ag",
		"refresh_token": "old-ag-refresh",
	}
	rawAG, _ := json.Marshal(agMeta)
	if errWrite := os.WriteFile(agPath, rawAG, 0o600); errWrite != nil {
		t.Fatalf("write ag-verify.json: %v", errWrite)
	}
	agExec := &recordingRefreshVerifyExecutor{}
	managerWithVerify := coreauth.NewManager(nil, nil, nil)
	managerWithVerify.RegisterExecutor(agExec)
	if _, errReg := managerWithVerify.Register(context.Background(), &coreauth.Auth{
		ID:             "ag-verify.json",
		FileName:       "ag-verify.json",
		Provider:       "antigravity",
		RequiresSignIn: true,
		Metadata:       agMeta,
		Attributes:     map[string]string{coreauth.AttributePath: agPath},
	}); errReg != nil {
		t.Fatalf("register ag-verify.json: %v", errReg)
	}

	agSvc := &targetedTestAntigravityOAuthService{
		email:       "verify@x.test",
		accessToken: "ag-verified-access",
	}
	origAG := newAntigravityOAuthService
	newAntigravityOAuthService = func(*config.Config) antigravityOAuthService { return agSvc }
	t.Cleanup(func() { newAntigravityOAuthService = origAG })

	hVerify := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, managerWithVerify)
	routerVerify := gin.New()
	routerVerify.POST("/v8/management/oauth/auth-url", hVerify.StartOAuthV8)
	routerVerify.GET("/v8/management/oauth/status", hVerify.GetAuthStatus)

	wVerifyStart := httptest.NewRecorder()
	routerVerify.ServeHTTP(wVerifyStart, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"antigravity","name":"ag-verify.json","verify":true,"verify_model":"gemini-2.5-flash"}`)))
	if wVerifyStart.Code != http.StatusOK {
		t.Fatalf("antigravity verify StartOAuthV8 status = %d: %s", wVerifyStart.Code, wVerifyStart.Body.String())
	}
	var verifyStartBody struct{ State string }
	_ = json.Unmarshal(wVerifyStart.Body.Bytes(), &verifyStartBody)
	_, _ = WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", verifyStartBody.State, "ag-code-verify", "")
	verifyStatus := pollOAuthStatusUntilDone(t, routerVerify, verifyStartBody.State)
	if verifyStatus["status"] != "ok" || verifyStatus["inference_verified"] != true || verifyStatus["recovery_stage"] != "inference_verified" {
		t.Fatalf("antigravity verify status = %#v, want ok with inference_verified=true", verifyStatus)
	}
	if agExec.calls == 0 || agExec.lastModel != "gemini-2.5-flash" {
		t.Fatalf("expected executor VerifyInference call with model gemini-2.5-flash, got calls=%d model=%q", agExec.calls, agExec.lastModel)
	}
}

type recordingRefreshVerifyExecutor struct {
	calls     int
	lastModel string
	err       error
}

func (e *recordingRefreshVerifyExecutor) Identifier() string { return "antigravity" }
func (e *recordingRefreshVerifyExecutor) Execute(_ context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	e.lastModel = req.Model
	if e.err != nil {
		return cliproxyexecutor.Response{}, e.err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"id":"resp_ok"}`)}, nil
}
func (e *recordingRefreshVerifyExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls++
	e.lastModel = req.Model
	if e.err != nil {
		return nil, e.err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\"}\n\n")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}
func (e *recordingRefreshVerifyExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (e *recordingRefreshVerifyExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *recordingRefreshVerifyExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (e *recordingRefreshVerifyExecutor) VerifyInference(_ context.Context, _ *coreauth.Auth, model string) error {
	e.calls++
	e.lastModel = model
	return e.err
}
