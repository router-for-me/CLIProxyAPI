package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/multiuser"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestKiroAccountManagerUploadAndFirstQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, hub := range []bool{false, true} {
		name := "management"
		if hub {
			name = "hub"
		}
		t.Run(name, func(t *testing.T) {
			const profile = "arn:aws:codewhisperer:eu-central-1:123:profile/test"
			var refreshes, profiles, quotas atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/refreshToken":
					refreshes.Add(1)
					if r.Host != "prod.us-east-1.auth.desktop.kiro.dev" {
						t.Errorf("refresh host: %s", r.Host)
					}
					if !strings.HasPrefix(r.Header.Get("User-Agent"), "KiroIDE-0.6.18-") {
						t.Errorf("refresh user agent does not mimic the IDE: %q", r.Header.Get("User-Agent"))
					}
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["refreshToken"] != "fixture-refresh" {
						t.Error("refresh token was not imported")
					}
					_, _ = io.WriteString(w, `{"accessToken":"fixture-access","refreshToken":"fixture-rotated","expiresIn":3600}`)
				case r.Header.Get("X-Amz-Target") == "KiroControlPlaneBearerService.ListAvailableProfiles":
					profiles.Add(1)
					if r.Host != "management.us-east-1.kiro.dev" || r.Header.Get("Authorization") != "Bearer fixture-access" {
						t.Errorf("profile request: %s", r.Host)
					}
					if r.Header.Get("x-amz-user-agent") != "aws-sdk-js/1.0.0" || r.Header.Get("amz-sdk-invocation-id") == "" {
						t.Errorf("profile request headers do not mimic the IDE client")
					}
					_, _ = io.WriteString(w, `{"profiles":[{"arn":"`+profile+`"}]}`)
				case r.URL.Path == "/getUsageLimits":
					quotas.Add(1)
					if r.Host != "management.eu-central-1.kiro.dev" || r.URL.Query().Get("profileArn") != profile || r.URL.Query().Get("resourceType") != "AGENTIC_REQUEST" || r.URL.Query().Get("isEmailRequired") != "true" || r.Header.Get("Authorization") != "Bearer fixture-access" {
						t.Errorf("quota used stale metadata: %s %s", r.Host, r.URL.String())
					}
					if !strings.HasPrefix(r.Header.Get("User-Agent"), "aws-sdk-js/1.0.0 ua/2.1 os/") || !strings.Contains(r.Header.Get("User-Agent"), "api/codewhispererruntime#1.0.0 m/N,E KiroIDE-") || !strings.HasPrefix(r.Header.Get("x-amz-user-agent"), "aws-sdk-js/1.0.0 KiroIDE-") || r.Header.Get("amz-sdk-invocation-id") == "" || r.Header.Get("amz-sdk-request") != "attempt=1; max=1" {
						t.Errorf("quota request headers do not mimic the IDE client")
					}
					_, _ = io.WriteString(w, `{"usageBreakdownList":[]}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Host, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer upstream.Close()
			transport := upstream.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig.ServerName = "127.0.0.1"
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			original := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })

			root := t.TempDir()
			authDir := filepath.Join(root, "auths")
			if err := os.MkdirAll(authDir, 0o700); err != nil {
				t.Fatal(err)
			}
			store, err := multiuser.Open(filepath.Join(root, "hub"), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			user, err := store.Register("kiro_user", "fixture-password-2026", "")
			if err != nil {
				t.Fatal(err)
			}
			channel, err := store.CreateChannel(user, "kiro", false)
			if err != nil {
				t.Fatal(err)
			}
			files := sdkauth.NewFileTokenStore()
			files.SetBaseDir(authDir)
			manager := coreauth.NewManager(files, nil, nil)
			h := NewHandler(&config.Config{AuthDir: authDir}, "", manager)
			h.tokenStore = files
			h.SetHubStore(store)
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("hubUser", user) })
			uploadURL := "/upload?name=github.json"
			if hub {
				router.POST("/upload", h.HubUploadCredential)
				uploadURL += "&channel_id=" + channel.ID
			} else {
				router.POST("/upload", h.UploadAuthFile)
			}
			router.POST("/quota", h.APICall)
			router.POST("/credentials/:id/api-call", h.HubQuotaAPICall)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, uploadURL, strings.NewReader(`{"email":"user@example.com","provider":"Github","refreshToken":"fixture-refresh"}`))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
				t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
			}
			auths := manager.List()
			if len(auths) != 1 || auths[0].Provider != "kiro" || auths[0].Metadata["auth_method"] != "social" || auths[0].Metadata["refresh_token"] != "fixture-refresh" {
				t.Fatal("uploaded export was not registered as a social Kiro credential")
			}
			if hub && (auths[0].Prefix != "kiro-kiro#1" || auths[0].Owner() != user.Username || auths[0].ChannelID() != channel.ID) {
				t.Fatal("Kiro import lost channel ownership or routing prefix")
			}
			auth := auths[0]
			endpoint := "/quota"
			if hub {
				endpoint = "/credentials/" + base64.RawURLEncoding.EncodeToString([]byte(auth.ID)) + "/api-call"
			}
			body, _ := json.Marshal(map[string]any{"authIndex": auth.EnsureIndex(), "method": "GET", "url": "https://management.us-east-1.kiro.dev/getUsageLimits", "header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
			for range 2 {
				rec = httptest.NewRecorder()
				req = httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status_code":200`) {
					t.Fatalf("quota: %d %s", rec.Code, rec.Body.String())
				}
			}
			if refreshes.Load() != 1 || profiles.Load() != 1 || quotas.Load() != 2 {
				t.Fatalf("requests: refresh=%d profile=%d quota=%d", refreshes.Load(), profiles.Load(), quotas.Load())
			}
			saved, err := files.List(context.Background())
			if err != nil || len(saved) != 1 || saved[0].Metadata["refresh_token"] != "fixture-rotated" || saved[0].Metadata["profile_arn"] != profile {
				t.Fatal("refreshed token and resolved profile were not persisted")
			}
		})
	}
}
