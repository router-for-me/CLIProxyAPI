package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	management "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
)

func TestDevinOAuthRoutes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)

	w := httptest.NewRecorder()
	server.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v0/management/devin-auth-url", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected login route: %d", w.Code)
	}
	registered := false
	for _, route := range server.engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/v0/management/devin-auth-url" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("Devin login route not registered")
	}

	for _, test := range []struct {
		name, provider, query, callbackPath string
		want                                int
	}{
		{name: "success", provider: "devin", query: "code=test-code", callbackPath: "/callback", want: http.StatusOK},
		{name: "success legacy path", provider: "devin", query: "code=test-code", callbackPath: "/devin/callback", want: http.StatusOK},
		{name: "denied", provider: "devin", query: "error=access_denied", callbackPath: "/callback", want: http.StatusOK},
		{name: "wrong provider", provider: "codex", query: "code=test-code", callbackPath: "/callback", want: http.StatusBadRequest},
		{name: "missing code", provider: "devin", callbackPath: "/callback", want: http.StatusBadRequest},
		{name: "unknown state", query: "code=test-code", callbackPath: "/callback", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := "devin-route-" + strings.ReplaceAll(test.name, " ", "-")
			if test.provider != "" {
				management.RegisterOAuthSession(state, test.provider)
				defer management.CompleteOAuthSession(state)
			}
			w := httptest.NewRecorder()
			path := test.callbackPath
			if path == "" {
				path = "/callback"
			}
			server.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?state="+state+"&"+test.query, nil))
			if w.Code != test.want {
				t.Fatalf("callback: %d %s", w.Code, w.Body.String())
			}
			filePath := filepath.Join(server.cfg.AuthDir, ".oauth-devin-"+state+".oauth")
			data, errRead := os.ReadFile(filePath)
			if test.want == http.StatusOK {
				if errRead != nil {
					t.Fatal(errRead)
				}
				var payload map[string]string
				if errDecode := json.Unmarshal(data, &payload); errDecode != nil {
					t.Fatal(errDecode)
				}
				if payload["state"] != state || (payload["code"] == "" && payload["error"] == "") {
					t.Fatalf("callback payload: %v", payload)
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("callback response is cacheable")
				}
			} else if !os.IsNotExist(errRead) {
				t.Fatalf("invalid callback persisted: %v", errRead)
			}
		})
	}
}
