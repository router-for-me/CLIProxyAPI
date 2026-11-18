package musecode

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newTestRouter(handler *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Any("/muse-code/*path", handler.Handle)
	return engine
}

func registerMetaTestCred(t *testing.T, manager *auth.Manager, cred *auth.Auth, models ...string) {
	t.Helper()
	if len(models) > 0 {
		infos := make([]*registry.ModelInfo, 0, len(models))
		for _, id := range models {
			infos = append(infos, &registry.ModelInfo{ID: id})
		}
		registry.GetGlobalRegistry().RegisterClient(cred.ID, "meta", infos)
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(cred.ID)
		})
	}
	if _, errReg := manager.Register(context.Background(), cred); errReg != nil {
		t.Fatalf("register cred %s: %v", cred.ID, errReg)
	}
}

func TestRelayHeadersQueryAndDisallowedPaths(t *testing.T) {
	var upstreamCalls atomic.Int32
	var gotHeaders http.Header
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		gotHeaders = r.Header.Clone()
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("Etag", `"v3"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.Header().Set("Retry-After", "12")
		w.Header().Set("Set-Cookie", "secret=cookie")
		w.Header().Set("X-Internal-Leak", "nope")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	cred := &auth.Auth{
		ID:       "meta-headers-test",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "meta-secret",
			"base_url": upstream.URL + "/api/v1",
		},
	}
	registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")

	router := newTestRouter(NewHandler(manager, cfg))

	// Disallowed endpoints must return 404 without calling upstream.
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/muse-code/key"},
		{http.MethodGet, "/muse-code/key"},
		{http.MethodPost, "/muse-code/telemetry"},
		{http.MethodPost, "/muse-code/models"},
		{http.MethodGet, "/muse-code/search"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("disallowed endpoints reached upstream %d times", upstreamCalls.Load())
	}

	// Allowed request strips proxy auth query params & unsafe headers.
	req := httptest.NewRequest(http.MethodGet, "/muse-code/config?channel=public&key=proxy-secret&auth_token=proxy-tok&extra=1", nil)
	req.Header.Set("Authorization", "Bearer client-proxy-secret")
	req.Header.Set("Cookie", "session=bad")
	req.Header.Set("X-Api-Key", "proxy-key")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("If-None-Match", `"v2"`)
	req.Header.Set("X-Tbh-Custom", "tbh-val")
	req.Header.Set("X-Meta-Ai-Custom", "meta-val")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if gotQuery != "channel=public&extra=1" {
		t.Fatalf("upstream query = %q, want channel=public&extra=1", gotQuery)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer meta-secret" {
		t.Fatalf("upstream Authorization = %q, want Bearer meta-secret", got)
	}
	if got := gotHeaders.Get("Cookie"); got != "" {
		t.Fatalf("upstream leaked Cookie = %q", got)
	}
	if got := gotHeaders.Get("X-Api-Key"); got != "" {
		t.Fatalf("upstream leaked X-Api-Key = %q", got)
	}
	if gotHeaders.Get("Traceparent") == "" || gotHeaders.Get("If-None-Match") != `"v2"` ||
		gotHeaders.Get("X-Tbh-Custom") != "tbh-val" || gotHeaders.Get("X-Meta-Ai-Custom") != "meta-val" {
		t.Fatalf("missing allowlisted request headers: %#v", gotHeaders)
	}
	if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("X-Internal-Leak") != "" {
		t.Fatalf("response leaked disallowed headers: %#v", rec.Header())
	}
	if rec.Header().Get("Cache-Control") != "max-age=60" || rec.Header().Get("Etag") != `"v3"` ||
		rec.Header().Get("Last-Modified") == "" || rec.Header().Get("Retry-After") != "12" {
		t.Fatalf("missing allowlisted response headers: %#v", rec.Header())
	}
}

func TestRelayBodySizeCaps(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxBodySize+1))
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	cred := &auth.Auth{
		ID:       "meta-size-test",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "meta-secret",
			"base_url": upstream.URL + "/v1",
		},
	}
	registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")
	router := newTestRouter(NewHandler(manager, cfg))

	// Oversized request returns 413 without calling upstream.
	oversizedReq := httptest.NewRequest(http.MethodPost, "/muse-code/search", bytes.NewReader(bytes.Repeat([]byte("x"), maxBodySize+1)))
	recReq := httptest.NewRecorder()
	router.ServeHTTP(recReq, oversizedReq)
	if recReq.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413", recReq.Code)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("oversized request reached upstream %d times", upstreamCalls.Load())
	}

	// Oversized response returns 502 without truncation.
	recResp := httptest.NewRecorder()
	router.ServeHTTP(recResp, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if recResp.Code != http.StatusBadGateway {
		t.Fatalf("oversized response status = %d, want 502", recResp.Code)
	}
}

func TestRelayExcludesStarExcludedAndHomeMode(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	// Credential with excluded-models: ["*"] has no models registered in registry.
	credExcluded := &auth.Auth{
		ID:       "meta-excluded-all",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":         "meta-secret",
			"base_url":        upstream.URL + "/v1",
			"excluded_models": "*",
		},
	}
	registerMetaTestCred(t, manager, credExcluded)
	router := newTestRouter(NewHandler(manager, cfg))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("excluded-all status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("excluded-all reached upstream")
	}

	// Home mode enabled returns 503 without calling upstream.
	homeManager := auth.NewManager(nil, nil, nil)
	homeManager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	registerMetaTestCred(t, homeManager, &auth.Auth{
		ID:       "meta-home-cred",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "meta-secret",
			"base_url": upstream.URL + "/v1",
		},
	}, "muse-wonder-1.0")
	homeManager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})

	homeRouter := newTestRouter(NewHandler(homeManager, cfg))
	recHome := httptest.NewRecorder()
	homeRouter.ServeHTTP(recHome, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if recHome.Code != http.StatusServiceUnavailable {
		t.Fatalf("home mode status = %d, want 503; body=%s", recHome.Code, recHome.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("home mode reached upstream")
	}
}

func TestRelayDCAOnlyMintsOnceAndUsesMintedOrigin(t *testing.T) {
	var mintCalls atomic.Int32
	var apiCalls atomic.Int32
	var seenAuths []string

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		seenAuths = append(seenAuths, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"models":[]}`)
	}))
	defer apiServer.Close()

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mintCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"api_key":"minted-key-1","base_url":"`+apiServer.URL+`/v1"}`)
	}))
	defer mintServer.Close()
	t.Setenv("META_MINT_URL", mintServer.URL)

	cfg := &config.Config{}
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	cred := &auth.Auth{
		ID:       "meta-dca-only",
		Provider: "meta",
		Status:   auth.StatusActive,
		Metadata: map[string]any{
			"type":         "meta",
			"access_token": "dca:initial-device-token",
			"dca_token":    "dca:initial-device-token",
		},
	}
	registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")
	router := newTestRouter(NewHandler(manager, cfg))

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200; body=%s", i, rec.Code, rec.Body.String())
		}
	}
	if mintCalls.Load() != 1 {
		t.Fatalf("mintCalls = %d, want 1", mintCalls.Load())
	}
	if apiCalls.Load() != 2 {
		t.Fatalf("apiCalls = %d, want 2", apiCalls.Load())
	}
	for i, got := range seenAuths {
		if got != "Bearer minted-key-1" {
			t.Fatalf("call %d Authorization = %q, want Bearer minted-key-1", i, got)
		}
	}
}

func TestRelayRefreshesOnceOn401AndRetiresOnSecond401(t *testing.T) {
	var mintCalls atomic.Int32
	var seenTokens []string

	var apiURL string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seenTokens = append(seenTokens, tok)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_api_key"}`)
	}))
	defer apiServer.Close()
	apiURL = apiServer.URL

	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mintCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"api_key":"fresh-minted-key","base_url":"`+apiURL+`/v1"}`)
	}))
	defer mintServer.Close()
	t.Setenv("META_MINT_URL", mintServer.URL)

	cfg := &config.Config{}
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	cred := &auth.Auth{
		ID:       "meta-refresh-test",
		Provider: "meta",
		Status:   auth.StatusActive,
		Metadata: map[string]any{
			"type":         "meta",
			"access_token": "stale-key",
			"api_key":      "stale-key",
			"dca_token":    "dca:valid",
			"base_url":     apiURL + "/v1",
		},
		Attributes: map[string]string{
			"api_key":  "stale-key",
			"base_url": apiURL + "/v1",
		},
	}
	registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")
	router := newTestRouter(NewHandler(manager, cfg))

	// First request gets 401 -> refreshes once -> retries -> gets 401 -> retires credential.
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if rec1.Code != http.StatusUnauthorized {
		t.Fatalf("first status = %d, want 401", rec1.Code)
	}
	if mintCalls.Load() != 1 {
		t.Fatalf("mintCalls = %d, want 1", mintCalls.Load())
	}
	if len(seenTokens) != 2 || seenTokens[0] != "stale-key" || seenTokens[1] != "fresh-minted-key" {
		t.Fatalf("seenTokens = %#v, want [stale-key fresh-minted-key]", seenTokens)
	}

	// Subsequent requests must NOT re-mint on every request once retired.
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if rec2.Code == http.StatusOK {
		t.Fatalf("second request unexpectedly succeeded")
	}
	if mintCalls.Load() != 1 {
		t.Fatalf("second request triggered extra mint: mintCalls = %d, want 1", mintCalls.Load())
	}
}

func TestRelaySkipsRateLimitedCredentialAndPassesRetryAfter(t *testing.T) {
	var badCalls atomic.Int32
	badUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badCalls.Add(1)
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
	}))
	defer badUpstream.Close()

	var goodCalls atomic.Int32
	goodUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"models":["ok"]}`)
	}))
	defer goodUpstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	credHigh := &auth.Auth{
		ID:       "meta-high-429",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "bad-key",
			"base_url": badUpstream.URL + "/v1",
			"priority": "10",
		},
	}
	credBackup := &auth.Auth{
		ID:       "meta-backup-ok",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "good-key",
			"base_url": goodUpstream.URL + "/v1",
			"priority": "1",
		},
	}
	registerMetaTestCred(t, manager, credHigh, "muse-wonder-1.0")
	registerMetaTestCred(t, manager, credBackup, "muse-wonder-1.0")
	router := newTestRouter(NewHandler(manager, cfg))

	// Request 1 hits credHigh -> 429 with Retry-After: 45, cools credHigh.
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(`{"q":"go"}`)))
	if rec1.Code != http.StatusTooManyRequests || rec1.Header().Get("Retry-After") != "45" {
		t.Fatalf("first response = status %d Retry-After %q, want 429 / 45", rec1.Code, rec1.Header().Get("Retry-After"))
	}

	// Request 2 skips cooled credHigh and routes to credBackup.
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second response = status %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	if badCalls.Load() != 1 || goodCalls.Load() != 1 {
		t.Fatalf("calls bad=%d good=%d, want 1 and 1", badCalls.Load(), goodCalls.Load())
	}
}

