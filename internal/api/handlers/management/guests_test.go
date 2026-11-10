package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func guestTestHandler(t *testing.T, raw string) (*Handler, *gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if raw == "" {
		raw = "port: 8317\napi-keys: [client]\n"
	}
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		cfg:            cfg,
		configFilePath: path,
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "master",
	}
	router := gin.New()
	v8 := router.Group("/v8/management")
	v8.Use(h.Middleware(), func(c *gin.Context) { c.Set(ConfigV8ContextKey, true) })
	v8.GET("/session", h.GetSession)
	v8.GET("/guests", h.ListGuests)
	v8.GET("/guests/routes", h.ListGuestRoutes)
	v8.POST("/guests", h.CreateGuest)
	v8.PATCH("/guests/:id", h.PatchGuest)
	v8.POST("/guests/:id/rotate", h.RotateGuestKey)
	v8.DELETE("/guests/:id", h.DeleteGuest)
	v8.GET("/config", h.ConfigV8)
	v8.PATCH("/config", h.ConfigV8)
	v8.GET("/config.yaml", h.ConfigV8)
	v8.GET("/observability/logs", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	v8.DELETE("/observability/logs", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	v8.GET("/credentials", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	return h, router, path
}

func guestDo(router *gin.Engine, key, method, url, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func createGuest(t *testing.T, router *gin.Engine, perms map[string]string) (id, key string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "dev", "permissions": perms})
	rec := guestDo(router, "master", http.MethodPost, "/v8/management/guests", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create guest: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Guest struct {
			ID string `json:"id"`
		} `json:"guest"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Key, guestKeyPrefix) {
		t.Fatalf("unexpected key %q", out.Key)
	}
	return out.Guest.ID, out.Key
}

func TestGuestRouteTable(t *testing.T) {
	g := &guestRecord{Permissions: map[string]GuestLevel{
		"logs":           GuestLevelView,
		"plugins":        GuestLevelEdit,
		"config-plugins": GuestLevelEdit,
		"config":         GuestLevelView,
		"oauth-status":   GuestLevelView,
		"oauth":          GuestLevelView,
		"credentials":    GuestLevelView,
	}}
	cases := []struct {
		method, rel string
		want        bool
	}{
		{"GET", "/observability/logs", true},
		{"DELETE", "/observability/logs", false},
		{"GET", "/observability/logs/errors", true},
		{"GET", "/plugins", true},
		{"DELETE", "/plugins/x", true},
		{"POST", "/plugins/store/x/install", false}, // plugins-store not granted
		{"PUT", "/config/plugins/configs/x", true},
		{"GET", "/config/plugins/configs/x", true}, // edit implies view
		{"GET", "/config", true},
		{"PATCH", "/config", false},
		{"GET", "/config/observability/logs/request-log", false}, // config-logging not granted
		{"GET", "/config.yaml", false},
		{"GET", "/credentials", true},
		{"POST", "/credentials", false},
		{"GET", "/credentials/download", false}, // own group
		{"GET", "/oauth/status", true},
		{"GET", "/oauth/auth-url", false}, // starting a flow needs oauth edit
		{"GET", "/guests", false},
		{"GET", "/guests/routes", false},
		{"POST", "/guests", false},
		{"POST", "/requests/api-call", false},
		{"GET", "/unknown/route", false},
		{"GET", "/server/latest-version", true},
		{"POST", "/server/latest-version", false},
	}
	for _, tc := range cases {
		if got := guestAllowed(g, tc.method, tc.rel); got != tc.want {
			t.Errorf("%s %s: got %v want %v", tc.method, tc.rel, got, tc.want)
		}
	}

	none := &guestRecord{}
	if guestAllowed(none, "GET", "/config") {
		t.Error("a guest without grants must not read the config")
	}
}

func TestGuestRouteGroupsHaveUniquePaths(t *testing.T) {
	seenID := map[string]bool{}
	seenPath := map[string]string{}
	for _, group := range guestRouteGroups {
		if seenID[group.ID] {
			t.Errorf("duplicate group id %q", group.ID)
		}
		seenID[group.ID] = true
		for _, path := range group.Paths {
			if other, ok := seenPath[path]; ok {
				t.Errorf("path %q in both %q and %q", path, other, group.ID)
			}
			seenPath[path] = group.ID
			if strings.Contains(group.ID, ".") {
				t.Errorf("group id %q must not contain a dot", group.ID)
			}
		}
	}
}

func TestGuestRouteCatalogEndpoint(t *testing.T) {
	_, router, _ := guestTestHandler(t, "")
	rec := guestDo(router, "master", http.MethodGet, "/v8/management/guests/routes", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"credentials-download"`) {
		t.Fatalf("catalog: %d %s", rec.Code, rec.Body.String())
	}
	_, key := createGuest(t, router, map[string]string{"logs": "edit"})
	if rec := guestDo(router, key, http.MethodGet, "/v8/management/guests/routes", ""); rec.Code != 403 {
		t.Fatalf("guest reading route catalog: %d", rec.Code)
	}
}

func TestGuestMiddlewareAndPermissionEdits(t *testing.T) {
	_, router, _ := guestTestHandler(t, "")
	id, key := createGuest(t, router, map[string]string{"logs": "view"})

	if rec := guestDo(router, key, http.MethodGet, "/v8/management/observability/logs", ""); rec.Code != 200 {
		t.Fatalf("view logs: %d %s", rec.Code, rec.Body.String())
	}
	if rec := guestDo(router, key, http.MethodDelete, "/v8/management/observability/logs", ""); rec.Code != 403 {
		t.Fatalf("delete logs with view: %d", rec.Code)
	}
	if rec := guestDo(router, key, http.MethodGet, "/v8/management/credentials", ""); rec.Code != 403 {
		t.Fatalf("credentials without access: %d", rec.Code)
	}
	if rec := guestDo(router, key, http.MethodGet, "/v8/management/guests", ""); rec.Code != 403 {
		t.Fatalf("guest listing guests: %d", rec.Code)
	}
	if rec := guestDo(router, key, http.MethodPost, "/v8/management/guests", `{"name":"x"}`); rec.Code != 403 {
		t.Fatalf("guest creating guests: %d", rec.Code)
	}

	rec := guestDo(router, key, http.MethodGet, "/v8/management/session", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"role":"guest"`) || !strings.Contains(rec.Body.String(), `"logs":"view"`) {
		t.Fatalf("guest session: %d %s", rec.Code, rec.Body.String())
	}
	rec = guestDo(router, "master", http.MethodGet, "/v8/management/session", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"role":"admin"`) {
		t.Fatalf("admin session: %d %s", rec.Code, rec.Body.String())
	}

	// Permission changes apply to the next request.
	rec = guestDo(router, "master", http.MethodPatch, "/v8/management/guests/"+id, `{"permissions":{"logs":"edit"}}`)
	if rec.Code != 200 {
		t.Fatalf("patch guest: %d %s", rec.Code, rec.Body.String())
	}
	if rec := guestDo(router, key, http.MethodDelete, "/v8/management/observability/logs", ""); rec.Code != 200 {
		t.Fatalf("delete logs after edit grant: %d", rec.Code)
	}
	rec = guestDo(router, "master", http.MethodPatch, "/v8/management/guests/"+id, `{"permissions":{"nope":"edit"}}`)
	if rec.Code != 400 {
		t.Fatalf("unknown page accepted: %d", rec.Code)
	}

	// Rotating invalidates the old key.
	rec = guestDo(router, "master", http.MethodPost, "/v8/management/guests/"+id+"/rotate", "")
	if rec.Code != 200 {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body.String())
	}
	var rotated struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &rotated)
	if rec := guestDo(router, key, http.MethodGet, "/v8/management/observability/logs", ""); rec.Code != 401 {
		t.Fatalf("old key after rotate: %d", rec.Code)
	}
	if rec := guestDo(router, rotated.Key, http.MethodGet, "/v8/management/observability/logs", ""); rec.Code != 200 {
		t.Fatalf("new key: %d", rec.Code)
	}

	rec = guestDo(router, "master", http.MethodDelete, "/v8/management/guests/"+id, "")
	if rec.Code != 200 {
		t.Fatalf("delete guest: %d", rec.Code)
	}
	if rec := guestDo(router, rotated.Key, http.MethodGet, "/v8/management/observability/logs", ""); rec.Code != 401 {
		t.Fatalf("deleted key: %d", rec.Code)
	}
}

