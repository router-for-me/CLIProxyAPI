package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

func TestAccountRoleHTTPIntegration(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "integration-legacy-admin-secret")
	server := newTestServer(t)
	cfg, err := yaml.Marshal(server.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(server.configFilePath, cfg, 0600); err != nil {
		t.Fatal(err)
	}
	// Test the same group middleware for billing, even when billing is disabled in this fixture.
	server.engine.GET("/v0/management/billing/security-probe", server.mgmt.Middleware(), func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, r)
		return w
	}
	secret, err := os.ReadFile(filepath.Join(filepath.Dir(server.configFilePath), ".management-auth", "management-admin-initial-password.txt"))
	if err != nil {
		t.Fatal(err)
	}
	login := func(username, password string) string {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"username": username, "password": password})
		w := request("POST", "/v0/management/accounts/login", "", string(b))
		if w.Code != 200 {
			t.Fatalf("login status %d", w.Code)
		}
		var data struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || data.Token == "" {
			t.Fatal("missing session token")
		}
		return data.Token
	}
	admin := login("admin", strings.TrimSpace(string(secret)))
	w := request("POST", "/v0/management/accounts/users", admin, `{"username":"operator","password":"operator-test-password-123"}`)
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	user := login("operator", "operator-test-password-123")
	for _, path := range []string{"/accounts/me", "/config", "/config.yaml", "/api-keys", "/api-key-usage", "/auth-files", "/plugins"} {
		w := request("GET", "/v0/management"+path, user, "")
		if w.Code != 200 {
			t.Errorf("user allowed %s: status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/accounts/users", "/billing/security-probe", "/quota/providers", "/quota-exceeded/switch-project", "/routing/quota-aware", "/usage-queue"} {
		w := request("GET", "/v0/management"+path, user, "")
		if w.Code != 403 {
			t.Errorf("user forbidden %s: status=%d", path, w.Code)
		}
	}
	for _, tc := range []struct{ path, body string }{
		{"/accounts/users", `{"username":"evil","password":"a-long-password-123"}`},
		{"/api-call", `{"method":"GET","url":"http://127.0.0.1:8317/v0/management/config"}`},
		{"/quota/fetch", `{}`},
	} {
		if w := request("POST", "/v0/management"+tc.path, user, tc.body); w.Code != 403 {
			t.Errorf("user POST %s status=%d", tc.path, w.Code)
		}
	}
	for _, value := range []string{"quota-aware", "quotaaware", "qa"} {
		if w := request("PATCH", "/v0/management/routing/strategy", user, `{"value":"`+value+`"}`); w.Code != 403 {
			t.Errorf("quota strategy alias %s status=%d", value, w.Code)
		}
	}
	// A user token cannot be upgraded by supplying an admin role/header.
	r := httptest.NewRequest("GET", "/v0/management/accounts/users", nil)
	r.Header.Set("Authorization", "Bearer "+user)
	r.Header.Set("X-Management-Key", "integration-legacy-admin-secret")
	r.Header.Set("X-Role", "admin")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, r)
	if rr.Code != 403 {
		t.Errorf("header spoof status=%d", rr.Code)
	}
	for _, token := range []string{admin, "integration-legacy-admin-secret"} {
		if w := request("GET", "/v0/management/billing/security-probe", token, ""); w.Code != 200 {
			t.Errorf("admin billing status=%d", w.Code)
		}
	}
	w = request("PUT", "/v0/management/accounts/password", user, `{"current_password":"operator-test-password-123","new_password":"changed-user-password-456"}`)
	if w.Code != 200 && w.Code != 204 {
		t.Fatalf("change password status=%d body=%s", w.Code, w.Body.String())
	}
	if w = request("GET", "/v0/management/accounts/me", user, ""); w.Code != 401 {
		t.Fatalf("revoked user session status=%d", w.Code)
	}
	user = login("operator", "changed-user-password-456")
	w = request("PATCH", "/v0/management/accounts/users/operator", admin, `{"disabled":true}`)
	if w.Code != 200 {
		t.Fatalf("disable status=%d", w.Code)
	}
	if w = request("GET", "/v0/management/config", user, ""); w.Code != 401 {
		t.Fatalf("disabled user status=%d", w.Code)
	}
	w = request("POST", "/v0/management/accounts/logout", admin, `{}`)
	if w.Code != 200 && w.Code != 204 {
		t.Fatalf("logout status=%d", w.Code)
	}
	if w = request("GET", "/v0/management/accounts/me", admin, ""); w.Code != 401 {
		t.Fatalf("logged-out admin status=%d", w.Code)
	}
	persisted, err := os.ReadFile(filepath.Join(filepath.Dir(server.configFilePath), ".management-auth", "management-accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, secret) || bytes.Contains(persisted, []byte("changed-user-password-456")) {
		t.Fatal("plaintext password in account database")
	}
}