func TestRelayModelCooldownDoesNotHideOtherHealthyCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))

	credA := &auth.Auth{
		ID:       "meta-cred-a",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "key-a",
			"base_url": upstream.URL + "/v1",
		},
	}
	credB := &auth.Auth{
		ID:       "meta-cred-b",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "key-b",
			"base_url": upstream.URL + "/v1",
		},
	}
	registerMetaTestCred(t, manager, credA, "muse-spark-1.3")
	registerMetaTestCred(t, manager, credB, "muse-wonder-1.0")

	// Cool down muse-spark-1.3 on credA; credB serving muse-wonder-1.0 must still work.
	manager.MarkResult(context.Background(), auth.Result{
		AuthID:   credA.ID,
		Provider: "meta",
		Model:    "muse-spark-1.3",
		Success:  false,
		Error:    &auth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	router := newTestRouter(NewHandler(manager, cfg))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestOriginFromAuth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		auth    *auth.Auth
		want    string
		wantErr bool
	}{
		{
			name: "default",
			auth: &auth.Auth{},
			want: "https://api.meta.ai",
		},
		{
			name: "attribute base_url",
			auth: &auth.Auth{Attributes: map[string]string{"base_url": "https://regional.meta.ai/api/v1"}},
			want: "https://regional.meta.ai",
		},
		{
			name: "metadata api_base_url",
			auth: &auth.Auth{Metadata: map[string]any{"api_base_url": "http://127.0.0.1:8080/v1"}},
			want: "http://127.0.0.1:8080",
		},
		{
			name: "empty metadata base_url falls back to api_base_url",
			auth: &auth.Auth{Metadata: map[string]any{"base_url": "", "api_base_url": "https://regional.example/v1"}},
			want: "https://regional.example",
		},
		{
			name: "whitespace metadata base_url falls back to api_base_url",
			auth: &auth.Auth{Metadata: map[string]any{"base_url": "   \t ", "api_base_url": "https://regional.example/v1"}},
			want: "https://regional.example",
		},
		{
			name:    "invalid url",
			auth:    &auth.Auth{Attributes: map[string]string{"base_url": "ftp://invalid.example/v1"}},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errOrigin := originFromAuth(tc.auth)
			if tc.wantErr {
				if errOrigin == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if errOrigin != nil {
				t.Fatalf("unexpected error: %v", errOrigin)
			}
			if got != tc.want {
				t.Fatalf("origin = %q, want %q", got, tc.want)
			}
		})
	}
}

