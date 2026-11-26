package management

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
)

func TestCodexV8DeviceOAuth(t *testing.T) {
	tests := []struct {
		name, method, poll, wantStatus string
		pollCode                       int
		cancel                         bool
	}{
		{name: "authorized", method: http.MethodGet, pollCode: http.StatusOK, poll: `{"authorization_code":"code-fixture","code_verifier":"verifier-fixture","code_challenge":"challenge-fixture"}`, wantStatus: "ok"},
		{name: "denied", method: http.MethodPost, pollCode: http.StatusBadRequest, poll: `{"error":"access_denied"}`, wantStatus: "error"},
		{name: "cancelled", method: http.MethodGet, pollCode: http.StatusForbidden, poll: `{}`, wantStatus: "error", cancel: true},
		{name: "invalid flow", method: http.MethodPost, wantStatus: "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authDir := t.TempDir()
			h := &Handler{cfg: &config.Config{AuthDir: authDir}, tokenStore: sdkAuth.NewFileTokenStore()}
			router := gin.New()
			const base = "/v8/management/oauth"
			router.GET(base+"/auth-url", h.StartOAuthV8)
			router.POST(base+"/auth-url", h.StartOAuthV8)
			router.GET(base+"/status", h.GetAuthStatus)
			router.DELETE(base+"/session", h.CancelAuthSession)

			transport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = transport })
			http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				status, payload := http.StatusOK, ""
				switch req.URL.String() {
				case "https://auth.openai.com/api/accounts/deviceauth/usercode":
					payload = `{"device_auth_id":"device-auth-fixture","user_code":"USER-CODE","interval":"1"}`
				case "https://auth.openai.com/api/accounts/deviceauth/token":
					status, payload = tt.pollCode, tt.poll
				case "https://auth.openai.com/oauth/token":
					payload = fmt.Sprintf(`{"access_token":"access-fixture","refresh_token":"refresh-fixture","id_token":%q,"expires_in":3600}`, makeOAuthTestJWT("plus"))
				default:
					return nil, fmt.Errorf("unexpected OAuth request: %s", req.URL)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(payload))}, nil
			})
			synctest.Test(t, func(t *testing.T) {
				path := base + "/auth-url?provider=codex&flow=device"
				var body io.Reader
				if tt.method == http.MethodPost {
					path = base + "/auth-url"
					body = strings.NewReader(`{"provider":"codex","flow":"device"}`)
				}
				if tt.wantStatus == "invalid" {
					body = strings.NewReader(`{"provider":"codex","flow":"devcie"}`)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(tt.method, path, body))
				if tt.wantStatus == "invalid" {
					if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid flow") {
						t.Fatalf("unexpected invalid flow response (%d): %s", rec.Code, rec.Body)
					}
					return
				}
				var login struct {
					State     string `json:"state"`
					Flow      string `json:"flow"`
					URL       string `json:"url"`
					UserCode  string `json:"user_code"`
					ExpiresIn int    `json:"expires_in"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil || rec.Code != http.StatusOK ||
					login.State == "" || login.Flow != "device" || login.URL != "https://auth.openai.com/codex/device" ||
					login.UserCode != "USER-CODE" || login.ExpiresIn != 900 {
					t.Fatalf("unexpected login response (%d): %s", rec.Code, rec.Body)
				}
				if strings.Contains(rec.Body.String(), "device-auth-fixture") {
					t.Fatalf("login response leaked device auth ID: %s", rec.Body)
				}
				t.Cleanup(func() { CancelOAuthSession(login.State) })
				statusPath := base + "/status?state=" + login.State
				if tt.cancel {
					rec = httptest.NewRecorder()
					router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, base+"/session?state="+login.State, nil))
					if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancelled":true`) {
						t.Fatalf("unexpected cancel response (%d): %s", rec.Code, rec.Body)
					}
				}
				time.Sleep(3 * time.Second) // past the 2s session-cancel watcher tick
				synctest.Wait()
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, statusPath, nil))
				var status struct{ Status string }
				if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || rec.Code != http.StatusOK || status.Status != tt.wantStatus {
					t.Fatalf("unexpected final status (%d): %s", rec.Code, rec.Body)
				}
				saved, errGlob := filepath.Glob(filepath.Join(authDir, "codex-*.json"))
				if errGlob != nil {
					t.Fatalf("glob saved credentials: %v", errGlob)
				}
				if tt.wantStatus == "error" {
					if len(saved) != 0 {
						t.Fatalf("failed login should not save credentials: %v", saved)
					}
					return
				}
				if len(saved) != 1 {
					t.Fatalf("expected one saved Codex credential, got %v", saved)
				}
				data, errRead := os.ReadFile(saved[0])
				var metadata map[string]any
				if errRead != nil || json.Unmarshal(data, &metadata) != nil || metadata["type"] != "codex" ||
					metadata["email"] != "oauth-user@example.test" || metadata["account_id"] != "acc-oauth-test" ||
					metadata["access_token"] != "access-fixture" || metadata["refresh_token"] != "refresh-fixture" {
					t.Fatalf("Codex credentials were not persisted correctly (%v): %s", errRead, data)
				}
			})
		})
	}
}
