package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quota"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCredentialQuotaSnapshotRestoresWithoutCallingProvider(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "quota-auth", FileName: "quota.json", Provider: "claude", Metadata: map[string]any{"email": "fixture@example.invalid", "access_token": "secret"}}
	auth.EnsureIndex()
	if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	service, _ := quota.New(nil, func() time.Time { return now })
	t.Cleanup(service.Close)
	identity := quotaCredentialIdentity(auth)
	request := quota.Request{Identity: identity, Resource: "usage", Interval: quota.UsageInterval,
		Fetch: func(context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"five_hour":{"utilization":25}}`), nil
		},
	}
	if _, errObserve := service.Observe(t.Context(), request); errObserve != nil {
		t.Fatal(errObserve)
	}
	h := &Handler{cfg: &config.Config{}, authManager: manager, quotaObservations: service}
	read := func(index string) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials/quota?auth_index="+index+"&resource=usage", nil)
		h.CredentialQuotaObservation(c)
		if recorder.Code != http.StatusOK {
			t.Fatalf("credential errors must not trigger management logout: %d", recorder.Code)
		}
		var result map[string]any
		if errDecode := json.Unmarshal(recorder.Body.Bytes(), &result); errDecode != nil {
			t.Fatal(errDecode)
		}
		if strings.Contains(recorder.Body.String(), "secret") {
			t.Fatal("credential leaked through snapshot API")
		}
		return result
	}
	result := read(auth.Index)
	if result["status_code"] != float64(200) || result["observed_at"] != now.Format(time.RFC3339) {
		t.Fatalf("cached observation: %v", result)
	}
	now = now.Add(quota.UsageInterval)
	request.Force = true
	request.Fetch = func(context.Context) (json.RawMessage, error) { return nil, &quota.FetchError{Status: 401} }
	if _, errObserve := service.Observe(t.Context(), request); errObserve != nil {
		t.Fatal(errObserve)
	}
	result = read(auth.Index)
	if result["status_code"] != float64(200) || result["error_status"] != float64(401) || result["stale"] != true || result["body"] == nil {
		t.Fatalf("failure lost cached data: %v", result)
	}
	if missing := read("missing"); missing["status_code"] != float64(404) {
		t.Fatalf("missing credential: %v", missing)
	}
	manager.Remove(t.Context(), auth.ID)
	h.reconcileQuotaObservations()
	request.CachedOnly = true
	if removed, errObserve := service.Observe(t.Context(), request); errObserve != nil || len(removed.Body) != 0 {
		t.Fatalf("deleted credential retained its snapshot: %s, %v", removed.Body, errObserve)
	}
}

func TestQuotaCredentialIdentitySurvivesTokenRotationAndRejectsAccountReplacement(t *testing.T) {
	auth := &coreauth.Auth{ID: "same-file", Provider: "claude", Metadata: map[string]any{"email": "first@example.invalid", "access_token": "first-token", "refresh_token": "first-refresh"}}
	first := quotaCredentialIdentity(auth)
	auth.Metadata["access_token"], auth.Metadata["refresh_token"] = "next-token", "next-refresh"
	if next := quotaCredentialIdentity(auth); next != first {
		t.Fatal("OAuth rotation invalidated the account identity")
	}
	auth.Metadata["email"] = "second@example.invalid"
	if next := quotaCredentialIdentity(auth); next == first {
		t.Fatal("same-name account replacement reused observations")
	}
}

func TestResetQuota_UsesAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	next := time.Now().Add(time.Hour)
	auth := &coreauth.Auth{
		ID:             "reset-auth-id",
		FileName:       "reset-auth-file.json",
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
		ModelStates: map[string]*coreauth.ModelState{
			"claude-reset-model": {
				Status:         coreauth.StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
			},
		},
	}
	authIndex := auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/reset-quota", strings.NewReader(`{"auth_index":"`+authIndex+`"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.ResetQuota(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode response: %v", errUnmarshal)
	}
	if payload["auth_index"] != authIndex {
		t.Fatalf("auth_index = %#v, want %q", payload["auth_index"], authIndex)
	}

	updated, ok := manager.GetByID("reset-auth-id")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after reset")
	}
	if updated.Status != coreauth.StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated auth state = status %q message %q unavailable %v next %v", updated.Status, updated.StatusMessage, updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() || updated.Quota.BackoffLevel != 0 {
		t.Fatalf("updated auth quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates["claude-reset-model"]
	if state == nil {
		t.Fatalf("expected model state to remain")
	}
	if state.Status != coreauth.StatusActive || state.StatusMessage != "" || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("updated model state = status %q message %q unavailable %v next %v", state.Status, state.StatusMessage, state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("updated model quota = %+v, want cleared", state.Quota)
	}
}

func TestResetQuota_DoesNotAcceptAuthIDOrFileName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "reset-auth-id-only",
		FileName: "reset-auth-file-only.json",
		Provider: "claude",
		Status:   coreauth.StatusError,
	}
	authIndex := auth.EnsureIndex()
	if authIndex == auth.ID || authIndex == auth.FileName {
		t.Fatalf("test auth_index unexpectedly matches id or file name: %q", authIndex)
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{name: "auth_id field ignored", body: `{"auth_id":"reset-auth-id-only"}`, wantCode: http.StatusBadRequest},
		{name: "id field ignored", body: `{"id":"reset-auth-id-only"}`, wantCode: http.StatusBadRequest},
		{name: "file name is not an index", body: `{"auth_index":"reset-auth-file-only.json"}`, wantCode: http.StatusNotFound},
		{name: "auth id is not an index", body: `{"auth_index":"reset-auth-id-only"}`, wantCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPost, "/v0/management/reset-quota", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			h.ResetQuota(ctx)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d with body %s", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}
