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

func TestCopilotV8DeviceOAuth(t *testing.T) {
	tests := []struct {
		name, method, grant, wantStatus string
		cancel                          bool
	}{
		{name: "authorized", method: http.MethodGet, grant: `{"access_token":"github-fixture"}`, wantStatus: "ok"},
		{name: "denied", method: http.MethodPost, grant: `{"error":"access_denied"}`, wantStatus: "error"},
		{name: "cancelled", method: http.MethodGet, grant: `{"access_token":"github-fixture"}`, wantStatus: "error", cancel: true},
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
			savedPath := filepath.Join(authDir, "copilot-12345.json")

			transport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = transport })
			http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				var payload string
				switch req.URL.String() {
				case "https://github.com/login/device/code":
					payload = `{"device_code":"device-fixture","user_code":"USER-CODE","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`
				case "https://github.com/login/oauth/access_token":
					payload = tt.grant
				case "https://api.github.com/user":
					payload = `{"id":12345,"login":"alice"}`
				case "https://api.github.com/copilot_internal/v2/token":
					payload = fmt.Sprintf(`{"token":"copilot-fixture","expires_at":%d}`, time.Now().Add(time.Hour).Unix())
				default:
					return nil, fmt.Errorf("unexpected OAuth request: %s", req.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
			})
			synctest.Test(t, func(t *testing.T) {
				path := base + "/auth-url?provider=copilot"
				var body io.Reader
				if tt.method == http.MethodPost {
					path = base + "/auth-url"
					body = strings.NewReader(`{"provider":"copilot"}`)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(tt.method, path, body))
				var login struct {
					State     string `json:"state"`
					Flow      string `json:"flow"`
					URL       string `json:"url"`
					UserCode  string `json:"user_code"`
					ExpiresIn int    `json:"expires_in"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil || rec.Code != http.StatusOK ||
					login.State == "" || login.Flow != "device" || login.URL != "https://github.com/login/device" ||
					login.UserCode != "USER-CODE" || login.ExpiresIn != 900 {
					t.Fatalf("unexpected login response (%d): %s", rec.Code, rec.Body)
				}
				t.Cleanup(func() { CancelOAuthSession(login.State) })
				statusPath := base + "/status?state=" + login.State
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, statusPath, nil))
				if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"wait"}` {
					t.Fatalf("unexpected pending status (%d): %s", rec.Code, rec.Body)
				}
				if tt.cancel {
					rec = httptest.NewRecorder()
					router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, base+"/session?state="+login.State, nil))
					if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancelled":true`) {
						t.Fatalf("unexpected cancel response (%d): %s", rec.Code, rec.Body)
					}
				}
				time.Sleep(time.Second)
				synctest.Wait()
				rec = httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, statusPath, nil))
				var status struct{ Status string }
				if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || rec.Code != http.StatusOK || status.Status != tt.wantStatus {
					t.Fatalf("unexpected final status (%d): %s", rec.Code, rec.Body)
				}
				data, errRead := os.ReadFile(savedPath)
				if tt.wantStatus == "error" {
					if !os.IsNotExist(errRead) {
						t.Fatalf("failed login should not save credentials: %v", errRead)
					}
					return
				}
				var metadata map[string]any
				if errRead != nil || json.Unmarshal(data, &metadata) != nil || metadata["type"] != "copilot" ||
					metadata["account_id"] != "12345" || metadata["username"] != "alice" ||
					metadata["access_token"] != "copilot-fixture" || metadata["refresh_token"] != "github-fixture" {
					t.Fatalf("Copilot credentials were not persisted correctly: %v", errRead)
				}
			})
		})
	}
}
