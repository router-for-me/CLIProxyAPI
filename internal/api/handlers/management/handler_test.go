package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"golang.org/x/crypto/bcrypt"
)

func TestAuthenticateManagementKey_LocalhostIPBan_BlocksCorrectKeyDuringBan(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}

	for i := 0; i < 5; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed {
			t.Fatalf("expected auth to be denied at attempt %d", i+1)
		}
		if statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("unexpected auth failure at attempt %d: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}

	allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if allowed {
		t.Fatalf("expected correct key to be denied while banned")
	}
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected forbidden status while banned, got %d", statusCode)
	}
	if !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("unexpected banned message: %q", errMsg)
	}
}

func TestMiddlewareSetsSupportPluginHeader(t *testing.T) {

	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	middleware := h.Middleware()

	t.Run("invalid key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		c.Request.RemoteAddr = "127.0.0.1:12345"
		c.Request.Header.Set("X-Management-Key", "wrong-secret")

		middleware(c)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if got := rec.Header().Get("X-CPA-SUPPORT-PLUGIN"); got != pluginhost.SupportPluginHeaderValue() {
			t.Fatalf("X-CPA-SUPPORT-PLUGIN = %q, want %q", got, pluginhost.SupportPluginHeaderValue())
		}
	})

	t.Run("valid key", func(t *testing.T) {
		engine := gin.New()
		engine.GET("/v0/management/config", middleware, func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Management-Key", "test-secret")
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("X-CPA-SUPPORT-PLUGIN"); got != pluginhost.SupportPluginHeaderValue() {
			t.Fatalf("X-CPA-SUPPORT-PLUGIN = %q, want %q", got, pluginhost.SupportPluginHeaderValue())
		}
	})
}

func TestAuthenticateManagementKey_ReusesVerifiedSecretWithinOneHourTTL(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.DefaultCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var nowMu sync.Mutex
	var compareCalls atomic.Int64
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		authCache: managementAuthCache{
			nowFunc: func() time.Time {
				nowMu.Lock()
				defer nowMu.Unlock()
				return now
			},
			bcryptCompare: func(hashedPassword, password []byte) error {
				compareCalls.Add(1)
				return bcrypt.CompareHashAndPassword(hashedPassword, password)
			},
		},
	}

	if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatalf("initial auth failed: status=%d msg=%q", statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("initial bcrypt calls = %d, want 1", got)
	}

	for i := 0; i < 10; i++ {
		if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
			t.Fatalf("cached auth %d failed: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("bcrypt calls after repeated polling = %d, want 1", got)
	}

	nowMu.Lock()
	now = now.Add(time.Hour - time.Second)
	nowMu.Unlock()
	if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatalf("auth just before 1h expiry failed: status=%d msg=%q", statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("bcrypt calls before 1h expiry = %d, want 1", got)
	}

	nowMu.Lock()
	now = now.Add(time.Second)
	nowMu.Unlock()
	if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatalf("auth at 1h expiry failed: status=%d msg=%q", statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 2 {
		t.Fatalf("bcrypt calls at 1h expiry = %d, want 2", got)
	}

	if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatalf("auth after re-verification failed: status=%d msg=%q", statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 2 {
		t.Fatalf("bcrypt calls after refresh = %d, want 2", got)
	}
}

func TestAuthenticateManagementKey_NeverCachesFailuresAndPreservesIPBan(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	var compareCalls atomic.Int64
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		authCache: managementAuthCache{
			bcryptCompare: func(hashedPassword, password []byte) error {
				compareCalls.Add(1)
				return bcrypt.CompareHashAndPassword(hashedPassword, password)
			},
		},
	}

	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatal("expected valid secret to succeed")
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("bcrypt calls = %d, want 1", got)
	}

	// Four wrong attempts must each invoke bcrypt (never negative-cached) and not ban yet.
	for i := 0; i < 4; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed || statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("wrong attempt %d = (%v, %d, %q), want (false, 401, invalid management key)", i+1, allowed, statusCode, errMsg)
		}
	}
	if got := compareCalls.Load(); got != 5 {
		t.Fatalf("bcrypt calls after 4 failures = %d, want 5", got)
	}

	// A cached hit must reset the failed-attempt counter.
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatal("expected cached valid secret to succeed and reset attempts")
	}
	if got := compareCalls.Load(); got != 5 {
		t.Fatalf("bcrypt calls on cached hit = %d, want 5", got)
	}

	// Five consecutive failures after reset trigger the 30-minute IP ban.
	for i := 0; i < 5; i++ {
		allowed, statusCode, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed || statusCode != http.StatusUnauthorized {
			t.Fatalf("post-reset failure %d = (%v, %d), want (false, 401)", i+1, allowed, statusCode)
		}
	}
	if got := compareCalls.Load(); got != 10 {
		t.Fatalf("bcrypt calls after 5 failures = %d, want 10", got)
	}

	// Even with the valid secret already cached, the banned IP must still receive 403.
	allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if allowed || statusCode != http.StatusForbidden || !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("banned auth with cached key = (%v, %d, %q), want (false, 403, IP banned...)", allowed, statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 10 {
		t.Fatalf("banned request should short-circuit before bcrypt; calls = %d, want 10", got)
	}
}

