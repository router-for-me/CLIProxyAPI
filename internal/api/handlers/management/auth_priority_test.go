package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestBuildAuthFileEntry_IncludesPriorityAndCodexQuota(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{Port: 8317}
	h := NewHandler(cfg, "config.yaml", manager)

	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "codex",
		Attributes: map[string]string{
			"path": "does-not-exist.json",
		},
		Metadata: map[string]any{"priority": 10},
	}

	usage.UpdateCodexQuotaSnapshot("auth-1", &usage.CodexQuotaSnapshot{UpdatedAt: time.Now()})

	entry := h.buildAuthFileEntry(auth)
	if entry == nil {
		t.Fatal("expected entry")
	}
	if got, ok := entry["priority"].(int); !ok || got != 10 {
		t.Fatalf("expected priority=10, got %#v", entry["priority"])
	}
	if entry["codex_quota"] == nil {
		t.Fatal("expected codex_quota to be present")
	}
}

func TestBuildAuthFileEntry_IncludesAntigravityQuota(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{Port: 8317}
	h := NewHandler(cfg, "config.yaml", manager)

	auth := &coreauth.Auth{
		ID:       "ag-1",
		Provider: "antigravity",
		Attributes: map[string]string{
			"path": "does-not-exist.json",
		},
	}

	usage.UpdateAntigravityQuotaSnapshot("ag-1", &usage.AntigravityQuotaSnapshot{UpdatedAt: time.Now()})

	entry := h.buildAuthFileEntry(auth)
	if entry == nil {
		t.Fatal("expected entry")
	}
	if entry["antigravity_quota"] == nil {
		t.Fatalf("expected antigravity_quota to be present, got %#v", entry)
	}
}

func TestBuildAuthFileEntry_IncludesQuotaState(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{Port: 8317}
	h := NewHandler(cfg, "config.yaml", manager)

	now := time.Now().UTC()
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "codex",
		Attributes: map[string]string{
			"path": "does-not-exist.json",
		},
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "rate_limited",
			NextRecoverAt: now.Add(5 * time.Minute),
			BackoffLevel:  2,
		},
	}

	entry := h.buildAuthFileEntry(auth)
	if entry == nil {
		t.Fatal("expected entry")
	}
	quota, ok := entry["quota"].(coreauth.QuotaState)
	if !ok {
		t.Fatalf("expected quota to be QuotaState, got %#v", entry["quota"])
	}
	if !quota.Exceeded {
		t.Fatalf("expected quota exceeded=true, got %#v", quota)
	}
	if quota.Reason != "rate_limited" {
		t.Fatalf("expected quota reason to match, got %#v", quota.Reason)
	}
}

func TestPutAuthFilePriority_UpdatesAuthMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{Port: 8317}
	h := NewHandler(cfg, "config.yaml", manager)

	_, _ = manager.Register(nil, &coreauth.Auth{ID: "auth-1", Provider: "codex", Metadata: map[string]any{}})

	body := []byte(`{"id":"auth-1","priority":7}`)
	req := httptest.NewRequest("PUT", "/v0/management/auth-files/priority", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.PutAuthFilePriority(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	updated, ok := manager.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatal("expected auth to exist")
	}
	if updated.Metadata == nil {
		t.Fatal("expected metadata")
	}
	if got, ok := updated.Metadata["priority"]; !ok || got == nil {
		t.Fatalf("expected priority to be set, got %#v", updated.Metadata["priority"])
	}
}

func TestPutAuthFileDisabled_TogglesAuthDisabledState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{Port: 8317}
	h := NewHandler(cfg, "config.yaml", manager)

	_, _ = manager.Register(nil, &coreauth.Auth{ID: "auth-1", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{}})

	disableBody := []byte(`{"id":"auth-1","disabled":true}`)
	req := httptest.NewRequest("PUT", "/v0/management/auth-files/disabled", bytes.NewReader(disableBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.PutAuthFileDisabled(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	updated, ok := manager.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatal("expected auth to exist")
	}
	if !updated.Disabled {
		t.Fatalf("expected disabled=true, got %#v", updated.Disabled)
	}
	if updated.Status != coreauth.StatusDisabled {
		t.Fatalf("expected status=disabled, got %#v", updated.Status)
	}

	enableBody := []byte(`{"id":"auth-1","disabled":false}`)
	req = httptest.NewRequest("PUT", "/v0/management/auth-files/disabled", bytes.NewReader(enableBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = req

	h.PutAuthFileDisabled(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	updated, ok = manager.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatal("expected auth to exist")
	}
	if updated.Disabled {
		t.Fatalf("expected disabled=false, got %#v", updated.Disabled)
	}
	if updated.Status == coreauth.StatusDisabled {
		t.Fatalf("expected status to not be disabled, got %#v", updated.Status)
	}
}
