package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexReauthStartRequiresExactDisabledGeneration(t *testing.T) {
	h, router, target, generation := newCodexReauthTestHandler(t)
	router.POST("/codex-auth-url", h.RequestCodexToken)

	for _, test := range []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"exact target", `{"auth_index":"` + target.Index + `","generation":"` + generation + `"}`, http.StatusOK},
		{"wrong generation", `{"auth_index":"` + target.Index + `","generation":"` + strings.Repeat("0", 64) + `"}`, http.StatusConflict},
		{"missing target", `{"auth_index":"missing","generation":"` + generation + `"}`, http.StatusNotFound},
		{"extra field", `{"auth_index":"` + target.Index + `","generation":"` + generation + `","email":"x@example.test"}`, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/codex-auth-url", strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, test.wantStatus, w.Body.String())
			}
		})
	}
}

func TestCodexReauthCandidateStagesDisabledWithoutRuntimeRegistration(t *testing.T) {
	h, _, target, generation := newCodexReauthTestHandler(t)
	bundle := &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{
		IDToken: "test-id-token", AccessToken: "test-access", RefreshToken: "test-refresh",
		Email: "replacement@example.test", AccountID: "acct-1",
	}}
	targetSpec := codexReauthTarget{AuthID: target.ID, AuthIndex: target.Index, FileName: target.FileName, Path: target.Attributes["path"], Generation: generation, Subject: "acct-1"}

	handle, err := h.stageCodexReauthCandidate(targetSpec, bundle)
	if err != nil {
		t.Fatalf("stage candidate: %v", err)
	}
	if strings.TrimSpace(handle) == "" {
		t.Fatal("stage handle is empty")
	}
	if got := len(h.authManager.List()); got != 1 {
		t.Fatalf("runtime auth count = %d, want original only", got)
	}
	stage, ok := h.codexReauthStages.take(handle)
	if !ok {
		t.Fatal("staged candidate is missing")
	}
	defer os.Remove(stage.Path)
	if filepath.Dir(stage.Path) == filepath.Dir(targetSpec.Path) {
		t.Fatal("candidate was staged in live auth directory")
	}
	raw, err := os.ReadFile(stage.Path)
	if err != nil {
		t.Fatalf("read staged candidate: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode staged candidate: %v", err)
	}
	if stored["disabled"] != true || stored["type"] != "codex" {
		t.Fatalf("candidate disabled/type = %#v/%#v, want true/codex", stored["disabled"], stored["type"])
	}
}

func TestCodexReauthRejectsSubjectOrRename(t *testing.T) {
	h, _, target, generation := newCodexReauthTestHandler(t)
	base := codexReauthTarget{AuthID: target.ID, AuthIndex: target.Index, FileName: target.FileName, Path: target.Attributes["path"], Generation: generation, Subject: "acct-1"}
	bundle := &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{IDToken: "test", AccessToken: "access", AccountID: "acct-2", Email: "x@example.test"}}
	if _, err := h.stageCodexReauthCandidate(base, bundle); err == nil {
		t.Fatal("subject mismatch was accepted")
	}
	bundle.TokenData.AccountID = "acct-1"
	base.FileName = "renamed.json"
	if _, err := h.stageCodexReauthCandidate(base, bundle); err == nil {
		t.Fatal("renamed target was accepted")
	}
}

func TestCodexReauthAdoptsVerifiedCandidateOnceAndKeepsDisabled(t *testing.T) {
	h, router, target, generation := newCodexReauthTestHandler(t)
	h.verifyCodexReauth = func(context.Context, *coreauth.Auth) error { return nil }
	router.POST("/codex-reauth", h.AdoptCodexReauth)
	bundle := &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{
		IDToken: "test-id-token", AccessToken: "replacement-access", RefreshToken: "replacement-refresh",
		Email: "replacement@example.test", AccountID: "acct-1",
	}}
	targetSpec := codexReauthTarget{AuthID: target.ID, AuthIndex: target.Index, FileName: target.FileName, Path: target.Attributes["path"], Generation: generation, Subject: "acct-1"}
	handle, err := h.stageCodexReauthCandidate(targetSpec, bundle)
	if err != nil {
		t.Fatal(err)
	}

	callAdopt := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/codex-reauth", strings.NewReader(`{"stage_handle":"`+handle+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	if w := callAdopt(); w.Code != http.StatusOK {
		t.Fatalf("adopt status = %d; body=%s", w.Code, w.Body.String())
	}
	updated, ok := h.authManager.GetByID(target.ID)
	if !ok || !updated.Disabled || updated.Metadata["access_token"] != "replacement-access" || updated.Index != target.Index {
		t.Fatalf("adopted runtime auth = %#v", updated)
	}
	raw, err := os.ReadFile(targetSpec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "replacement-access") || !strings.Contains(string(raw), `"disabled":true`) {
		t.Fatal("adopted file is not the verified disabled candidate")
	}
	if w := callAdopt(); w.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestCodexReauthChangedTargetDoesNotAdopt(t *testing.T) {
	h, router, target, generation := newCodexReauthTestHandler(t)
	h.verifyCodexReauth = func(context.Context, *coreauth.Auth) error { return nil }
	router.POST("/codex-reauth", h.AdoptCodexReauth)
	targetSpec := codexReauthTarget{AuthID: target.ID, AuthIndex: target.Index, FileName: target.FileName, Path: target.Attributes["path"], Generation: generation, Subject: "acct-1"}
	bundle := &codex.CodexAuthBundle{TokenData: codex.CodexTokenData{IDToken: "test", AccessToken: "replacement", Email: "replacement@example.test", AccountID: "acct-1"}}
	handle, err := h.stageCodexReauthCandidate(targetSpec, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetSpec.Path, []byte(`{"type":"codex","account_id":"acct-1","access_token":"newer","disabled":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/codex-reauth", strings.NewReader(`{"stage_handle":"`+handle+`"}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want conflict; body=%s", w.Code, w.Body.String())
	}
	raw, _ := os.ReadFile(targetSpec.Path)
	if !strings.Contains(string(raw), "newer") || strings.Contains(string(raw), "replacement") {
		t.Fatal("changed target was overwritten")
	}
}

func newCodexReauthTestHandler(t *testing.T) (*Handler, *gin.Engine, *coreauth.Auth, string) {
	t.Helper()
	authDir := filepath.Join(t.TempDir(), "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("acct-1"))
	name := codex.CredentialFileName("replacement@example.test", "", hex.EncodeToString(digest[:])[:8], true)
	path := filepath.Join(authDir, name)
	raw := []byte(`{"type":"codex","account_id":"acct-1","access_token":"old","disabled":true}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	target := &coreauth.Auth{ID: name, Provider: "codex", FileName: name, Disabled: true, Status: coreauth.StatusDisabled, Attributes: map[string]string{"path": path}, Metadata: map[string]any{"account_id": "acct-1"}}
	if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), target); err != nil {
		t.Fatal(err)
	}
	target, _ = manager.GetByID(target.ID)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	original := newCodexOAuthService
	newCodexOAuthService = func(*config.Config) codexOAuthService { return &fakeCodexOAuthService{} }
	t.Cleanup(func() { newCodexOAuthService = original })
	return h, gin.New(), target, credentialGeneration(raw)
}
