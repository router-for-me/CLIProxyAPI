package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementauth"
)

func TestAccountHandlersLoginMeLogoutNoSecretExposure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, store, adminPassword := newAccountHandlerTest(t)
	r := accountRouter(h, store)

	loginBody := `{"username":"admin","password":` + quoteJSON(adminPassword) + `}`
	w := performJSON(r, http.MethodPost, "/accounts/login", loginBody, "")
	if w.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control=%q", cc)
	}
	if strings.Contains(w.Body.String(), adminPassword) {
		t.Fatalf("login response exposed password")
	}
	var login struct {
		Token     string              `json:"token"`
		ExpiresAt string              `json:"expires_at"`
		User      managementauth.User `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if login.Token == "" || login.ExpiresAt == "" || login.User.Username != managementauth.AdminUsername || login.User.Role != managementauth.RoleAdmin {
		t.Fatalf("unexpected login response: %+v", login)
	}

	w = performJSON(r, http.MethodGet, "/accounts/me", ``, login.Token)
	if w.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), adminPassword) {
		t.Fatalf("me response exposed secret: %s", w.Body.String())
	}

	w = performJSON(r, http.MethodPost, "/accounts/logout", `{}`, login.Token)
	if w.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", w.Code, w.Body.String())
	}
	w = performJSON(r, http.MethodGet, "/accounts/me", ``, login.Token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAccountHandlersAdminUserLifecycleAndRevocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, store, adminPassword := newAccountHandlerTest(t)
	r := accountRouter(h, store)
	adminToken := loginAccount(t, r, "admin", adminPassword)

	w := performJSON(r, http.MethodPost, "/accounts/users", `{"username":"alice","password":"pw1"}`, adminToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "pw1") || strings.Contains(w.Body.String(), "password_hash") {
		t.Fatalf("create response exposed secret: %s", w.Body.String())
	}

	aliceToken := loginAccount(t, r, "alice", "pw1")
	w = performJSON(r, http.MethodGet, "/accounts/users", ``, aliceToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin list status=%d body=%s", w.Code, w.Body.String())
	}

	w = performJSON(r, http.MethodPut, "/accounts/password", `{"current_password":"wrong","new_password":"pw2"}`, aliceToken)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password change status=%d body=%s", w.Code, w.Body.String())
	}
	w = performJSON(r, http.MethodPut, "/accounts/password", `{"current_password":"pw1","new_password":"pw2"}`, aliceToken)
	if w.Code != http.StatusOK {
		t.Fatalf("password change status=%d body=%s", w.Code, w.Body.String())
	}
	w = performJSON(r, http.MethodGet, "/accounts/me", ``, aliceToken)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old user token status=%d body=%s", w.Code, w.Body.String())
	}

	aliceToken = loginAccount(t, r, "alice", "pw2")
	w = performJSON(r, http.MethodPatch, "/accounts/users/admin", `{"disabled":true}`, adminToken)
	if w.Code != http.StatusForbidden {
		t.Fatalf("disable admin status=%d body=%s", w.Code, w.Body.String())
	}
	w = performJSON(r, http.MethodPatch, "/accounts/users/alice", `{"disabled":true}`, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("disable alice status=%d body=%s", w.Code, w.Body.String())
	}
	w = performJSON(r, http.MethodGet, "/accounts/me", ``, aliceToken)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled token status=%d body=%s", w.Code, w.Body.String())
	}

	w = performJSON(r, http.MethodGet, "/accounts/users", ``, adminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "pw2") || strings.Contains(w.Body.String(), "password_hash") {
		t.Fatalf("list response exposed secret: %s", w.Body.String())
	}
}

func TestAccountLoginRemotePolicyAndGenericFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _, adminPassword := newAccountHandlerTest(t)
	r := accountRouter(h, nil)

	body := `{"username":"admin","password":` + quoteJSON(adminPassword) + `}`
	w := performRemoteJSON(r, http.MethodPost, "/accounts/login", body, "", "203.0.113.5:1234")
	if w.Code != http.StatusForbidden {
		t.Fatalf("remote denied status=%d body=%s", w.Code, w.Body.String())
	}
	h.cfg.RemoteManagement.AllowRemote = true
	w = performRemoteJSON(r, http.MethodPost, "/accounts/login", `{"username":"missing","password":"bad"}`, "", "203.0.113.5:1234")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "missing") || strings.Contains(w.Body.String(), "disabled") {
		t.Fatalf("login failure enumerated account state: %s", w.Body.String())
	}
}

func newAccountHandlerTest(t *testing.T) (*Handler, *managementauth.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := managementauth.Open(filepath.Join(dir, "management-accounts.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	passwordBytes, err := osReadFile(filepath.Join(dir, managementauth.BootstrapFile))
	if err != nil {
		t.Fatalf("read bootstrap: %v", err)
	}
	h := &Handler{cfg: &config.Config{}}
	attachManagementAccountStoreForTest(h, store)
	return h, store, strings.TrimSpace(string(passwordBytes))
}

func accountRouter(h *Handler, store *managementauth.Store) *gin.Engine {
	r := gin.New()
	r.POST("/accounts/login", h.AccountLogin)
	protected := r.Group("/accounts")
	protected.Use(func(c *gin.Context) {
		if store == nil {
			store = h.managementAccountStore()
		}
		if user, ok := store.Authenticate(bearerToken(c)); ok {
			c.Set("management_account", user)
		}
		c.Next()
	})
	protected.GET("/me", h.AccountMe)
	protected.POST("/logout", h.AccountLogout)
	protected.PUT("/password", h.AccountPassword)
	protected.GET("/users", h.AccountUsers)
	protected.POST("/users", h.AccountCreateUser)
	protected.PATCH("/users/:username", h.AccountUpdateUser)
	return r
}

func loginAccount(t *testing.T, r http.Handler, username, password string) string {
	t.Helper()
	w := performJSON(r, http.MethodPost, "/accounts/login", `{"username":`+quoteJSON(username)+`,"password":`+quoteJSON(password)+`}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("login %s status=%d body=%s", username, w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if resp.Token == "" {
		t.Fatalf("empty login token")
	}
	return resp.Token
}

func performJSON(r http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	return performRemoteJSON(r, method, path, body, token, "127.0.0.1:12345")
}

func performRemoteJSON(r http.Handler, method, path, body, token, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.RemoteAddr = remoteAddr
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var osReadFile = func(name string) ([]byte, error) { return os.ReadFile(name) }

func attachManagementAccountStoreForTest(h *Handler, s *managementauth.Store) {
	if h != nil {
		h.accounts = s
	}
}

func TestAccountLoginRejectsSpoofedForwardingHeaders(t *testing.T) {
	h, store, password := newAccountHandlerTest(t)
	h.allowRemoteOverride = false
	h.cfg.RemoteManagement.AllowRemote = false
	router := accountRouter(h, store)
	req := httptest.NewRequest(http.MethodPost, "/accounts/login", strings.NewReader(`{"username":"admin","password":`+quoteJSON(password)+`}`))
	req.RemoteAddr = "203.0.113.9:23456"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("spoofed peer bypassed remote policy: %d", w.Code)
	}
}