type museCodeRoundTripper func(*http.Request) (*http.Response, error)

func (fn museCodeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type errUnexpectedEOFBody struct{}

func (errUnexpectedEOFBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (errUnexpectedEOFBody) Close() error             { return nil }

func TestRelayTransportDropsAndClientCancelDoNotCooldownCredential(t *testing.T) {
	assertHealthy := func(t *testing.T, manager *auth.Manager, id string) {
		t.Helper()
		cred, ok := manager.GetByID(id)
		if !ok || cred == nil {
			t.Fatalf("credential %s missing", id)
		}
		if cred.Unavailable || cred.Status != auth.StatusActive || !cred.NextRetryAfter.IsZero() || cred.Quota.Exceeded {
			t.Fatalf("credential %s was unexpectedly cooled down: status=%s unavailable=%v retry=%v quota=%+v",
				id, cred.Status, cred.Unavailable, cred.NextRetryAfter, cred.Quota)
		}
	}

	t.Run("client_cancel", func(t *testing.T) {
		inFlight := make(chan struct{})
		release := make(chan struct{})
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(inFlight)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer func() {
			close(release)
			upstream.Close()
		}()

		cfg := &config.Config{}
		manager := auth.NewManager(nil, nil, nil)
		manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
		cred := &auth.Auth{
			ID:         "meta-cancel-test",
			Provider:   "meta",
			Status:     auth.StatusActive,
			Attributes: map[string]string{"api_key": "meta-secret", "base_url": upstream.URL + "/v1"},
		}
		registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")
		router := newTestRouter(NewHandler(manager, cfg))

		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/muse-code/config", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			router.ServeHTTP(rec, req)
		}()
		<-inFlight
		cancel()
		<-done

		if rec.Code != 499 {
			t.Fatalf("client cancel status = %d, want 499", rec.Code)
		}
		assertHealthy(t, manager, cred.ID)
	})

	t.Run("connection_reset", func(t *testing.T) {
		cfg := &config.Config{}
		manager := auth.NewManager(nil, nil, nil)
		manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
		cred := &auth.Auth{
			ID:         "meta-connreset-test",
			Provider:   "meta",
			Status:     auth.StatusActive,
			Attributes: map[string]string{"api_key": "meta-secret", "base_url": "https://api.meta.ai/v1"},
		}
		registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")
		router := newTestRouter(NewHandler(manager, cfg))

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", museCodeRoundTripper(func(req *http.Request) (*http.Response, error) {
			return nil, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
		}))
		req := httptest.NewRequest(http.MethodGet, "/muse-code/config", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("connection reset status = %d, want 502; body=%s", rec.Code, rec.Body.String())
		}
		assertHealthy(t, manager, cred.ID)
	})

	t.Run("response_body_unexpected_eof", func(t *testing.T) {
		cfg := &config.Config{}
		manager := auth.NewManager(nil, nil, nil)
		manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
		cred := &auth.Auth{
			ID:         "meta-eof-test",
			Provider:   "meta",
			Status:     auth.StatusActive,
			Attributes: map[string]string{"api_key": "meta-secret", "base_url": "https://api.meta.ai/v1"},
		}
		registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")
		router := newTestRouter(NewHandler(manager, cfg))

		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", museCodeRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       errUnexpectedEOFBody{},
				Request:    req,
			}, nil
		}))
		req := httptest.NewRequest(http.MethodGet, "/muse-code/config", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Fatalf("unexpected eof status = %d, want 502; body=%s", rec.Code, rec.Body.String())
		}
		assertHealthy(t, manager, cred.ID)
	})
}

