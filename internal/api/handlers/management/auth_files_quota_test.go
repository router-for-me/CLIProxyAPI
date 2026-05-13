package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFilesIncludesQuotaAndModelStates(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	nextRecover := time.Unix(1_800_000_000, 0).UTC()
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	record := &coreauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		Status:   coreauth.StatusError,
		Attributes: map[string]string{
			"path": "/tmp/codex.json",
		},
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: nextRecover,
			BackoffLevel:  2,
		},
		ModelStates: map[string]*coreauth.ModelState{
			"gpt-5.5-codex": {
				Status:         coreauth.StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: nextRecover,
				Quota: coreauth.QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRecover,
					BackoffLevel:  2,
				},
				UpdatedAt: nextRecover,
			},
		},
	}
	if _, errRegister := manager.Register(t.Context(), record); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)

	h.ListAuthFiles(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Files []struct {
			Quota       coreauth.QuotaState            `json:"quota"`
			ModelStates map[string]coreauth.ModelState `json:"model_states"`
		} `json:"files"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v; body=%s", errDecode, rec.Body.String())
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(payload.Files))
	}
	file := payload.Files[0]
	if !file.Quota.Exceeded || file.Quota.Reason != "quota" || file.Quota.BackoffLevel != 2 {
		t.Fatalf("quota = %+v, want exceeded quota with backoff 2", file.Quota)
	}
	state, ok := file.ModelStates["gpt-5.5-codex"]
	if !ok {
		t.Fatalf("expected gpt-5.5-codex model state, got %+v", file.ModelStates)
	}
	if !state.Quota.Exceeded || state.StatusMessage != "quota exhausted" || !state.Unavailable {
		t.Fatalf("model state = %+v, want unavailable quota exhausted state", state)
	}
}
