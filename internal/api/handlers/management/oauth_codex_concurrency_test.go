package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type fakeCodexOAuthService struct{}

func (f *fakeCodexOAuthService) GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error) {
	return "https://auth.example.test/oauth?state=" + state, nil
}

func (f *fakeCodexOAuthService) ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	now := time.Now()
	return &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      "invalid-test-id-token",
			AccessToken:  "access-" + code,
			RefreshToken: "refresh-" + code,
			Email:        "codex-" + code + "@example.test",
			Expire:       now.Add(time.Hour).Format(time.RFC3339),
		},
		LastRefresh: now.Format(time.RFC3339),
	}, nil
}

func (f *fakeCodexOAuthService) CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage {
	planType := codex.DefaultPlanType
	if bundle != nil && bundle.TokenData.PlanType != "" {
		planType = bundle.TokenData.PlanType
	}
	return &codex.CodexTokenStorage{
		IDToken:      bundle.TokenData.IDToken,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
		PlanType:     planType,
	}
}

func TestRequestCodexTokenCompletionKeepsConcurrentSessionPending(t *testing.T) {
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		return &fakeCodexOAuthService{}
	}
	defer func() {
		newCodexOAuthService = originalNewCodexOAuthService
	}()

	authDir := filepath.Join(t.TempDir(), "auths")
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)

	firstState := requestCodexTokenState(t, router)
	secondState := requestCodexTokenState(t, router)
	defer CompleteOAuthSession(firstState)
	defer CompleteOAuthSession(secondState)

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", firstState, "first-code", ""); errWrite != nil {
		t.Fatalf("write first callback file: %v", errWrite)
	}

	waitForOAuthSessionDone(t, firstState)
	if !IsOAuthSessionPending(secondState, "codex") {
		t.Fatalf("expected concurrent codex session %s to remain pending after %s completed", secondState, firstState)
	}
}

func requestCodexTokenState(t *testing.T, router http.Handler) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/codex-auth-url", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload struct {
		State string `json:"state"`
	}
	if errDecode := json.Unmarshal(w.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode codex auth URL response: %v", errDecode)
	}
	if payload.State == "" {
		t.Fatalf("expected codex auth URL response to include state")
	}
	return payload.State
}

func waitForOAuthSessionDone(t *testing.T, state string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !IsOAuthSessionPending(state, "codex") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for codex session %s to complete", state)
}

func makeOAuthTestJWT(planType string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	authInfo := map[string]any{
		"chatgpt_account_id": "acc-oauth-test",
	}
	if planType != "" {
		authInfo["chatgpt_plan_type"] = planType
	}
	claimsMap := map[string]any{
		"email":                       "oauth-user@example.test",
		"https://api.openai.com/auth": authInfo,
	}
	payloadBytes, _ := json.Marshal(claimsMap)
	claims := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + claims + "."
}

type fakeCustomPlanCodexOAuthService struct {
	planType string
}

func (f *fakeCustomPlanCodexOAuthService) GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error) {
	return "https://auth.example.test/oauth?state=" + state, nil
}

func (f *fakeCustomPlanCodexOAuthService) ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	now := time.Now()
	idToken := makeOAuthTestJWT(f.planType)
	return &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      idToken,
			AccessToken:  "access-" + code,
			RefreshToken: "refresh-" + code,
			Email:        "oauth-user@example.test",
			PlanType:     f.planType,
			Expire:       now.Add(time.Hour).Format(time.RFC3339),
		},
		LastRefresh: now.Format(time.RFC3339),
	}, nil
}

func (f *fakeCustomPlanCodexOAuthService) CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage {
	planType := codex.DefaultPlanType
	if bundle != nil && bundle.TokenData.PlanType != "" {
		planType = bundle.TokenData.PlanType
	}
	return &codex.CodexTokenStorage{
		IDToken:      bundle.TokenData.IDToken,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
		PlanType:     planType,
	}
}

func TestRequestCodexToken_PlanTypeSavedToAuthFile(t *testing.T) {
	tests := []struct {
		name         string
		claimPlan    string
		wantPlanJSON string
	}{
		{
			name:         "missing plan type defaults to free in auth file",
			claimPlan:    "",
			wantPlanJSON: "free",
		},
		{
			name:         "specified plan type preserved in auth file",
			claimPlan:    "pro",
			wantPlanJSON: "pro",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalNewCodexOAuthService := newCodexOAuthService
			newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
				return &fakeCustomPlanCodexOAuthService{planType: tt.claimPlan}
			}
			defer func() {
				newCodexOAuthService = originalNewCodexOAuthService
			}()

			authDir := filepath.Join(t.TempDir(), "auths")
			handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
			router := gin.New()
			router.GET("/codex-auth-url", handler.RequestCodexToken)

			state := requestCodexTokenState(t, router)
			defer CompleteOAuthSession(state)

			if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", state, "test-code", ""); errWrite != nil {
				t.Fatalf("write callback file: %v", errWrite)
			}

			waitForOAuthSessionDone(t, state)

			entries, errReadDir := os.ReadDir(authDir)
			if errReadDir != nil {
				t.Fatalf("read auth dir error: %v", errReadDir)
			}

			var savedFile string
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".json") && strings.HasPrefix(entry.Name(), "codex-") {
					savedFile = filepath.Join(authDir, entry.Name())
					break
				}
			}
			if savedFile == "" {
				t.Fatalf("expected codex auth file in %s, got none", authDir)
			}

			data, errReadFile := os.ReadFile(savedFile)
			if errReadFile != nil {
				t.Fatalf("read saved auth file error: %v", errReadFile)
			}

			var parsed map[string]any
			if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
				t.Fatalf("unmarshal auth file error: %v", errUnmarshal)
			}

			if got := parsed["plan_type"]; got != tt.wantPlanJSON {
				t.Errorf("saved auth file plan_type = %v, want %q", got, tt.wantPlanJSON)
			}
		})
	}
}
