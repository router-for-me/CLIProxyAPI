package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const testManagementKey = "test-secret"

func newSessionAffinityTestEngine(t *testing.T, manager *coreauth.Manager) *gin.Engine {
	t.Helper()
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      testManagementKey,
		authManager:    manager,
	}
	engine := gin.New()
	engine.DELETE("/v0/management/session-affinity", h.Middleware(), h.DeleteSessionAffinity)
	return engine
}

func deleteSessionAffinity(t *testing.T, engine *gin.Engine, sessionID, key string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/v0/management/session-affinity"
	if sessionID != "" {
		target += "?session_id=" + url.QueryEscape(sessionID)
	}
	req := httptest.NewRequest(http.MethodDelete, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if key != "" {
		req.Header.Set("X-Management-Key", key)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestDeleteSessionAffinity(t *testing.T) {
	const sessionUUID = "8046da35-c12f-4b6b-8c9e-86ecb162fcee"
	affinitySessionID := "claude:" + sessionUUID

	manager := coreauth.NewManager(nil, nil, nil)
	selector := coreauth.NewSessionAffinitySelector(&coreauth.RoundRobinSelector{})
	defer selector.Stop()
	manager.SetSelector(selector)
	engine := newSessionAffinityTestEngine(t, manager)

	auths := []*coreauth.Auth{{ID: "auth-a"}, {ID: "auth-b"}}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Claude-Code-Session-Id": []string{sessionUUID}},
	}
	bound, errPick := selector.Pick(context.Background(), "claude", "claude-sonnet-4-5", opts, auths)
	if errPick != nil || bound == nil {
		t.Fatalf("Pick() error = %v, auth = %v", errPick, bound)
	}

	t.Run("missing management key is unauthorized", func(t *testing.T) {
		rec := deleteSessionAffinity(t, engine, affinitySessionID, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("wrong management key is unauthorized", func(t *testing.T) {
		rec := deleteSessionAffinity(t, engine, affinitySessionID, "wrong-secret")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("missing session id is a bad request", func(t *testing.T) {
		rec := deleteSessionAffinity(t, engine, "", testManagementKey)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})

	t.Run("control characters are a bad request", func(t *testing.T) {
		rec := deleteSessionAffinity(t, engine, "claude:bad\nsession", testManagementKey)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	})

	t.Run("unknown session is a successful no-op", func(t *testing.T) {
		rec := deleteSessionAffinity(t, engine, "claude:00000000-0000-4000-8000-000000000000", testManagementKey)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		var body struct {
			Status      string `json:"status"`
			Removed     bool   `json:"removed"`
			CacheGroups int    `json:"cache_groups"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
		}
		if body.Status != "ok" || body.Removed || body.CacheGroups != 0 {
			t.Fatalf("unknown session response = %+v, want ok/false/0", body)
		}
	})

	t.Run("bound session is released", func(t *testing.T) {
		rec := deleteSessionAffinity(t, engine, affinitySessionID, testManagementKey)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		var body struct {
			Status      string `json:"status"`
			SessionID   string `json:"session_id"`
			Removed     bool   `json:"removed"`
			CacheGroups int    `json:"cache_groups"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
		}
		if body.Status != "ok" || !body.Removed || body.CacheGroups != 1 || body.SessionID != affinitySessionID {
			t.Fatalf("bound session response = %+v, want ok/true/1/%s", body, affinitySessionID)
		}

		if result := selector.InvalidateSession(affinitySessionID); result.Removed() {
			t.Fatalf("binding survived the management call: %+v", result)
		}
	})
}

func TestDeleteSessionAffinityWithoutAffinitySelector(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetSelector(&coreauth.RoundRobinSelector{})
	engine := newSessionAffinityTestEngine(t, manager)

	rec := deleteSessionAffinity(t, engine, "claude:8046da35-c12f-4b6b-8c9e-86ecb162fcee", testManagementKey)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}
