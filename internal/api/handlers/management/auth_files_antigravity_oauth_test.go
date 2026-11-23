package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type stubAntigravityProjectOAuthService struct {
	email      string
	projectID  string
	projectErr error
}

func (s *stubAntigravityProjectOAuthService) BuildAuthURL(state, _ string) string {
	return "https://accounts.google.com/o/oauth2/v2/auth?state=" + state
}

func (s *stubAntigravityProjectOAuthService) ExchangeCodeForTokens(_ context.Context, code, _ string) (*antigravity.TokenResponse, error) {
	return &antigravity.TokenResponse{
		AccessToken:  "ag-access-" + code,
		RefreshToken: "ag-refresh-" + code,
		ExpiresIn:    3600,
		TokenType:    "Bearer",
	}, nil
}

func (s *stubAntigravityProjectOAuthService) FetchUserInfo(_ context.Context, _ string) (string, error) {
	return s.email, nil
}

func (s *stubAntigravityProjectOAuthService) FetchProjectID(_ context.Context, _ string) (string, error) {
	return s.projectID, s.projectErr
}

func TestRequestAntigravityTokenFailsSessionWhenFetchProjectIDErrorsWithoutProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name       string
		email      string
		projectID  string
		projectErr error
	}{
		{
			name:  "tos_violation_403",
			email: "broken-proj@example.com",
			projectErr: &antigravity.HTTPStatusError{
				StatusCodeValue: http.StatusForbidden,
				Message:         "request failed with status 403: TOS_VIOLATION",
			},
		},
		{
			name:  "validation_required_403",
			email: "validation-required@example.com",
			projectErr: &antigravity.HTTPStatusError{
				StatusCodeValue: http.StatusForbidden,
				Message:         "request failed with status 403: VALIDATION_REQUIRED",
			},
		},
		{
			name:       "upstream_transport_error",
			email:      "transport-fail@example.com",
			projectErr: errors.New("dial tcp: connection refused"),
		},
		{
			name:      "empty_project_id",
			email:     "empty-proj@example.com",
			projectID: "   ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origFactory := newAntigravityOAuthService
			newAntigravityOAuthService = func(*config.Config) antigravityOAuthService {
				return &stubAntigravityProjectOAuthService{
					email:      tc.email,
					projectID:  tc.projectID,
					projectErr: tc.projectErr,
				}
			}
			t.Cleanup(func() { newAntigravityOAuthService = origFactory })

			authDir := t.TempDir()
			manager := coreauth.NewManager(nil, nil, nil)
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

			router := gin.New()
			router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
			router.GET("/status", h.GetAuthStatus)

			startRec := httptest.NewRecorder()
			startReq := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"antigravity"}`))
			startReq.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(startRec, startReq)
			if startRec.Code != http.StatusOK {
				t.Fatalf("StartOAuthV8 status = %d, body = %s", startRec.Code, startRec.Body.String())
			}

			var startResp struct {
				State string `json:"state"`
			}
			if errUnmarshal := json.Unmarshal(startRec.Body.Bytes(), &startResp); errUnmarshal != nil {
				t.Fatalf("unmarshal start response: %v", errUnmarshal)
			}
			if strings.TrimSpace(startResp.State) == "" {
				t.Fatalf("expected non-empty state, got %s", startRec.Body.String())
			}
			t.Cleanup(func() { CancelOAuthSession(startResp.State) })

			if _, errCb := WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", startResp.State, "ag-code", ""); errCb != nil {
				t.Fatalf("write callback file: %v", errCb)
			}

			statusResp := waitForOAuthStatus(t, router, startResp.State)
			if statusResp.Status != "error" || statusResp.Error != "Failed to fetch project ID" {
				t.Fatalf("status = %#v, want status=error error=%q", statusResp, "Failed to fetch project ID")
			}

			entries, errReadDir := os.ReadDir(authDir)
			if errReadDir != nil {
				t.Fatalf("read authDir: %v", errReadDir)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".json") {
					t.Fatalf("unexpected credential file persisted on FetchProjectID failure: %s", entry.Name())
				}
			}

			if registered := manager.List(); len(registered) != 0 {
				t.Fatalf("expected no auth registered in manager on FetchProjectID failure, got %d (%+v)", len(registered), registered)
			}
		})
	}
}

func TestRequestAntigravityTokenSucceedsAndTrimsProjectIDWithoutProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origFactory := newAntigravityOAuthService
	newAntigravityOAuthService = func(*config.Config) antigravityOAuthService {
		return &stubAntigravityProjectOAuthService{
			email:     "healthy-proj@example.com",
			projectID: "  ag-proj-123  ",
		}
	}
	t.Cleanup(func() { newAntigravityOAuthService = origFactory })

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/status", h.GetAuthStatus)

	startRec := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"antigravity"}`))
	startReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("StartOAuthV8 status = %d, body = %s", startRec.Code, startRec.Body.String())
	}

	var startResp struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(startRec.Body.Bytes(), &startResp); errUnmarshal != nil {
		t.Fatalf("unmarshal start response: %v", errUnmarshal)
	}
	t.Cleanup(func() { CancelOAuthSession(startResp.State) })

	if _, errCb := WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", startResp.State, "ag-code", ""); errCb != nil {
		t.Fatalf("write callback file: %v", errCb)
	}

	statusResp := waitForOAuthStatus(t, router, startResp.State)
	if statusResp.Status != "ok" {
		t.Fatalf("status = %#v, want ok", statusResp)
	}

	expectedFile := antigravity.CredentialFileName("healthy-proj@example.com")
	if _, errStat := os.Stat(filepath.Join(authDir, expectedFile)); errStat != nil {
		t.Fatalf("expected persisted credential file %s: %v", expectedFile, errStat)
	}

	registered := manager.List()
	if len(registered) != 1 {
		t.Fatalf("expected 1 registered auth, got %d", len(registered))
	}
	gotProject, _ := registered[0].Metadata["project_id"].(string)
	if gotProject != "ag-proj-123" {
		t.Fatalf("registered Metadata[project_id] = %q, want %q", gotProject, "ag-proj-123")
	}
}
