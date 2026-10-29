package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const kunkunProfileTestProxyURL = "http://user:pass@127.0.0.1:18080"

type kunkunProfileTestCodexService struct{}

func (s *kunkunProfileTestCodexService) GenerateAuthURL(state string, _ *codex.PKCECodes) (string, error) {
	return "https://auth.example.test/oauth?state=" + state, nil
}

func (s *kunkunProfileTestCodexService) ExchangeCodeForTokens(_ context.Context, code string, _ *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	now := time.Now()
	return &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			AccessToken:  "access-" + code,
			RefreshToken: "refresh-" + code,
			AccountID:    "acc-" + code,
			Email:        "kunkun-" + code + "@example.test",
			PlanType:     "plus",
			Expire:       now.Add(time.Hour).Format(time.RFC3339),
		},
		LastRefresh: now.Format(time.RFC3339),
	}, nil
}

func (s *kunkunProfileTestCodexService) CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage {
	return &codex.CodexTokenStorage{
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
		PlanType:     bundle.TokenData.PlanType,
	}
}

type kunkunProfileRecordingLauncher struct {
	mu        sync.Mutex
	profileID string
}

func (l *kunkunProfileRecordingLauncher) Launch(_ context.Context, opts browser.LaunchOptions) error {
	l.mu.Lock()
	l.profileID = opts.ProfileID
	l.mu.Unlock()
	return nil
}

func (l *kunkunProfileRecordingLauncher) profile() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.profileID
}

func (l *kunkunProfileRecordingLauncher) waitForProfile(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if id := l.profile(); id != "" {
			return id
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("launcher did not receive a profile id")
	return ""
}

func TestKunKunOAuthSessionPersistsProfileID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Cleanup(browser.ClearLauncher)

	originalFactory := newCodexOAuthService
	newCodexOAuthService = func(*config.Config) codexOAuthService {
		return &kunkunProfileTestCodexService{}
	}
	t.Cleanup(func() { newCodexOAuthService = originalFactory })

	t.Run("launcher installed", func(t *testing.T) {
		launcher := &kunkunProfileRecordingLauncher{}
		browser.SetLauncher(launcher)
		t.Cleanup(browser.ClearLauncher)

		persisted, launchID := runKunKunProfileSession(t, launcher)
		want := browser.DeriveKunKunProfileID("codex", kunkunProfileTestProxyURL)
		if launchID != want {
			t.Fatalf("launch ProfileID = %q, want derived %q", launchID, want)
		}
		if got := persisted.Attributes[browser.KunKunProfileIDAttribute]; got != launchID {
			t.Fatalf("persisted %s = %q, want launch id %q", browser.KunKunProfileIDAttribute, got, launchID)
		}
	})

	t.Run("no launcher", func(t *testing.T) {
		browser.ClearLauncher()

		persisted, launchID := runKunKunProfileSession(t, nil)
		if launchID != "" {
			t.Fatalf("launcher profile = %q, want none", launchID)
		}
		if got := persisted.Attributes[browser.KunKunProfileIDAttribute]; got != "" {
			t.Fatalf("persisted %s = %q, want no attribute without a launcher", browser.KunKunProfileIDAttribute, got)
		}
	})
}

func TestApplySessionProxyToAuthRecordWritesKunKunProfileID(t *testing.T) {
	t.Cleanup(browser.ClearLauncher)
	browser.SetLauncher(&kunkunProfileRecordingLauncher{})

	record := &coreauth.Auth{Provider: "codex"}
	applySessionProxyToAuthRecord(record, "")
	if _, ok := record.Attributes[browser.KunKunProfileIDAttribute]; ok {
		t.Fatal("empty session proxy must not write a KunKun profile id")
	}

	applySessionProxyToAuthRecord(record, kunkunProfileTestProxyURL)
	got := record.Attributes[browser.KunKunProfileIDAttribute]
	want := browser.DeriveKunKunProfileID("codex", kunkunProfileTestProxyURL)
	if got != want {
		t.Fatalf("%s = %q, want %q", browser.KunKunProfileIDAttribute, got, want)
	}
}

// runKunKunProfileSession drives a full codex OAuth session through the v8
// management endpoints and returns the persisted auth record plus the profile
// id used at launch time (empty when no launcher is installed).
func runKunKunProfileSession(t *testing.T, launcher *kunkunProfileRecordingLauncher) (*coreauth.Auth, string) {
	t.Helper()

	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	var persisted *coreauth.Auth
	h.SetPostAuthPersistHook(func(_ context.Context, auth *coreauth.Auth) error {
		if auth != nil {
			persisted = auth.Clone()
		}
		return nil
	})

	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/status", h.GetAuthStatus)

	reqBody := `{"provider":"codex","proxy_url":"` + kunkunProfileTestProxyURL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v8/management/oauth/auth-url status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var startResp struct {
		State string `json:"state"`
		URL   string `json:"url"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &startResp); errDecode != nil {
		t.Fatalf("decode start response: %v", errDecode)
	}
	if startResp.State == "" || startResp.URL == "" {
		t.Fatalf("unexpected start response: %s", rec.Body.String())
	}
	t.Cleanup(func() { CancelOAuthSession(startResp.State) })

	launchID := ""
	if launcher != nil {
		launchID = launcher.waitForProfile(t)
	}

	if _, errCallback := WriteOAuthCallbackFileForPendingSession(authDir, "codex", startResp.State, "code-1", ""); errCallback != nil {
		t.Fatalf("write callback file: %v", errCallback)
	}
	status := waitForOAuthStatus(t, router, startResp.State)
	if status.Status != "ok" {
		t.Fatalf("oauth status = %#v, want ok", status)
	}
	if persisted == nil {
		t.Fatal("expected post-auth persist hook to be called")
	}
	return persisted, launchID
}
