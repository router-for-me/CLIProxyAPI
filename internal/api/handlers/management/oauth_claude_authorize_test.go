package management

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type claudeAuthorizationProbe struct {
	*claude.ClaudeAuth
	authURL string
	state   string
}

func (p *claudeAuthorizationProbe) GenerateAuthURL(state string, pkceCodes *claude.PKCECodes) (string, string, error) {
	authURL, returnedState, errGenerate := p.ClaudeAuth.GenerateAuthURL(state, pkceCodes)
	if errGenerate != nil {
		return "", "", errGenerate
	}
	p.authURL, p.state = authURL, returnedState
	// Stop before starting a background callback waiter that could outlive this test.
	return "", "", errors.New("authorization URL inspected")
}

func TestClaudeOAuthV8CallbackPreservesURLSafeState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, form := range []string{"json", "redirect_url", "query"} {
		t.Run(form, func(t *testing.T) {
			authDir := t.TempDir()
			handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
			router := gin.New()
			router.POST("/v8/management/oauth/callback", handler.PostOAuthCallbackV8)
			router.GET("/v8/management/oauth/callback", handler.GetOAuthCallbackV8)
			state := strings.Repeat("Ab9-_", 8) + "AbA"
			RegisterOAuthSession(state, "anthropic")
			defer CancelOAuthSession(state)

			callbackRequest := func(callbackState string) *http.Request {
				values := map[string]string{"provider": "claude", "code": "callback-code", "state": callbackState}
				query := url.Values{}
				for key, value := range values {
					query.Set(key, value)
				}
				if form == "query" {
					return httptest.NewRequest(http.MethodGet, "/v8/management/oauth/callback?"+query.Encode(), nil)
				}
				if form == "redirect_url" {
					values = map[string]string{"redirect_url": "http://localhost:54545/callback?" + query.Encode()}
				}
				doc := ojson.NewDocString(`{}`)
				for key, value := range values {
					if errSet := doc.Set(key, value); errSet != nil {
						t.Fatalf("set callback field %s: %v", key, errSet)
					}
				}
				request := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/callback", bytes.NewReader(doc.Bytes()))
				request.Header.Set("Content-Type", "application/json")
				return request
			}

			wrongState := httptest.NewRecorder()
			router.ServeHTTP(wrongState, callbackRequest(strings.ToLower(state)))
			if wrongState.Code != http.StatusNotFound {
				t.Fatalf("wrong-state callback status = %d, want 404: %s", wrongState.Code, wrongState.Body.String())
			}
			entries, errReadDir := os.ReadDir(authDir)
			if errReadDir != nil || len(entries) != 0 {
				t.Fatalf("wrong-state callback wrote a file: entries = %v, error = %v", entries, errReadDir)
			}

			response := httptest.NewRecorder()
			router.ServeHTTP(response, callbackRequest(state))
			if response.Code != http.StatusOK {
				t.Fatalf("callback status = %d, want 200: %s", response.Code, response.Body.String())
			}
			payload, errRead := os.ReadFile(filepath.Join(authDir, ".oauth-anthropic-"+state+".oauth"))
			if errRead != nil {
				t.Fatalf("read callback file: %v", errRead)
			}
			if got := ojson.GetBytesNoCopy(payload, "state").String(); got != state {
				t.Fatalf("callback file state = %q, want %q", got, state)
			}
			if got := ojson.GetBytesNoCopy(payload, "code").String(); got != "callback-code" {
				t.Fatalf("callback file code = %q, want callback-code", got)
			}
		})
	}
}

func TestStartOAuthV8ClaudeUsesNativeAuthorizationParameters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	probe := &claudeAuthorizationProbe{ClaudeAuth: &claude.ClaudeAuth{}}
	originalFactory := newClaudeOAuthService
	newClaudeOAuthService = func(*config.Config, string) claudeOAuthService { return probe }
	defer func() { newClaudeOAuthService = originalFactory }()

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", handler.StartOAuthV8)

	request := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"claude"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("authorization probe status = %d, want 500: %s", response.Code, response.Body.String())
	}
	state := probe.state
	if len(state) != 43 {
		t.Errorf("state length = %d, want 43", len(state))
	}
	decoded, errDecode := base64.RawURLEncoding.DecodeString(state)
	if errDecode != nil || len(decoded) != 32 {
		t.Errorf("state must encode 32 random bytes as unpadded base64url: decoded length = %d, error = %v", len(decoded), errDecode)
	}
	if errValidate := ValidateOAuthState(state); errValidate != nil {
		t.Errorf("generated state cannot be used by the callback handler: %v", errValidate)
	}
	parsed, errParse := url.Parse(probe.authURL)
	if errParse != nil {
		t.Fatalf("parse authorization URL: %v", errParse)
	}
	if got := parsed.Query().Get("state"); got != state {
		t.Error("authorization URL did not preserve the generated state")
	}
	const wantScope = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	if got := parsed.Query().Get("scope"); got != wantScope {
		t.Errorf("authorization scope = %q, want %q", got, wantScope)
	}
}