func TestGuestKeysPersistWithoutPlaintext(t *testing.T) {
	h, router, path := guestTestHandler(t, "")
	_, key := createGuest(t, router, map[string]string{"logs": "view"})

	stored, err := os.ReadFile(filepath.Join(filepath.Dir(path), guestStoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), key) {
		t.Fatal("guest key stored in plaintext")
	}

	reloaded := &Handler{cfg: h.cfg, configFilePath: path, failedAttempts: map[string]*attemptInfo{}, envSecret: "master"}
	if reloaded.guests().lookup(key) == nil {
		t.Fatal("guest key not found after reload")
	}
}

func TestGuestKeyIgnoredByNonHTTPAuth(t *testing.T) {
	h, router, _ := guestTestHandler(t, "")
	_, key := createGuest(t, router, map[string]string{"logs": "edit"})
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, key); allowed {
		t.Fatal("guest key accepted by AuthenticateManagementKey")
	}
}

func TestGuestCannotReadOrChangeManagementSection(t *testing.T) {
	_, router, _ := guestTestHandler(t, "port: 8317\nremote-management:\n  allow-remote: false\n  secret-key: hunter2\n")
	_, key := createGuest(t, router, map[string]string{"config": "edit"})

	rec := guestDo(router, key, http.MethodGet, "/v8/management/config", "")
	if rec.Code != 200 {
		t.Fatalf("guest get config: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"management"`) || strings.Contains(rec.Body.String(), "secret-key") {
		t.Fatalf("management section leaked to guest: %s", rec.Body.String())
	}

	rec = guestDo(router, key, http.MethodPatch, "/v8/management/config", `{"management":{"allow-remote":true}}`)
	if rec.Code != 403 {
		t.Fatalf("guest changed management section: %d %s", rec.Code, rec.Body.String())
	}
	rec = guestDo(router, key, http.MethodPatch, "/v8/management/config", `{"routing":{"retry":{"request-retry":1}}}`)
	if rec.Code != 200 {
		t.Fatalf("guest ordinary config edit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := guestDo(router, key, http.MethodGet, "/v8/management/config.yaml", ""); rec.Code != 403 {
		t.Fatalf("guest read config.yaml: %d", rec.Code)
	}
	if rec := guestDo(router, "master", http.MethodGet, "/v8/management/config", ""); !strings.Contains(rec.Body.String(), "management") {
		t.Fatalf("admin lost management section: %s", rec.Body.String())
	}
}