func TestRelayConcurrent200DoesNotClearNewer429Cooldown(t *testing.T) {
	configStarted := make(chan struct{})
	releaseConfig := make(chan struct{})
	var configOnce sync.Once
	var upstreamCalls atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		switch r.URL.Path {
		case "/muse-code/config":
			configOnce.Do(func() { close(configStarted) })
			<-releaseConfig
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/muse-code/search":
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	cred := &auth.Auth{
		ID:         "meta-seq-race-test",
		Provider:   "meta",
		Status:     auth.StatusActive,
		Attributes: map[string]string{"api_key": "meta-secret", "base_url": upstream.URL + "/v1"},
	}
	registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")
	router := newTestRouter(NewHandler(manager, cfg))

	// 1. Start config request and hold it mid-flight before its 200 completes.
	rec1 := httptest.NewRecorder()
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		router.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	}()
	<-configStarted

	// 2. While request 1 is in flight, search request receives 429 and records cooldown.
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(`{"q":"test"}`)))
	if rec2.Code != http.StatusTooManyRequests {
		close(releaseConfig)
		<-done1
		t.Fatalf("search status = %d, want 429", rec2.Code)
	}

	// 3. Release request 1 so its stale 200 arrives after the 429 cooldown.
	close(releaseConfig)
	<-done1
	if rec1.Code != http.StatusOK {
		t.Fatalf("config status = %d, want 200", rec1.Code)
	}

	// 4. Cooldown must remain intact; a follow-up request must not call upstream.
	snap, ok := manager.GetByID(cred.ID)
	if !ok || snap == nil {
		t.Fatalf("credential missing")
	}
	if !snap.Unavailable || !snap.Quota.Exceeded || !snap.NextRetryAfter.After(time.Now()) {
		t.Fatalf("stale 200 cleared newer 429 cooldown: unavailable=%v quota=%+v nextRetry=%v",
			snap.Unavailable, snap.Quota, snap.NextRetryAfter)
	}

	callsBefore := upstreamCalls.Load()
	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("follow-up status = %d, want 429; body=%s", rec3.Code, rec3.Body.String())
	}
	if upstreamCalls.Load() != callsBefore {
		t.Fatalf("cooled credential was called again (%d -> %d)", callsBefore, upstreamCalls.Load())
	}
}