func TestAuthenticateManagementKey_InvalidatesCacheOnSecretChangeAndRemoval(t *testing.T) {
	hash1, errHash1 := bcrypt.GenerateFromPassword([]byte("secret-one"), bcrypt.MinCost)
	if errHash1 != nil {
		t.Fatalf("bcrypt.GenerateFromPassword(1) error = %v", errHash1)
	}
	hash2, errHash2 := bcrypt.GenerateFromPassword([]byte("secret-two"), bcrypt.MinCost)
	if errHash2 != nil {
		t.Fatalf("bcrypt.GenerateFromPassword(2) error = %v", errHash2)
	}
	var compareCalls atomic.Int64
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash1),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		authCache: managementAuthCache{
			bcryptCompare: func(hashedPassword, password []byte) error {
				compareCalls.Add(1)
				return bcrypt.CompareHashAndPassword(hashedPassword, password)
			},
		},
	}

	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); !allowed {
		t.Fatal("expected secret-one to succeed")
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); !allowed {
		t.Fatal("expected cached secret-one to succeed")
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("bcrypt calls before rotation = %d, want 1", got)
	}

	// Rotate via SetConfig.
	h.SetConfig(&config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: string(hash2),
		},
	})
	if allowed, statusCode, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); allowed || statusCode != http.StatusUnauthorized {
		t.Fatalf("stale secret-one after SetConfig = (%v, %d), want (false, 401)", allowed, statusCode)
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-two"); !allowed {
		t.Fatal("expected secret-two to succeed after rotation")
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-two"); !allowed {
		t.Fatal("expected cached secret-two to succeed")
	}
	if got := compareCalls.Load(); got != 3 {
		t.Fatalf("bcrypt calls after rotation = %d, want 3", got)
	}

	// Rotating back to hash1 must not reuse pre-rotation cached entry.
	h.SetConfig(&config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: string(hash1),
		},
	})
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); !allowed {
		t.Fatal("expected secret-one to succeed after rotating back")
	}
	if got := compareCalls.Load(); got != 4 {
		t.Fatalf("bcrypt calls after rotating back = %d, want 4", got)
	}

	// Direct h.cfg swap (e.g. PutConfigYAML) still misses because digest includes secretHash.
	h.mu.Lock()
	h.cfg = &config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: string(hash2),
		},
	}
	h.mu.Unlock()
	if allowed, statusCode, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); allowed || statusCode != http.StatusUnauthorized {
		t.Fatalf("stale secret-one after direct cfg swap = (%v, %d), want (false, 401)", allowed, statusCode)
	}

	// Clearing secret-key denies further requests immediately.
	h.SetConfig(&config.Config{})
	allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "secret-two")
	if allowed || statusCode != http.StatusForbidden || errMsg != "remote management key not set" {
		t.Fatalf("auth after secret removal = (%v, %d, %q), want (false, 403, remote management key not set)", allowed, statusCode, errMsg)
	}
}

