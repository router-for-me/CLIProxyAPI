package management

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fakeDevinOAuthService struct {
	exchange func(context.Context, string, string) (string, error)
	create   func(context.Context, string) (*coreauth.Auth, error)
}

func (f *fakeDevinOAuthService) BuildAuthorizationURL(redirectURI, challenge, state string) string {
	return devin.NewDevinAuthService(nil).BuildAuthorizationURL(redirectURI, challenge, state)
}

func (f *fakeDevinOAuthService) ExchangeCodeForToken(ctx context.Context, code, verifier string) (string, error) {
	if f.exchange != nil {
		return f.exchange(ctx, code, verifier)
	}
	return "eyJ.test.token", nil
}

func (f *fakeDevinOAuthService) CreateAuthRecord(ctx context.Context, token string) (*coreauth.Auth, error) {
	if f.create != nil {
		return f.create(ctx, token)
	}
	return &coreauth.Auth{
		ID: "devin-test.json", FileName: "devin-test.json", Provider: "devin",
		Metadata: map[string]any{"type": "devin", "api_key": devin.FormatSessionToken(token), "auth_kind": "oauth"},
	}, nil
}

func TestDevinRemoteOAuthFlow(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, nil)
	exchanged := make(chan string, 1)
	service := &fakeDevinOAuthService{exchange: func(ctx context.Context, code, verifier string) (string, error) {
		if code != "remote-code" {
			t.Errorf("code = %q", code)
		}
		if ctx.Err() != nil {
			t.Error("login inherited completed HTTP request cancellation")
		}
		exchanged <- verifier
		return "eyJ.test.token", nil
	}}
	originalFactory := newDevinOAuthService
	newDevinOAuthService = func(*config.Config) devinOAuthService { return service }
	t.Cleanup(func() { newDevinOAuthService = originalFactory })

	router := gin.New()
	router.GET("/devin-auth-url", h.RequestDevinToken)
	router.POST("/oauth-callback", h.PostOAuthCallback)
	router.GET("/get-auth-status", h.GetAuthStatus)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/devin-auth-url?is_webui=true", nil).WithContext(requestCtx))
	cancelRequest()
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var start struct{ State, URL, Status string }
	if errDecode := json.Unmarshal(w.Body.Bytes(), &start); errDecode != nil {
		t.Fatal(errDecode)
	}
	t.Cleanup(func() { CancelOAuthSession(start.State) })
	u, errParse := url.Parse(start.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	query := u.Query()
	if start.Status != "ok" || start.State == "" || query.Get("state") != start.State || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("invalid authorization response: %s", w.Body.String())
	}
	if got := query.Get("redirect_uri"); got != "http://127.0.0.1:8317/callback" {
		t.Fatalf("redirect_uri = %q", got)
	}
	if query.Get("code_verifier") != "" {
		t.Fatal("PKCE verifier exposed")
	}

	secondState := start.State + "-second"
	RegisterOAuthSession(secondState, "devin")
	defer CancelOAuthSession(secondState)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/get-auth-status?state="+start.State, nil))
	if !strings.Contains(w.Body.String(), `"status":"wait"`) {
		t.Fatalf("pending: %s", w.Body.String())
	}

	redirect := query.Get("redirect_uri") + "?code=remote-code&state=" + start.State
	body, errMarshal := json.Marshal(map[string]string{"provider": "cognition", "redirect_url": redirect})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/oauth-callback", strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	select {
	case verifier := <-exchanged:
		digest := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(digest[:]) != query.Get("code_challenge") {
			t.Fatal("PKCE challenge does not match verifier")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("token exchange did not start")
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/get-auth-status?state="+start.State, nil))
		var status map[string]string
		if errDecode := json.Unmarshal(w.Body.Bytes(), &status); errDecode != nil {
			t.Fatal(errDecode)
		}
		if status["status"] == "ok" {
			break
		}
		if status["status"] == "error" {
			t.Fatalf("login failed: %v", status)
		}
		select {
		case <-deadline.C:
			t.Fatal("login did not complete")
		case <-ticker.C:
		}
	}
	if !IsOAuthSessionPending(secondState, "devin") {
		t.Fatal("completed another login session")
	}
	data, errRead := os.ReadFile(filepath.Join(authDir, "devin-test.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	var record map[string]any
	if errDecode := json.Unmarshal(data, &record); errDecode != nil {
		t.Fatal(errDecode)
	}
	if record["api_key"] != "devin-session-token$eyJ.test.token" || record["type"] != "devin" || record["auth_kind"] != "oauth" {
		t.Fatalf("unexpected credential: %v", record)
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/oauth-callback", strings.NewReader(string(body))))
	if w.Code != http.StatusConflict {
		t.Fatalf("replay: %d", w.Code)
	}
}

func TestCompleteDevinOAuthFailures(t *testing.T) {
	for _, test := range []struct {
		name               string
		payload            string
		exchangeErr        bool
		emptyToken         bool
		cancelDuringCreate bool
		saveErr            bool
		want               string
	}{
		{name: "denied", payload: `{"state":"test-state","error":"access_denied"}`, want: "Devin authorization denied"},
		{name: "mismatched state", payload: `{"state":"wrong","code":"code"}`, want: "State code error"},
		{name: "missing code", payload: `{"state":"test-state"}`, want: "Missing authorization code"},
		{name: "malformed callback", payload: `{`, want: "Invalid OAuth callback"},
		{name: "exchange error", exchangeErr: true, want: "Failed to exchange authorization code for tokens"},
		{name: "empty token", emptyToken: true, want: "Failed to exchange authorization code for tokens"},
		{name: "cancel during profile", cancelDuringCreate: true},
		{name: "save error", saveErr: true, want: "Failed to save authentication tokens"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const state = "test-state"
			authDir := t.TempDir()
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
			RegisterOAuthSession(state, "devin")
			defer CompleteOAuthSession(state)
			payload := test.payload
			if payload == "" {
				payload = `{"state":"test-state","code":"code"}`
			}
			path := filepath.Join(authDir, ".oauth-devin-"+state+".oauth")
			if errWrite := os.WriteFile(path, []byte(payload), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			service := &fakeDevinOAuthService{}
			if test.exchangeErr {
				service.exchange = func(context.Context, string, string) (string, error) { return "", errors.New("secret-upstream-token") }
			}
			if test.emptyToken {
				service.exchange = func(context.Context, string, string) (string, error) { return "", nil }
			}
			if test.cancelDuringCreate {
				service.create = func(context.Context, string) (*coreauth.Auth, error) {
					CancelOAuthSession(state)
					return &coreauth.Auth{}, nil
				}
			}
			if test.saveErr {
				h.postAuthHook = func(context.Context, *coreauth.Auth) error { return errors.New("save failed") }
			}
			h.completeDevinOAuth(context.Background(), authDir, state, "verifier", service)
			_, status, ok := GetOAuthSession(state)
			if test.cancelDuringCreate {
				if ok {
					t.Fatal("cancelled session was recreated")
				}
			} else if !ok || status != test.want {
				t.Fatalf("status = %q, exists = %v; want %q", status, ok, test.want)
			}
			if _, errStat := os.Stat(path); !errors.Is(errStat, os.ErrNotExist) {
				t.Fatalf("callback file not removed: %v", errStat)
			}
			entries, errRead := os.ReadDir(authDir)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if len(entries) != 0 {
				t.Fatalf("credentials saved for failed/cancelled flow: %v", entries)
			}
		})
	}
}

func TestWaitDevinOAuthCallbackExpiredContext(t *testing.T) {
	const state = "devin-expired-context"
	RegisterOAuthSession(state, "devin")
	defer CompleteOAuthSession(state)
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	_, errWait := waitDevinOAuthCallback(ctx, filepath.Join(t.TempDir(), "missing.oauth"), state)
	if errWait == nil || errWait.Error() != "Timeout waiting for OAuth callback" {
		t.Fatalf("error = %v", errWait)
	}
}