func TestRelayRespectsRequestScopedErrorRulesOn401And429(t *testing.T) {
	var mintCalls atomic.Int32
	mintServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mintCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"api_key":"should-not-mint"}`)
	}))
	defer mintServer.Close()
	t.Setenv("META_MINT_URL", mintServer.URL)

	var upstreamStatus atomic.Int32
	var upstreamBody atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := int(upstreamStatus.Load())
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(status)
		body, _ := upstreamBody.Load().(string)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		OAuthRequestScopedErrors: map[string][]config.RequestScopedErrorRule{
			"meta": {
				{
					Status: 401,
					Match:  []string{"scoped_unauthorized"},
					Action: auth.RequestScopedActionStop,
				},
				{
					Status: 429,
					Match:  []string{"scoped_rate_limit"},
					Action: auth.RequestScopedActionStop,
				},
			},
		},
	}
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))
	cred := &auth.Auth{
		ID:       "meta-request-scoped-test",
		Provider: "meta",
		Status:   auth.StatusActive,
		Metadata: map[string]any{
			"type":         "meta",
			"access_token": "valid-oauth-key",
			"api_key":      "valid-oauth-key",
			"dca_token":    "dca:valid",
			"base_url":     upstream.URL + "/v1",
		},
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "valid-oauth-key",
			"base_url":  upstream.URL + "/v1",
		},
	}
	registerMetaTestCred(t, manager, cred, "muse-wonder-1.0")
	router := newTestRouter(NewHandler(manager, cfg))

	assertHealthy := func(t *testing.T) {
		t.Helper()
		snap, ok := manager.GetByID(cred.ID)
		if !ok || snap == nil {
			t.Fatalf("credential missing")
		}
		if snap.Unavailable || snap.Status != auth.StatusActive || !snap.NextRetryAfter.IsZero() || snap.Quota.Exceeded || snap.RejectedAccessToken != "" {
			t.Fatalf("request-scoped stop altered credential state: status=%s unavailable=%v retry=%v quota=%+v rejected=%q",
				snap.Status, snap.Unavailable, snap.NextRetryAfter, snap.Quota, snap.RejectedAccessToken)
		}
	}

	// 1. 401 matching request-scoped stop skips refresh, keeps token unrejected, keeps credential active.
	upstreamStatus.Store(http.StatusUnauthorized)
	upstreamBody.Store(`{"error":"scoped_unauthorized"}`)
	rec401 := httptest.NewRecorder()
	router.ServeHTTP(rec401, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if rec401.Code != http.StatusUnauthorized {
		t.Fatalf("401 status = %d, want 401", rec401.Code)
	}
	if mintCalls.Load() != 0 {
		t.Fatalf("mintCalls = %d, want 0 on request-scoped 401", mintCalls.Load())
	}
	assertHealthy(t)

	// 2. 429 matching request-scoped stop skips quota cooldown and keeps credential active.
	upstreamStatus.Store(http.StatusTooManyRequests)
	upstreamBody.Store(`{"error":"scoped_rate_limit"}`)
	rec429 := httptest.NewRecorder()
	router.ServeHTTP(rec429, httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(`{"q":"test"}`)))
	if rec429.Code != http.StatusTooManyRequests {
		t.Fatalf("429 status = %d, want 429", rec429.Code)
	}
	assertHealthy(t)

	// 3. Subsequent request still uses the same credential without cooldown blocking.
	upstreamStatus.Store(http.StatusOK)
	upstreamBody.Store(`{"models":[]}`)
	rec200 := httptest.NewRecorder()
	router.ServeHTTP(rec200, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if rec200.Code != http.StatusOK {
		t.Fatalf("follow-up status = %d, want 200; body=%s", rec200.Code, rec200.Body.String())
	}
	assertHealthy(t)
}

func TestRelay429AfterPriorGenerationModelStateFailsOverToBackup(t *testing.T) {
	var primaryCalls atomic.Int32
	primaryUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
	}))
	defer primaryUpstream.Close()

	var backupCalls atomic.Int32
	backupUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"models":["backup"]}`)
	}))
	defer backupUpstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))

	credPrimary := &auth.Auth{
		ID:       "meta-primary-with-model-state",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "primary-key",
			"base_url": primaryUpstream.URL + "/v1",
			"priority": "10",
		},
	}
	credBackup := &auth.Auth{
		ID:       "meta-backup-healthy",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "backup-key",
			"base_url": backupUpstream.URL + "/v1",
			"priority": "1",
		},
	}
	registerMetaTestCred(t, manager, credPrimary, "muse-wonder-1.0")
	registerMetaTestCred(t, manager, credBackup, "muse-wonder-1.0")

	// Seed an active model state on credPrimary as left by prior /v1/responses generation.
	manager.MarkResult(context.Background(), auth.Result{
		AuthID:            credPrimary.ID,
		Provider:          "meta",
		Model:             "muse-wonder-1.0",
		Success:           true,
		CompletedResponse: true,
	})
	seeded, _ := manager.GetByID(credPrimary.ID)
	if seeded == nil || len(seeded.ModelStates) == 0 {
		t.Fatalf("expected seeded ModelStates on primary credential")
	}

	router := newTestRouter(NewHandler(manager, cfg))

	// 1. /muse-code/search hits credPrimary and receives 429.
	recSearch := httptest.NewRecorder()
	router.ServeHTTP(recSearch, httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(`{"q":"go"}`)))
	if recSearch.Code != http.StatusTooManyRequests {
		t.Fatalf("search status = %d, want 429; body=%s", recSearch.Code, recSearch.Body.String())
	}

	// Verify credential_quota is recorded and propagated to existing model states.
	after429, _ := manager.GetByID(credPrimary.ID)
	if after429 == nil || !after429.Quota.Exceeded || after429.Quota.Reason != auth.ErrorCodeCredentialQuota {
		t.Fatalf("primary quota = %+v, want Exceeded=true Reason=credential_quota", after429.Quota)
	}
	if ms := after429.ModelStates["muse-wonder-1.0"]; ms == nil || !ms.Quota.Exceeded || ms.Quota.Reason != auth.ErrorCodeCredentialQuota {
		t.Fatalf("primary model state = %+v, want propagated credential_quota", ms)
	}

	// 2. Subsequent /muse-code/config must skip cooled credPrimary and hit credBackup.
	recConfig := httptest.NewRecorder()
	router.ServeHTTP(recConfig, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if recConfig.Code != http.StatusOK {
		t.Fatalf("config status = %d, want 200; body=%s", recConfig.Code, recConfig.Body.String())
	}
	if primaryCalls.Load() != 1 || backupCalls.Load() != 1 {
		t.Fatalf("calls primary=%d backup=%d, want 1 and 1", primaryCalls.Load(), backupCalls.Load())
	}
}