func TestAuthenticateManagementKey_CoalescesConcurrentColdCacheLookups(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	var compareCalls atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		authCache: managementAuthCache{
			bcryptCompare: func(hashedPassword, password []byte) error {
				compareCalls.Add(1)
				if string(password) == "test-secret" {
					select {
					case started <- struct{}{}:
					default:
					}
					<-release
				}
				return bcrypt.CompareHashAndPassword(hashedPassword, password)
			},
		},
	}

	const workers = 8
	var wg sync.WaitGroup
	results := make([]bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
			results[idx] = allowed
		}(i)
	}

	<-started
	// A different/wrong secret runs independently without blocking on or succeeding from the valid lookup.
	wrongAllowed, wrongStatus, _ := h.AuthenticateManagementKey("127.0.0.2", true, "wrong-secret")
	if wrongAllowed || wrongStatus != http.StatusUnauthorized {
		t.Fatalf("concurrent wrong secret = (%v, %d), want (false, 401)", wrongAllowed, wrongStatus)
	}

	close(release)
	wg.Wait()

	for i, allowed := range results {
		if !allowed {
			t.Fatalf("worker %d failed", i)
		}
	}
	// 1 coalesced lookup for "test-secret" + 1 lookup for "wrong-secret".
	if got := compareCalls.Load(); got != 2 {
		t.Fatalf("bcrypt calls = %d, want 2", got)
	}
}

func TestManagementAuthCache_BoundsEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache := managementAuthCache{
		nowFunc: func() time.Time { return now },
		bcryptCompare: func(_, _ []byte) error {
			return nil
		},
	}
	for i := 0; i < managementAuthCacheMaxEntries+16; i++ {
		now = now.Add(time.Second)
		if !cache.verify(fmt.Sprintf("hash-%d", i), "secret") {
			t.Fatalf("verify(%d) = false, want true", i)
		}
	}
	cache.mu.Lock()
	entryCount := len(cache.entries)
	cache.mu.Unlock()
	if entryCount > managementAuthCacheMaxEntries {
		t.Fatalf("cache entries = %d, want <= %d", entryCount, managementAuthCacheMaxEntries)
	}
}

func TestMiddleware_AuthenticatesRealBcryptHashAndInvalidatesOnConfigV8Update(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hash1, errHash1 := bcrypt.GenerateFromPassword([]byte("key-one"), bcrypt.DefaultCost)
	if errHash1 != nil {
		t.Fatalf("bcrypt.GenerateFromPassword(1) error = %v", errHash1)
	}
	hash2, errHash2 := bcrypt.GenerateFromPassword([]byte("key-two"), bcrypt.MinCost)
	if errHash2 != nil {
		t.Fatalf("bcrypt.GenerateFromPassword(2) error = %v", errHash2)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := fmt.Sprintf("config-version: 8\nmanagement:\n  secret-key: %q\n", string(hash1))
	if errWrite := os.WriteFile(path, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}

	h := &Handler{cfg: cfg, configFilePath: path, failedAttempts: make(map[string]*attemptInfo)}
	router := gin.New()
	v8 := router.Group("/v8/management", h.Middleware())
	v8.GET("/config/*path", h.ConfigV8)
	v8.PUT("/config/*path", h.ConfigV8)

	doReq := func(method, url, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		router.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 3; i++ {
		if rec := doReq(http.MethodGet, "/v8/management/config/management", "key-one", ""); rec.Code != http.StatusOK {
			t.Fatalf("GET #%d status = %d, want 200; body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	updateBody := fmt.Sprintf("%q", string(hash2))
	if rec := doReq(http.MethodPut, "/v8/management/config/management/secret-key", "key-one", updateBody); rec.Code != http.StatusOK {
		t.Fatalf("PUT secret-key status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	if rec := doReq(http.MethodGet, "/v8/management/config/management", "key-one", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET with old key after ConfigV8 update status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	if rec := doReq(http.MethodGet, "/v8/management/config/management", "key-two", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET with new key after ConfigV8 update status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func BenchmarkAuthenticateManagementKey_Cached(b *testing.B) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.DefaultCost)
	if errHash != nil {
		b.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		b.Fatal("warmup auth failed")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
			b.Fatal("cached auth failed")
		}
	}
}
