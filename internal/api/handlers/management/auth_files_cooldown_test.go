package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type authFilesCooldownResponse struct {
	ObservedAt time.Time `json:"observed_at"`
	Files      []struct {
		ID             string                    `json:"id"`
		AuthIndex      string                    `json:"auth_index"`
		Name           string                    `json:"name"`
		Status         string                    `json:"status"`
		Unavailable    bool                      `json:"unavailable"`
		NextRetryAfter time.Time                 `json:"next_retry_after"`
		Cooldowns      json.RawMessage           `json:"cooldowns"`
		Quota          map[string]any            `json:"quota"`
		ModelQuotas    map[string]map[string]any `json:"model_quotas"`
	} `json:"files"`
}

func requestAuthFilesCooldowns(t *testing.T, h *Handler, query string) authFilesCooldownResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files"+query, nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload authFilesCooldownResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	if payload.ObservedAt.IsZero() || payload.ObservedAt.Location() != time.UTC {
		t.Fatalf("invalid observed_at: %v", payload.ObservedAt)
	}
	return payload
}

func TestListAuthFilesCooldownsSnapshot(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	next := now.Add(time.Hour)
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	for _, id := range []string{"a", "b"} {
		registerAuthForLookupTest(t, manager, &coreauth.Auth{
			ID: id, Index: "index-" + id, Provider: "codex", Status: coreauth.StatusError,
			Unavailable: true, NextRetryAfter: next,
			Attributes: map[string]string{"runtime_only": "true"},
			Quota:      coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, ObservedAt: now, Signals: map[string]string{"x-codex-primary-used-percent": "90"}},
			ModelStates: map[string]*coreauth.ModelState{
				"model-a": {Unavailable: true, NextRetryAfter: next, Quota: coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 6, ObservedAt: now, Signals: map[string]string{"x-codex-primary-used-percent": "90"}}, LastError: &coreauth.Error{HTTPStatus: 429, Message: "private upstream body"}},
				"expired": {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: now.Add(-time.Hour), Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: now.Add(-time.Hour), BackoffLevel: 9}},
			},
		})
	}
	beforeA, _ := manager.GetByID("a")
	beforeB, _ := manager.GetByID("b")
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	for range 2 {
		payload := requestAuthFilesCooldowns(t, h, "")
		if len(payload.Files) != 2 {
			t.Fatalf("files = %+v", payload.Files)
		}
		for i, file := range payload.Files {
			if file.ID != []string{"a", "b"}[i] || file.AuthIndex != "index-"+file.ID {
				t.Fatalf("identity/order changed: %+v", file)
			}
			if file.Status != string(coreauth.StatusError) || !file.Unavailable || !file.NextRetryAfter.Equal(next) {
				t.Fatalf("existing state changed: %+v", file)
			}
			var views []coreauth.CooldownView
			if errDecode := json.Unmarshal(file.Cooldowns, &views); errDecode != nil {
				t.Fatal(errDecode)
			}
			if len(views) != 1 || views[0].Scope != "model" || views[0].ModelKey != "model-a" || views[0].Reason != "quota" || views[0].HTTPStatus != 429 || views[0].BackoffLevel == nil || *views[0].BackoffLevel != 6 {
				t.Fatalf("unexpected cooldowns: %s", file.Cooldowns)
			}
			remaining := next.Sub(payload.ObservedAt)
			wantSeconds := int64(remaining / time.Second)
			if remaining%time.Second != 0 {
				wantSeconds++
			}
			if views[0].RemainingSeconds != wantSeconds || !views[0].RetryAt.Equal(next) {
				t.Fatalf("inconsistent time basis: %+v", views[0])
			}
			for _, quota := range []map[string]any{file.Quota, file.ModelQuotas["model-a"]} {
				if len(quota) != 2 || quota["signals"] == nil || quota["observed_at"] == nil {
					t.Fatalf("quota observation changed: %+v", quota)
				}
			}
			var fields []map[string]any
			if errDecode := json.Unmarshal(file.Cooldowns, &fields); errDecode != nil {
				t.Fatal(errDecode)
			}
			if len(fields[0]) != 7 {
				t.Fatalf("unexpected fields: %+v", fields[0])
			}
		}
	}
	afterA, _ := manager.GetByID("a")
	afterB, _ := manager.GetByID("b")
	if !reflect.DeepEqual(beforeA, afterA) || !reflect.DeepEqual(beforeB, afterB) {
		t.Fatal("GET mutated auth state")
	}
}

func TestListAuthFilesCooldownsCredentialKindsAndFilters(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	path := filepath.Join(cfg.AuthDir, "shared.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	for _, id := range []string{"file", "virtual", "runtime"} {
		auth := &coreauth.Auth{ID: id, Index: "index-" + id, FileName: "shared.json", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"path": path}}
		if id == "virtual" {
			coreauth.MarkPluginVirtualAuth(auth, path, 0)
		}
		if id == "runtime" {
			auth.Attributes = map[string]string{"runtime_only": "true"}
		}
		registerAuthForLookupTest(t, manager, auth)
	}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=shared.json")
	if len(payload.Files) != 3 {
		t.Fatalf("files = %+v", payload.Files)
	}
	for _, file := range payload.Files {
		if string(file.Cooldowns) != "[]" {
			t.Fatalf("known empty cooldowns = %s", file.Cooldowns)
		}
		filtered := requestAuthFilesCooldowns(t, h, "?name=shared.json&auth_index="+url.QueryEscape(file.AuthIndex))
		if len(filtered.Files) != 1 || filtered.Files[0].ID != file.ID || string(filtered.Files[0].Cooldowns) != "[]" {
			t.Fatalf("filter mismatch: %+v", filtered.Files)
		}
	}
	if missing := requestAuthFilesCooldowns(t, h, "?auth_index=missing"); len(missing.Files) != 0 {
		t.Fatal("unknown index matched")
	}
}

func TestListAuthFilesCooldownsUnknown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	for _, mode := range []string{"disk", "home"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{AuthDir: t.TempDir()}
			var manager *coreauth.Manager
			if mode == "disk" {
				if errWrite := os.WriteFile(filepath.Join(cfg.AuthDir, "a.json"), []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
			} else {
				cfg.Home.Enabled = true
				manager = coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				registerAuthForLookupTest(t, manager, &coreauth.Auth{ID: "a", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"runtime_only": "true"}})
			}
			h := NewHandlerWithoutConfigFilePath(cfg, manager)
			payload := requestAuthFilesCooldowns(t, h, "")
			if len(payload.Files) != 1 || string(payload.Files[0].Cooldowns) != "null" {
				t.Fatalf("unknown cooldowns = %+v", payload.Files)
			}
			if mode == "disk" {
				if filtered := requestAuthFilesCooldowns(t, h, "?auth_index=missing"); len(filtered.Files) != 0 {
					t.Fatal("disk fallback matched index")
				}
			}
		})
	}
}