func TestRelayNon429CredentialCooldownBlocksAllModelsEvenStatelessOnes(t *testing.T) {
	var primaryCalls atomic.Int32
	primaryUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"forbidden"}`)
	}))
	defer primaryUpstream.Close()

	var backupCalls atomic.Int32
	backupUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"models":["backup"]}`)
	}))
	defer backupUpstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))

	credPrimary := &auth.Auth{
		ID:       "meta-primary-403-partial-state",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "primary-key",
			"base_url": primaryUpstream.URL + "/v1",
			"priority": "10",
		},
	}
	credBackup := &auth.Auth{
		ID:       "meta-backup-403-healthy",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "backup-key",
			"base_url": backupUpstream.URL + "/v1",
			"priority": "1",
		},
	}
	// Register two models; seed ModelStates ONLY for muse-wonder-1.0 so muse-spark-1.3 remains stateless.
	registerMetaTestCred(t, manager, credPrimary, "muse-spark-1.3", "muse-wonder-1.0")
	registerMetaTestCred(t, manager, credBackup, "muse-spark-1.3", "muse-wonder-1.0")

	manager.MarkResult(context.Background(), auth.Result{
		AuthID:            credPrimary.ID,
		Provider:          "meta",
		Model:             "muse-wonder-1.0",
		Success:           true,
		CompletedResponse: true,
	})

	router := newTestRouter(NewHandler(manager, cfg))

	recSearch := httptest.NewRecorder()
	router.ServeHTTP(recSearch, httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(`{"q":"go"}`)))
	if recSearch.Code != http.StatusForbidden {
		t.Fatalf("search status = %d, want 403; body=%s", recSearch.Code, recSearch.Body.String())
	}

	// Next request sorts models and tries stateless muse-spark-1.3 first: credPrimary must still be blocked.
	recConfig := httptest.NewRecorder()
	router.ServeHTTP(recConfig, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
	if recConfig.Code != http.StatusOK {
		t.Fatalf("config status = %d, want 200; body=%s", recConfig.Code, recConfig.Body.String())
	}
	if primaryCalls.Load() != 1 || backupCalls.Load() != 1 {
		t.Fatalf("calls primary=%d backup=%d, want 1 and 1", primaryCalls.Load(), backupCalls.Load())
	}
}

