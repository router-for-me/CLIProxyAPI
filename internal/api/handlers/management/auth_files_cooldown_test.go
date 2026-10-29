package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authFilesCooldownResponse struct {
	ObservedAt time.Time `json:"observed_at"`
	Files      []struct {
		ID             string                    `json:"id"`
		AuthIndex      string                    `json:"auth_index"`
		Name           string                    `json:"name"`
		Status         string                    `json:"status"`
		StatusMessage  string                    `json:"status_message"`
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

func TestListAuthFiles_ExpiredCooldownReconciledToActive(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Credential-level expired cooldown (e.g. Codex quota exhaustion in Issue 5964)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "codex-expired",
		Index:          "idx-codex-expired",
		FileName:       "codex-expired.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "credential_quota",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredDeadline,
			ObservedAt:    expiredDeadline,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	// Model-level expired cooldown where all model cooldowns have elapsed
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "model-expired",
		Index:          "idx-model-expired",
		FileName:       "model-expired.json",
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "rate limit exceeded",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		ModelStates: map[string]*coreauth.ModelState{
			"claude-3-5-sonnet": {
				Status:         coreauth.StatusError,
				StatusMessage:  "rate limit exceeded",
				Unavailable:    true,
				NextRetryAfter: expiredDeadline,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "")

	if len(payload.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(payload.Files))
	}

	for _, file := range payload.Files {
		if file.Unavailable {
			t.Errorf("auth %s: expected unavailable=false after cooldown expiration, got true", file.ID)
		}
		if file.Status != string(coreauth.StatusActive) {
			t.Errorf("auth %s: expected status=%q after cooldown expiration, got %q", file.ID, coreauth.StatusActive, file.Status)
		}
		if file.StatusMessage != "" {
			t.Errorf("auth %s: expected empty status_message after cooldown expiration, got %q", file.ID, file.StatusMessage)
		}
		if !file.NextRetryAfter.IsZero() {
			t.Errorf("auth %s: expected zero next_retry_after after cooldown expiration, got %v", file.ID, file.NextRetryAfter)
		}
		if string(file.Cooldowns) != "[]" {
			t.Errorf("auth %s: expected cooldowns=[], got %s", file.ID, file.Cooldowns)
		}
	}
}

func TestListAuthFiles_ExpiredCooldownWithSubsequentTokenFailure(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth auth that had a cooldown, but subsequently had its access token expire / refresh fail.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "codex-token-expired",
		Index:          "idx-codex-token-expired",
		FileName:       "codex-token-expired.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "token expired",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "expired-access-token",
			"expired":      now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=codex-token-expired.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable {
		t.Error("expected unavailable=true for expired token failure, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q for expired token failure, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "token expired" {
		t.Errorf("expected status_message=%q, got %q", "token expired", file.StatusMessage)
	}
	if !file.NextRetryAfter.IsZero() {
		t.Errorf("expected zero next_retry_after, got %v", file.NextRetryAfter)
	}
}

func TestListAuthFiles_PartialModelCooldownWithAuthFailure(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Auth has an independent auth error ("unauthorized"), but only one of its models is in cooldown.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:            "auth-unauthorized-partial-model",
		Index:         "idx-auth-unauthorized",
		FileName:      "auth-unauthorized.json",
		Provider:      "claude",
		Status:        coreauth.StatusError,
		StatusMessage: "unauthorized",
		Unavailable:   true,
		LastError:     &coreauth.Error{HTTPStatus: 401, Message: "unauthorized"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-cool": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
			"model-free": {
				Status: coreauth.StatusActive,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-unauthorized.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable {
		t.Error("expected unavailable=true due to unauthorized error, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "unauthorized" {
		t.Errorf("expected status_message=%q, got %q", "unauthorized", file.StatusMessage)
	}
}

func TestListAuthFiles_UnexpiredTokenWithRefresh401_NoCooldown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	futureExpiry := now.Add(48 * time.Hour).Format(time.RFC3339)
	retryBackoff := now.Add(5 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential whose access token is still valid (+48h), but background refresh failed with 401
	// and scheduled a retry backoff. The credential itself is still active and usable.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:               "auth-valid-token-refresh-401",
		Index:            "idx-valid-token",
		FileName:         "auth-valid-token.json",
		Provider:         "codex",
		Status:           coreauth.StatusActive,
		Unavailable:      false,
		NextRefreshAfter: retryBackoff,
		LastError:        &coreauth.Error{HTTPStatus: 401, Message: "401 unauthorized on refresh"},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-valid-token.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false for valid token with scheduled refresh retry, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_UnexpiredTokenWithRefresh401_ExpiredCooldown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	futureExpiry := now.Add(48 * time.Hour).Format(time.RFC3339)
	expiredCooldown := now.Add(-10 * time.Minute)
	retryBackoff := now.Add(5 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential with a valid access token (+48h) that previously entered quota cooldown (now expired),
	// and has a pending refresh retry after a 401 refresh error.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:               "auth-valid-token-expired-cooldown",
		Index:            "idx-valid-token-exp-cool",
		FileName:         "auth-valid-token-exp-cool.json",
		Provider:         "codex",
		Status:           coreauth.StatusError,
		StatusMessage:    "credential_quota",
		Unavailable:      true,
		NextRetryAfter:   expiredCooldown,
		NextRefreshAfter: retryBackoff,
		LastError:        &coreauth.Error{HTTPStatus: 401, Message: "401 unauthorized on refresh"},
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredCooldown,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-future-access-token",
			"expired":      futureExpiry,
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-valid-token-exp-cool.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false after quota cooldown expires for valid token, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
	if file.StatusMessage != "" {
		t.Errorf("expected empty status_message, got %q", file.StatusMessage)
	}
	if !file.NextRetryAfter.IsZero() {
		t.Errorf("expected zero next_retry_after, got %v", file.NextRetryAfter)
	}
}

func TestListAuthFiles_ModelLevel403CooldownExpired(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth whose model failed with 403 (forbidden) and the conductor copied the error to auth-level,
	// but the model-level cooldown has now elapsed.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "auth-model-403-expired",
		Index:          "idx-model-403-exp",
		FileName:       "auth-model-403-exp.json",
		Provider:       "codex",
		Status:         coreauth.StatusError,
		StatusMessage:  "forbidden",
		Unavailable:    true,
		NextRetryAfter: expiredDeadline,
		LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-a": {
				Status:         coreauth.StatusError,
				StatusMessage:  "forbidden",
				Unavailable:    true,
				NextRetryAfter: expiredDeadline,
				LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-model-403-exp.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false after model-level 403 cooldown expires, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
	if file.StatusMessage != "" {
		t.Errorf("expected empty status_message after cooldown expires, got %q", file.StatusMessage)
	}
	if !file.NextRetryAfter.IsZero() {
		t.Errorf("expected zero next_retry_after, got %v", file.NextRetryAfter)
	}
}

func TestListAuthFiles_SingleModel403Cooling_OtherModelActive(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth where Model A is in a 403 cooldown, but Model B is active and healthy.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:            "auth-single-403-other-active",
		Index:         "idx-single-403",
		FileName:      "auth-single-403.json",
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "forbidden",
		Unavailable:   false,
		LastError:     &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
		ModelStates: map[string]*coreauth.ModelState{
			"model-forbidden": {
				Status:         coreauth.StatusError,
				StatusMessage:  "forbidden",
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
				LastError:      &coreauth.Error{HTTPStatus: 403, Message: "forbidden"},
			},
			"model-working": {
				Status: coreauth.StatusActive,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-single-403.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false because model-working is active, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q because model-working is active, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_ExpiredCooldown_ExpiredToken_InFlightRefresh(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	expiredDeadline := now.Add(-10 * time.Minute)
	inFlightRefresh := now.Add(1 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An OAuth credential whose quota cooldown expired, but its access token is expired,
	// and a background refresh is currently in-flight/scheduled (NextRefreshAfter in future).
	// Because the access token is expired, selector blocks it and it must NOT be reported as active.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:               "auth-expired-cooldown-expired-token",
		Index:            "idx-exp-cool-exp-tok",
		FileName:         "auth-exp-cool-exp-tok.json",
		Provider:         "codex",
		Status:           coreauth.StatusError,
		StatusMessage:    "credential_quota",
		Unavailable:      true,
		NextRetryAfter:   expiredDeadline,
		NextRefreshAfter: inFlightRefresh,
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: expiredDeadline,
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "expired-access-token",
			"expired":      now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-exp-cool-exp-tok.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable {
		t.Error("expected unavailable=true because access token is expired, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusError, file.Status)
	}
	if file.StatusMessage != "credential_quota" {
		t.Errorf("expected status_message=%q, got %q", "credential_quota", file.StatusMessage)
	}
}

func TestListAuthFiles_ActiveAuth_InactiveFutureTimestamp(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	inactiveFutureTimestamp := now.Add(1 * time.Hour)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An active, healthy auth that has an inactive future NextRetryAfter timestamp
	// (Unavailable=false, Quota.Exceeded=false). Matching selector.availabilityBlock,
	// an inactive timestamp does not block the credential or report it as in error.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:             "auth-active-inactive-future-timestamp",
		Index:          "idx-active-inactive-future",
		FileName:       "auth-active-future.json",
		Provider:       "codex",
		Status:         coreauth.StatusActive,
		Unavailable:    false,
		NextRetryAfter: inactiveFutureTimestamp,
		Attributes:     map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-active-future.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false for active auth with inactive future timestamp, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_ModelCooling_OtherModelPermanentBlocked(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// Model A is in active cooldown (+30m).
	// Model B has a permanent failure with no recovery time (Unavailable=true, NextRetryAfter=zero).
	// Because all schedulable models are blocked, the credential as a whole cannot serve any model.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:            "auth-all-blocked-cooling-and-perm",
		Index:         "idx-all-blocked",
		FileName:      "auth-all-blocked.json",
		Provider:      "codex",
		Status:        coreauth.StatusError,
		StatusMessage: "model failure",
		Unavailable:   true,
		ModelStates: map[string]*coreauth.ModelState{
			"model-cooling": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
			"model-perm-blocked": {
				Status:      coreauth.StatusError,
				Unavailable: true,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-all-blocked.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if !file.Unavailable {
		t.Error("expected unavailable=true because all models are blocked, got false")
	}
	if file.Status != string(coreauth.StatusError) {
		t.Errorf("expected status=%q because all models are blocked, got %q", coreauth.StatusError, file.Status)
	}
}

func TestListAuthFiles_SparseModelState_BlockedModelDoesNotMakeAuthUnavailable(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	activeDeadline := now.Add(30 * time.Minute)

	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)

	// An auth where Model A has an active cooldown in ModelStates, but Model B has no entry in the sparse map yet.
	// Because other supported models are schedulable and auth.Unavailable is false, the credential as a whole
	// must NOT be reported as unavailable.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:          "auth-sparse-models",
		Index:       "idx-sparse-models",
		FileName:    "auth-sparse-models.json",
		Provider:    "codex",
		Status:      coreauth.StatusActive,
		Unavailable: false,
		ModelStates: map[string]*coreauth.ModelState{
			"model-a": {
				Status:         coreauth.StatusError,
				Unavailable:    true,
				NextRetryAfter: activeDeadline,
			},
		},
		Attributes: map[string]string{"runtime_only": "true"},
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	payload := requestAuthFilesCooldowns(t, h, "?name=auth-sparse-models.json")

	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(payload.Files))
	}

	file := payload.Files[0]
	if file.Unavailable {
		t.Error("expected unavailable=false because sparse unrecorded models remain schedulable, got true")
	}
	if file.Status != string(coreauth.StatusActive) {
		t.Errorf("expected status=%q, got %q", coreauth.StatusActive, file.Status)
	}
}

func TestListAuthFiles_V8CodexTerminalRenewalRecoveryWorkflow_Issue5783(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now().UTC()
	futureExpiry := now.Add(24 * time.Hour).Truncate(time.Second)

	manager := coreauth.NewManager(nil, nil, nil)
	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir}
	manager.SetConfig(cfg)

	exec := &failingRefreshManagementExecutor{
		provider: "codex",
		err: fmt.Errorf(
			`token refresh failed with status 401: {"error":{"message":"Your refresh token has already been used to generate a new access token. Please try signing in again. https://auth.openai.com/authorize?code=secret-code&state=secret-state refresh_token=rt-secret-123 Authorization: Bearer opaque-bearer-secret","code":"refresh_token_reused"}}`,
		),
	}
	manager.RegisterExecutor(exec)

	credPath := filepath.Join(authDir, "codex-a.json")
	if errWrite := os.WriteFile(credPath, []byte(`{"type":"codex","email":"a@example.com"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       "codex-a.json",
		Index:    "idx-codex-a",
		FileName: "codex-a.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":          "codex",
			"email":         "a@example.com",
			"account_id":    "acc-a",
			"access_token":  "valid-access-token-secret",
			"refresh_token": "rt-secret-123",
			"expired":       futureExpiry.Format(time.RFC3339),
		},
		Attributes: map[string]string{"path": credPath, "source": credPath},
	})

	// Trigger terminal refresh failure via ForceRefreshAuth while access_token is still valid.
	_, _ = manager.ForceRefreshAuth(context.Background(), "codex-a.json")

	// Ordinary successful completed inference must NOT mask the terminal renewal failure.
	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:            "codex-a.json",
		Provider:          "codex",
		Model:             "gpt-5.5",
		Success:           true,
		CompletedResponse: true,
	})

	h := NewHandlerWithoutConfigFilePath(cfg, manager)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials?name=codex-a.json", nil)
	ginCtx.Set(ConfigV8ContextKey, true)
	h.ListAuthFiles(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Files []map[string]any `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &resp); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(resp.Files))
	}
	entry := resp.Files[0]
	if entry["requires_sign_in"] != true {
		t.Fatalf("requires_sign_in = %v, want true (entry=%#v)", entry["requires_sign_in"], entry)
	}
	if entry["renewal_state"] != coreauth.RenewalStateSignInRequired {
		t.Fatalf("renewal_state = %v, want %s", entry["renewal_state"], coreauth.RenewalStateSignInRequired)
	}
	if entry["recovery_stage"] != coreauth.RecoveryStageSignInRequired {
		t.Fatalf("recovery_stage = %v, want %s", entry["recovery_stage"], coreauth.RecoveryStageSignInRequired)
	}
	if entry["access_token_expires_at"] != futureExpiry.Format(time.RFC3339) {
		t.Fatalf("access_token_expires_at = %v, want %s", entry["access_token_expires_at"], futureExpiry.Format(time.RFC3339))
	}
	if entry["next_action"] != "sign_in" {
		t.Fatalf("next_action = %v, want sign_in", entry["next_action"])
	}
	if entry["renewal_event"] == nil {
		t.Fatalf("expected deduplicated renewal_event in entry: %#v", entry)
	}
	rawBody := rec.Body.String()
	for _, forbidden := range []string{"valid-access-token-secret", "rt-secret-123", "secret-code", "secret-state", "opaque-bearer-secret", "https://auth.openai.com"} {
		if strings.Contains(rawBody, forbidden) {
			t.Fatalf("response leaked sensitive value %q in %s", forbidden, rawBody)
		}
	}

	// Targeted OAuth recovery via StartOAuthV8: cancelling or wrong provider does not mark codex-a.json repaired.
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(ConfigV8ContextKey, true)
		c.Next()
	})
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/v8/management/oauth/status", h.GetAuthStatus)
	router.DELETE("/v8/management/oauth/session", h.CancelAuthSession)
	router.POST("/v8/management/credentials/verify", h.VerifyAuthFileV8)

	wMismatch := httptest.NewRecorder()
	reqMismatch := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"claude","name":"codex-a.json"}`))
	reqMismatch.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wMismatch, reqMismatch)
	if wMismatch.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for provider mismatch, got %d: %s", wMismatch.Code, wMismatch.Body.String())
	}

	wStart := httptest.NewRecorder()
	reqStart := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"codex","name":"codex-a.json"}`))
	reqStart.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wStart, reqStart)
	if wStart.Code != http.StatusOK {
		t.Fatalf("StartOAuthV8 status = %d, want 200: %s", wStart.Code, wStart.Body.String())
	}
	var startResp map[string]any
	if errUnmarshal := json.Unmarshal(wStart.Body.Bytes(), &startResp); errUnmarshal != nil {
		t.Fatalf("unmarshal StartOAuthV8: %v", errUnmarshal)
	}
	if startResp["target_auth_id"] != "codex-a.json" {
		t.Fatalf("target_auth_id = %v, want codex-a.json", startResp["target_auth_id"])
	}
	state, _ := startResp["state"].(string)

	// Cancel login session and confirm codex-a.json still requires sign-in.
	wCancel := httptest.NewRecorder()
	router.ServeHTTP(wCancel, httptest.NewRequest(http.MethodDelete, "/v8/management/oauth/session?state="+state, nil))
	if wCancel.Code != http.StatusOK {
		t.Fatalf("CancelAuthSession status = %d, want 200", wCancel.Code)
	}
	stillBroken, _ := manager.GetByID("codex-a.json")
	if !stillBroken.RequiresSignIn {
		t.Fatal("cancelling OAuth session must not clear RequiresSignIn")
	}

	// Targeted recovery with a DIFFERENT account (other@example.com) must fail and not mark codex-a.json repaired.
	mockSvc := &targetedRecoveryTestCodexOAuthService{
		authDir:   authDir,
		email:     "other@example.com",
		accountID: "acc-other",
	}
	origFactory := newCodexOAuthService
	newCodexOAuthService = func(*config.Config) codexOAuthService { return mockSvc }
	t.Cleanup(func() { newCodexOAuthService = origFactory })

	wDiffStart := httptest.NewRecorder()
	reqDiffStart := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"codex","name":"codex-a.json"}`))
	reqDiffStart.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wDiffStart, reqDiffStart)
	if wDiffStart.Code != http.StatusOK {
		t.Fatalf("StartOAuthV8 (diff account) status = %d: %s", wDiffStart.Code, wDiffStart.Body.String())
	}
	var diffStartResp struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(wDiffStart.Body.Bytes(), &diffStartResp); errUnmarshal != nil {
		t.Fatalf("unmarshal diffStartResp: %v", errUnmarshal)
	}
	diffStatus := pollOAuthStatusUntilDone(t, router, diffStartResp.State)
	if diffStatus["status"] != "error" || !strings.Contains(fmt.Sprint(diffStatus["error"]), "does not match target credential") {
		t.Fatalf("expected account mismatch error, got %#v", diffStatus)
	}
	afterMismatch, _ := manager.GetByID("codex-a.json")
	if !afterMismatch.RequiresSignIn {
		t.Fatal("authenticating a different account must not mark target credential repaired")
	}

	// Targeted recovery with missing account_id or missing email must also be rejected (fail-closed).
	for _, missingCase := range []struct {
		name      string
		email     string
		accountID string
	}{
		{name: "missing_account_id", email: "a@example.com", accountID: ""},
		{name: "missing_email", email: "", accountID: "acc-a"},
	} {
		mockSvc.email = missingCase.email
		mockSvc.accountID = missingCase.accountID
		wMissing := httptest.NewRecorder()
		reqMissing := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"codex","name":"codex-a.json"}`))
		reqMissing.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(wMissing, reqMissing)
		if wMissing.Code != http.StatusOK {
			t.Fatalf("StartOAuthV8 (%s) status = %d: %s", missingCase.name, wMissing.Code, wMissing.Body.String())
		}
		var missingStartResp struct {
			State string `json:"state"`
		}
		if errUnmarshal := json.Unmarshal(wMissing.Body.Bytes(), &missingStartResp); errUnmarshal != nil {
			t.Fatalf("unmarshal missingStartResp (%s): %v", missingCase.name, errUnmarshal)
		}
		missingStatus := pollOAuthStatusUntilDone(t, router, missingStartResp.State)
		if missingStatus["status"] != "error" || !strings.Contains(fmt.Sprint(missingStatus["error"]), "does not match target credential") {
			t.Fatalf("%s: expected mismatch error, got %#v", missingCase.name, missingStatus)
		}
		afterMissing, _ := manager.GetByID("codex-a.json")
		if !afterMissing.RequiresSignIn {
			t.Fatalf("%s: missing identity field must not mark target credential repaired", missingCase.name)
		}
	}

	// If saving to storage succeeds but runtime adoption fails, GetAuthStatus must report saved=true, adopted=false, recovery_stage=credential_saved.
	mockSvc.email = "a@example.com"
	mockSvc.accountID = "acc-a"
	h.SetPostAuthPersistHook(func(context.Context, *coreauth.Auth) error {
		return fmt.Errorf("simulated runtime adoption failure")
	})
	wUnadoptedStart := httptest.NewRecorder()
	reqUnadoptedStart := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"codex","name":"codex-a.json"}`))
	reqUnadoptedStart.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wUnadoptedStart, reqUnadoptedStart)
	if wUnadoptedStart.Code != http.StatusOK {
		t.Fatalf("StartOAuthV8 (unadopted) status = %d: %s", wUnadoptedStart.Code, wUnadoptedStart.Body.String())
	}
	var unadoptedStartResp struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(wUnadoptedStart.Body.Bytes(), &unadoptedStartResp); errUnmarshal != nil {
		t.Fatalf("unmarshal unadoptedStartResp: %v", errUnmarshal)
	}
	unadoptedStatus := pollOAuthStatusUntilDone(t, router, unadoptedStartResp.State)
	if unadoptedStatus["status"] != "ok" || unadoptedStatus["saved"] != true || unadoptedStatus["adopted"] != false || unadoptedStatus["recovery_stage"] != coreauth.RecoveryStageCredentialSaved {
		t.Fatalf("expected saved=true adopted=false recovery_stage=credential_saved when runtime adoption fails, got %#v", unadoptedStatus)
	}
	// An unrelated runtime update or replacement with a different grant must NOT falsely satisfy adoption for unadoptedStartResp.State.
	unrelatedReplacement := stillBroken.Clone()
	unrelatedReplacement.Metadata["access_token"] = "unrelated-access-token"
	unrelatedReplacement.Metadata["refresh_token"] = "unrelated-refresh-token"
	if _, errUnrelated := manager.Update(coreauth.WithSkipPersist(context.Background()), unrelatedReplacement); errUnrelated != nil {
		t.Fatalf("Update unrelatedReplacement: %v", errUnrelated)
	}
	unrelatedStatus := pollOAuthStatusUntilDone(t, router, unadoptedStartResp.State)
	if unrelatedStatus["adopted"] != false || unrelatedStatus["recovery_stage"] != coreauth.RecoveryStageCredentialSaved {
		t.Fatalf("unrelated credential update must not mark OAuth session adopted, got %#v", unrelatedStatus)
	}
	// When a delayed watcher/reload subsequently adopts the actual saved file into manager AND an automatic refresh rotates
	// the tokens BEFORE GetAuthStatus is polled again, polling GetAuthStatus must still report adopted=true and renewal_verified=true.
	if errDelayedReg := h.registerAuthFromFile(context.Background(), credPath, nil); errDelayedReg != nil {
		t.Fatalf("delayed registerAuthFromFile error: %v", errDelayedReg)
	}
	exec.err = nil
	exec.rotateTokens = true
	if _, errDelayedRot := manager.ForceRefreshAuth(coreauth.WithSkipPersist(context.Background()), "codex-a.json"); errDelayedRot != nil {
		t.Fatalf("ForceRefreshAuth after delayed adoption error: %v", errDelayedRot)
	}
	exec.rotateTokens = false
	delayedStatus := pollOAuthStatusUntilDone(t, router, unadoptedStartResp.State)
	if delayedStatus["status"] != "ok" || delayedStatus["saved"] != true || delayedStatus["adopted"] != true || delayedStatus["renewal_verified"] != true || delayedStatus["recovery_stage"] != coreauth.RecoveryStageRenewalVerified {
		t.Fatalf("expected delayed adoption + rotation to transition GetAuthStatus to adopted=true renewal_verified=true recovery_stage=renewal_verified, got %#v", delayedStatus)
	}
	h.SetPostAuthPersistHook(nil)

	// Targeted recovery with the MATCHING account (a@example.com / acc-a) saves fresh tokens, adopts into manager, and clears RequiresSignIn.
	wMatchStart := httptest.NewRecorder()
	reqMatchStart := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"codex","name":"codex-a.json"}`))
	reqMatchStart.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wMatchStart, reqMatchStart)
	if wMatchStart.Code != http.StatusOK {
		t.Fatalf("StartOAuthV8 (matching account) status = %d: %s", wMatchStart.Code, wMatchStart.Body.String())
	}
	var matchStartResp struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(wMatchStart.Body.Bytes(), &matchStartResp); errUnmarshal != nil {
		t.Fatalf("unmarshal matchStartResp: %v", errUnmarshal)
	}
	matchStatus := pollOAuthStatusUntilDone(t, router, matchStartResp.State)
	if matchStatus["status"] != "ok" || matchStatus["auth_id"] != "codex-a.json" || matchStatus["saved"] != true || matchStatus["adopted"] != true {
		t.Fatalf("expected saved & adopted ok status for codex-a.json, got %#v", matchStatus)
	}
	if matchStatus["requires_sign_in"] != false || matchStatus["renewal_state"] != coreauth.RenewalStateAwaitingFirstRenewal || matchStatus["recovery_stage"] != coreauth.RecoveryStageLoaded || matchStatus["inference_verified"] != false || matchStatus["renewal_verified"] != false {
		t.Fatalf("unexpected post-recovery status fields: %#v", matchStatus)
	}
	repaired, _ := manager.GetByID("codex-a.json")
	if repaired.RequiresSignIn || repaired.RefreshError != nil {
		t.Fatalf("expected codex-a.json repaired in manager, got RequiresSignIn=%v RefreshError=%+v", repaired.RequiresSignIn, repaired.RefreshError)
	}

	// Operator-triggered POST /v8/management/credentials/verify:
	// 1) response.incomplete must fail (HTTP 502) and NOT mark inference_verified=true.
	exec.streamPayloads = [][]byte{[]byte(`data: {"type":"response.incomplete","response":{"status":"incomplete"}}` + "\n\n")}
	wVerifyIncomp := httptest.NewRecorder()
	reqVerifyIncomp := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/verify", strings.NewReader(`{"name":"codex-a.json","model":"gpt-5.5"}`))
	reqVerifyIncomp.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wVerifyIncomp, reqVerifyIncomp)
	if wVerifyIncomp.Code != http.StatusBadGateway {
		t.Fatalf("VerifyAuthFileV8 (incomplete) status = %d, want 502: %s", wVerifyIncomp.Code, wVerifyIncomp.Body.String())
	}

	// 1b) 503 transient failure must NOT be misclassified as quota_blocked.
	exec.streamPayloads = nil
	exec.streamErr = &coreauth.Error{Code: "service_unavailable", Message: "upstream 503", HTTPStatus: http.StatusServiceUnavailable, Retryable: true}
	wVerify503 := httptest.NewRecorder()
	reqVerify503 := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/verify", strings.NewReader(`{"name":"codex-a.json","model":"gpt-5.5"}`))
	reqVerify503.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wVerify503, reqVerify503)
	if wVerify503.Code != http.StatusServiceUnavailable {
		t.Fatalf("VerifyAuthFileV8 (503) status = %d, want 503: %s", wVerify503.Code, wVerify503.Body.String())
	}
	var verify503Resp map[string]any
	if errUnmarshal := json.Unmarshal(wVerify503.Body.Bytes(), &verify503Resp); errUnmarshal != nil {
		t.Fatalf("unmarshal verify503Resp: %v", errUnmarshal)
	}
	if verify503Resp["quota_blocked"] != false || verify503Resp["recovery_stage"] == coreauth.RecoveryStageQuotaBlocked {
		t.Fatalf("503 transient failure must not be classified as quota_blocked, got %#v", verify503Resp)
	}
	_, _, _ = manager.ResetQuota(context.Background(), "codex-a.json")

	// 2) 429 quota error after valid auth must transition recovery_stage to quota_blocked and sanitize any secrets in the error message.
	exec.streamPayloads = nil
	exec.streamErr = &coreauth.Error{
		Code:       "rate_limit_exceeded",
		Message:    "You have hit your usage limit https://chatgpt.com/backend-api/codex?code=secret-verify-code&state=secret-verify-state Bearer secret-verify-bearer",
		HTTPStatus: http.StatusTooManyRequests,
	}
	wVerifyQuota := httptest.NewRecorder()
	reqVerifyQuota := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/verify", strings.NewReader(`{"name":"codex-a.json","model":"gpt-5.5"}`))
	reqVerifyQuota.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wVerifyQuota, reqVerifyQuota)
	if wVerifyQuota.Code != http.StatusTooManyRequests {
		t.Fatalf("VerifyAuthFileV8 (quota) status = %d, want 429: %s", wVerifyQuota.Code, wVerifyQuota.Body.String())
	}
	for _, secret := range []string{"secret-verify-code", "secret-verify-state", "secret-verify-bearer", "https://chatgpt.com"} {
		if strings.Contains(wVerifyQuota.Body.String(), secret) {
			t.Fatalf("VerifyAuthFileV8 response leaked secret %q: %s", secret, wVerifyQuota.Body.String())
		}
	}
	var quotaVerifyResp map[string]any
	if errUnmarshal := json.Unmarshal(wVerifyQuota.Body.Bytes(), &quotaVerifyResp); errUnmarshal != nil {
		t.Fatalf("unmarshal quotaVerifyResp: %v", errUnmarshal)
	}
	if quotaVerifyResp["recovery_stage"] != coreauth.RecoveryStageQuotaBlocked || quotaVerifyResp["quota_blocked"] != true {
		t.Fatalf("expected recovery_stage=quota_blocked and quota_blocked=true, got %#v", quotaVerifyResp)
	}
	_, _, _ = manager.ResetQuota(context.Background(), "codex-a.json")

	// 3) Completed stream transitions recovery_stage to inference_verified.
	exec.streamErr = nil
	exec.streamPayloads = [][]byte{[]byte(`data: {"type":"response.completed"}` + "\n\n")}
	wVerifyOK := httptest.NewRecorder()
	reqVerifyOK := httptest.NewRequest(http.MethodPost, "/v8/management/credentials/verify", strings.NewReader(`{"name":"codex-a.json","model":"gpt-5.5"}`))
	reqVerifyOK.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wVerifyOK, reqVerifyOK)
	if wVerifyOK.Code != http.StatusOK {
		t.Fatalf("VerifyAuthFileV8 (ok) status = %d, want 200: %s", wVerifyOK.Code, wVerifyOK.Body.String())
	}
	var okVerifyResp map[string]any
	if errUnmarshal := json.Unmarshal(wVerifyOK.Body.Bytes(), &okVerifyResp); errUnmarshal != nil {
		t.Fatalf("unmarshal okVerifyResp: %v", errUnmarshal)
	}
	if okVerifyResp["verified"] != true || okVerifyResp["inference_verified"] != true || okVerifyResp["recovery_stage"] != coreauth.RecoveryStageInferenceVerified {
		t.Fatalf("expected verified=true inference_verified=true recovery_stage=inference_verified, got %#v", okVerifyResp)
	}

	// 3b) After a subsequent automatic refresh rotates tokens on the adopted credential, GetAuthStatus for matchStartResp.State
	// must still report adopted=true and transition to renewal_verified=true / recovery_stage=renewal_verified.
	exec.err = nil
	exec.rotateTokens = true
	if _, errForceRefresh := manager.ForceRefreshAuth(coreauth.WithSkipPersist(context.Background()), "codex-a.json"); errForceRefresh != nil {
		t.Fatalf("ForceRefreshAuth after recovery error: %v", errForceRefresh)
	}
	rotatedStatus := pollOAuthStatusUntilDone(t, router, matchStartResp.State)
	if rotatedStatus["adopted"] != true || rotatedStatus["renewal_verified"] != true || rotatedStatus["recovery_stage"] != coreauth.RecoveryStageRenewalVerified {
		t.Fatalf("expected adopted=true renewal_verified=true recovery_stage=renewal_verified after token rotation, got %#v", rotatedStatus)
	}

	// 3c) If the adopted credential is subsequently replaced via Update by an unrelated credential
	// and that replacement credential is refreshed, the original OAuth session must NOT claim the replacement's renewal.
	currentAdopted, _ := manager.GetByID("codex-a.json")
	postRotationUnrelated := currentAdopted.Clone()
	postRotationUnrelated.Metadata["access_token"] = "unrelated-access-token"
	postRotationUnrelated.Metadata["refresh_token"] = "unrelated-refresh-token"
	if _, errUpdUnrelated := manager.Update(coreauth.WithSkipPersist(context.Background()), postRotationUnrelated); errUpdUnrelated != nil {
		t.Fatalf("Update postRotationUnrelated error: %v", errUpdUnrelated)
	}
	if _, errForceUnrelated := manager.ForceRefreshAuth(coreauth.WithSkipPersist(context.Background()), "codex-a.json"); errForceUnrelated != nil {
		t.Fatalf("ForceRefreshAuth unrelatedReplacement error: %v", errForceUnrelated)
	}
	replacedAfterRotationStatus := pollOAuthStatusUntilDone(t, router, matchStartResp.State)
	if replacedAfterRotationStatus["adopted"] != false || strings.TrimSpace(fmt.Sprint(replacedAfterRotationStatus["adoption_error"])) == "" {
		t.Fatalf("expected adopted=false and adoption_error after unrelated credential replacement + refresh, got %#v", replacedAfterRotationStatus)
	}

	// 4) Opt-in post-login verification (verify=true on StartOAuthV8) sanitizes verification_error in GetAuthStatus.
	exec.streamPayloads = nil
	exec.streamErr = &coreauth.Error{
		Code:       "rate_limit_exceeded",
		Message:    "Quota error https://chatgpt.com/backend-api/codex?code=secret-postlogin-code&state=secret-postlogin-state Bearer secret-postlogin-bearer",
		HTTPStatus: http.StatusTooManyRequests,
	}
	wVerifyOptIn := httptest.NewRecorder()
	reqVerifyOptIn := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"codex","name":"codex-a.json","verify":true,"verify_model":"gpt-5.5"}`))
	reqVerifyOptIn.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wVerifyOptIn, reqVerifyOptIn)
	if wVerifyOptIn.Code != http.StatusOK {
		t.Fatalf("StartOAuthV8 (verify=true) status = %d: %s", wVerifyOptIn.Code, wVerifyOptIn.Body.String())
	}
	var verifyOptInStartResp struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(wVerifyOptIn.Body.Bytes(), &verifyOptInStartResp); errUnmarshal != nil {
		t.Fatalf("unmarshal verifyOptInStartResp: %v", errUnmarshal)
	}
	optInStatus := pollOAuthStatusUntilDone(t, router, verifyOptInStartResp.State)
	if optInStatus["status"] != "ok" || optInStatus["adopted"] != true || optInStatus["quota_blocked"] != true {
		t.Fatalf("expected adopted=true quota_blocked=true after opt-in verify quota error, got %#v", optInStatus)
	}
	verifyErrText := fmt.Sprint(optInStatus["verification_error"])
	if verifyErrText == "" {
		t.Fatalf("expected sanitized verification_error in status response, got %#v", optInStatus)
	}
	for _, secret := range []string{"secret-postlogin-code", "secret-postlogin-state", "secret-postlogin-bearer", "https://chatgpt.com"} {
		if strings.Contains(verifyErrText, secret) {
			t.Fatalf("GetAuthStatus verification_error leaked secret %q: %s", secret, verifyErrText)
		}
	}
}

type targetedRecoveryTestCodexOAuthService struct {
	authDir   string
	email     string
	accountID string
}

func (s *targetedRecoveryTestCodexOAuthService) GenerateAuthURL(state string, _ *codex.PKCECodes) (string, error) {
	waitFile := filepath.Join(s.authDir, fmt.Sprintf(".oauth-codex-%s.oauth", state))
	payload, _ := json.Marshal(map[string]string{"state": state, "code": "code-" + state})
	_ = os.WriteFile(waitFile, payload, 0o600)
	return "https://auth.example.test/authorize?state=" + state, nil
}

func (s *targetedRecoveryTestCodexOAuthService) ExchangeCodeForTokens(_ context.Context, code string, _ *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	now := time.Now().UTC()
	return &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      makeOAuthTestJWT("plus"),
			AccessToken:  "fresh-access-" + code,
			RefreshToken: "fresh-refresh-" + code,
			AccountID:    s.accountID,
			Email:        s.email,
			PlanType:     "plus",
			Expire:       now.Add(48 * time.Hour).Format(time.RFC3339),
		},
		LastRefresh: now.Format(time.RFC3339),
	}, nil
}

func (s *targetedRecoveryTestCodexOAuthService) CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage {
	return &codex.CodexTokenStorage{
		IDToken:      bundle.TokenData.IDToken,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
		PlanType:     bundle.TokenData.PlanType,
	}
}

func pollOAuthStatusUntilDone(t *testing.T, router http.Handler, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v8/management/oauth/status?state="+state, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("oauth status HTTP %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if errUnmarshal := json.Unmarshal(w.Body.Bytes(), &out); errUnmarshal != nil {
			t.Fatalf("unmarshal oauth status: %v", errUnmarshal)
		}
		if out["status"] != "wait" {
			return out
		}
	}
	t.Fatalf("timed out waiting for oauth session %s", state)
	return nil
}

type failingRefreshManagementExecutor struct {
	provider       string
	err            error
	rotateTokens   bool
	streamErr      error
	streamPayloads [][]byte
}

func (e *failingRefreshManagementExecutor) Identifier() string { return e.provider }

func (e *failingRefreshManagementExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *failingRefreshManagementExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.streamErr != nil {
		return nil, e.streamErr
	}
	ch := make(chan cliproxyexecutor.StreamChunk, len(e.streamPayloads))
	for _, p := range e.streamPayloads {
		ch <- cliproxyexecutor.StreamChunk{Payload: p}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *failingRefreshManagementExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.rotateTokens && auth != nil {
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["access_token"] = "rotated-access-token"
		auth.Metadata["refresh_token"] = "rotated-refresh-token"
	}
	return auth, nil
}

func (e *failingRefreshManagementExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *failingRefreshManagementExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
