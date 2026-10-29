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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCachedClaudeQuotaNeverRefreshesUpstream(testContext *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	credential, err := manager.Register(context.Background(), &coreauth.Auth{ID: "claude-account", Provider: "claude", Metadata: map[string]any{"access_token": "synthetic-token"}})
	if err != nil {
		testContext.Fatal(err)
	}
	path := filepath.Join(testContext.TempDir(), "quota.json")
	observed := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	writeSnapshot := func(timestamp time.Time) {
		snapshot, err := json.Marshal(map[string]any{
			"observed_at": time.Now().UTC(),
			"accounts": map[string]any{credential.ID: map[string]any{
				"observed_at":            timestamp,
				"auth_index":             credential.Index,
				"credential_fingerprint": coreauth.AccessTokenSHA256(credential),
				"eligible":               true,
				"usage":                  map[string]any{"five_hour": map[string]any{"utilization": 12, "resets_at": nil}},
			}},
		})
		if err != nil {
			testContext.Fatal(err)
		}
		if err := os.WriteFile(path, snapshot, 0600); err != nil {
			testContext.Fatal(err)
		}
	}
	writeSnapshot(observed)
	handler := &Handler{cfg: &config.Config{Routing: config.RoutingConfig{ClaudeQuotaSnapshot: path}}, authManager: manager}
	router := gin.New()
	router.GET("/quota", handler.GetCachedClaudeQuota)
	router.GET("/quota/credentials", handler.GetClaudeQuotaCredentials)
	credentialResponse := httptest.NewRecorder()
	router.ServeHTTP(credentialResponse, httptest.NewRequest(http.MethodGet, "/quota/credentials", nil))
	var credentials struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(credentialResponse.Body.Bytes(), &credentials); err != nil || len(credentials.Files) != 1 || credentials.Files[0]["credential_fingerprint"] != coreauth.AccessTokenSHA256(credential) {
		testContext.Fatalf("credentials=%+v error=%v", credentials, err)
	}
	if strings.Contains(credentialResponse.Body.String(), "synthetic-token") {
		testContext.Fatal("credential token leaked in quota identity listing")
	}
	for _, test := range []struct {
		query string
		want  int
	}{
		{"", http.StatusBadRequest},
		{"?auth_index=unknown", http.StatusNotFound},
		{"?auth_index=" + credential.Index, http.StatusOK},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/quota"+test.query, nil))
		if recorder.Code != test.want {
			testContext.Fatalf("status=%d want=%d body=%s", recorder.Code, test.want, recorder.Body.String())
		}
		if test.want == http.StatusOK {
			var response struct {
				ObservedAt time.Time      `json:"observed_at"`
				Usage      map[string]any `json:"usage"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || !response.ObservedAt.Equal(observed) || response.Usage["five_hour"] == nil {
				testContext.Fatalf("response=%+v error=%v", response, err)
			}
		}
	}
	writeSnapshot(time.Now().Add(-31 * time.Minute))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/quota?auth_index="+credential.Index, nil))
	if recorder.Code != http.StatusServiceUnavailable {
		testContext.Fatalf("expired cache status=%d", recorder.Code)
	}
}