func TestRelayRequestScopedContinueAndCooldownFailover(t *testing.T) {
	t.Run("continue_fails_over_without_cooling_primary", func(t *testing.T) {
		var primaryCalls atomic.Int32
		primaryUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryCalls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"scoped_continue_401"}`)
		}))
		defer primaryUpstream.Close()

		var backupCalls atomic.Int32
		backupUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backupCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"models":["from-backup"]}`)
		}))
		defer backupUpstream.Close()

		cfg := &config.Config{
			OAuthRequestScopedErrors: map[string][]config.RequestScopedErrorRule{
				"meta": {
					{
						Status: 401,
						Match:  []string{"scoped_continue_401"},
						Action: auth.RequestScopedActionContinue,
					},
				},
			},
		}
		manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
		manager.SetConfig(cfg)
		manager.RegisterExecutor(executor.NewMetaExecutor(cfg))

		credPrimary := &auth.Auth{
			ID:       "meta-continue-primary",
			Provider: "meta",
			Status:   auth.StatusActive,
			Metadata: map[string]any{"type": "meta", "access_token": "p-key", "api_key": "p-key", "dca_token": "dca:p", "base_url": primaryUpstream.URL + "/v1"},
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"api_key":   "p-key",
				"base_url":  primaryUpstream.URL + "/v1",
				"priority":  "10",
			},
		}
		credBackup := &auth.Auth{
			ID:       "meta-continue-backup",
			Provider: "meta",
			Status:   auth.StatusActive,
			Metadata: map[string]any{"type": "meta", "access_token": "b-key", "api_key": "b-key", "base_url": backupUpstream.URL + "/v1"},
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"api_key":   "b-key",
				"base_url":  backupUpstream.URL + "/v1",
				"priority":  "1",
			},
		}
		registerMetaTestCred(t, manager, credPrimary, "muse-wonder-1.0")
		registerMetaTestCred(t, manager, credBackup, "muse-wonder-1.0")
		router := newTestRouter(NewHandler(manager, cfg))

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "from-backup") {
			t.Fatalf("status=%d body=%q, want 200 from backup", rec.Code, rec.Body.String())
		}
		if primaryCalls.Load() != 1 || backupCalls.Load() != 1 {
			t.Fatalf("calls primary=%d backup=%d, want 1 and 1", primaryCalls.Load(), backupCalls.Load())
		}
		snap, _ := manager.GetByID(credPrimary.ID)
		if snap == nil || snap.Unavailable || snap.Status != auth.StatusActive || snap.RejectedAccessToken != "" {
			t.Fatalf("continue must not cool or reject primary credential: %+v", snap)
		}
	})

	t.Run("continue_bounded_by_max_retry_credentials", func(t *testing.T) {
		var primaryCalls, backupCalls atomic.Int32
		primaryUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryCalls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"scoped_continue_401"}`)
		}))
		defer primaryUpstream.Close()
		backupUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backupCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"models":["backup"]}`)
		}))
		defer backupUpstream.Close()

		cfg := &config.Config{
			OAuthRequestScopedErrors: map[string][]config.RequestScopedErrorRule{
				"meta": {{Status: 401, Match: []string{"scoped_continue_401"}, Action: auth.RequestScopedActionContinue}},
			},
		}
		manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
		manager.SetConfig(cfg)
		manager.SetRetryConfig(0, 0, 1)
		manager.RegisterExecutor(executor.NewMetaExecutor(cfg))

		registerMetaTestCred(t, manager, &auth.Auth{
			ID: "meta-bound-p", Provider: "meta", Status: auth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "api_key": "p-key", "base_url": primaryUpstream.URL + "/v1", "priority": "10"},
		}, "muse-wonder-1.0")
		registerMetaTestCred(t, manager, &auth.Auth{
			ID: "meta-bound-b", Provider: "meta", Status: auth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "api_key": "b-key", "base_url": backupUpstream.URL + "/v1", "priority": "1"},
		}, "muse-wonder-1.0")
		router := newTestRouter(NewHandler(manager, cfg))

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 when MaxRetryCredentials=1", rec.Code)
		}
		if primaryCalls.Load() != 1 || backupCalls.Load() != 0 {
			t.Fatalf("calls primary=%d backup=%d, want 1 and 0", primaryCalls.Load(), backupCalls.Load())
		}
	})

	t.Run("continue_and_cooldown_cools_primary_and_fails_over_to_backup", func(t *testing.T) {
		var primaryCalls, backupCalls atomic.Int32
		primaryUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryCalls.Add(1)
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"scoped_continue_cooldown"}`)
		}))
		defer primaryUpstream.Close()
		backupUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backupCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"models":["from-backup"]}`)
		}))
		defer backupUpstream.Close()

		cfg := &config.Config{
			DisableCooling: true,
			OAuthRequestScopedErrors: map[string][]config.RequestScopedErrorRule{
				"meta": {{Status: 429, Match: []string{"scoped_continue_cooldown"}, Action: auth.RequestScopedActionContinueAndCooldown}},
			},
		}
		manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
		manager.SetConfig(cfg)
		manager.RegisterExecutor(executor.NewMetaExecutor(cfg))

		credPrimary := &auth.Auth{
			ID: "meta-cc-primary", Provider: "meta", Status: auth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "api_key": "p-key", "base_url": primaryUpstream.URL + "/v1", "priority": "10"},
		}
		credBackup := &auth.Auth{
			ID: "meta-cc-backup", Provider: "meta", Status: auth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "api_key": "b-key", "base_url": backupUpstream.URL + "/v1", "priority": "1"},
		}
		registerMetaTestCred(t, manager, credPrimary, "muse-wonder-1.0")
		registerMetaTestCred(t, manager, credBackup, "muse-wonder-1.0")
		router := newTestRouter(NewHandler(manager, cfg))

		// First request fails over from primary to backup and force-cools primary.
		rec1 := httptest.NewRecorder()
		router.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
		if rec1.Code != http.StatusOK || !strings.Contains(rec1.Body.String(), "from-backup") {
			t.Fatalf("first status=%d body=%q, want 200 from backup", rec1.Code, rec1.Body.String())
		}
		snap, _ := manager.GetByID(credPrimary.ID)
		if snap == nil || !snap.Unavailable || !snap.Quota.Exceeded || snap.Quota.Reason != auth.ErrorCodeCredentialQuota {
			t.Fatalf("expected primary to be force-cooled, got %+v", snap)
		}

		// Second request skips cooled primary and goes directly to backup.
		rec2 := httptest.NewRecorder()
		router.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
		if rec2.Code != http.StatusOK {
			t.Fatalf("second status=%d, want 200", rec2.Code)
		}
		if primaryCalls.Load() != 1 || backupCalls.Load() != 2 {
			t.Fatalf("calls primary=%d backup=%d, want 1 and 2", primaryCalls.Load(), backupCalls.Load())
		}
	})

	t.Run("continue_exhausts_all_tried_credentials_once_and_returns_last_response", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"scoped_continue_401"}`)
		}))
		defer upstream.Close()

		cfg := &config.Config{
			OAuthRequestScopedErrors: map[string][]config.RequestScopedErrorRule{
				"meta": {{Status: 401, Match: []string{"scoped_continue_401"}, Action: auth.RequestScopedActionContinue}},
			},
		}
		manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
		manager.SetConfig(cfg)
		manager.RegisterExecutor(executor.NewMetaExecutor(cfg))

		registerMetaTestCred(t, manager, &auth.Auth{
			ID: "meta-exhaust-1", Provider: "meta", Status: auth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "api_key": "k1", "base_url": upstream.URL + "/v1", "priority": "10"},
		}, "muse-wonder-1.0")
		registerMetaTestCred(t, manager, &auth.Auth{
			ID: "meta-exhaust-2", Provider: "meta", Status: auth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "api_key": "k2", "base_url": upstream.URL + "/v1", "priority": "1"},
		}, "muse-wonder-1.0")
		router := newTestRouter(NewHandler(manager, cfg))

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/muse-code/config", nil))
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "scoped_continue_401") {
			t.Fatalf("status=%d body=%q, want final 401 upstream error", rec.Code, rec.Body.String())
		}
		if calls.Load() != 2 {
			t.Fatalf("calls = %d, want 2 (each credential tried once)", calls.Load())
		}
	})
}
