package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func newBedrockConfigListHandler(t *testing.T, entries []config.AWSBedrockKey) *Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		AWSBedrockKey: append([]config.AWSBedrockKey(nil), entries...),
	}
	cfg.SanitizeAWSBedrockKeys()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\naws-bedrock-api-key: []\n"), 0o600); err != nil {
		t.Fatalf("write temp config file: %v", err)
	}
	return NewHandler(cfg, configPath, nil)
}

func TestPatchAWSBedrockKey_AmbiguousMatchReturnsConflict(t *testing.T) {
	h := newBedrockConfigListHandler(t, []config.AWSBedrockKey{
		{APIKey: "shared-key", Region: "us-east-1", Priority: 1},
		{APIKey: "shared-key", Region: "eu-west-1", Priority: 2},
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/aws-bedrock-api-key", strings.NewReader(`{"match":"shared-key","value":{"priority":9}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchAWSBedrockKey(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if got := h.cfg.AWSBedrockKey[0].Priority; got != 1 {
		t.Fatalf("entry[0].priority = %d, want %d", got, 1)
	}
	if got := h.cfg.AWSBedrockKey[1].Priority; got != 2 {
		t.Fatalf("entry[1].priority = %d, want %d", got, 2)
	}
}

func TestPatchAWSBedrockKey_CompositeMatchUpdatesExactEntry(t *testing.T) {
	h := newBedrockConfigListHandler(t, []config.AWSBedrockKey{
		{APIKey: "shared-key", Region: "us-east-1", Priority: 1},
		{APIKey: "shared-key", Region: "eu-west-1", Priority: 2},
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodPatch,
		"/v0/management/aws-bedrock-api-key",
		strings.NewReader(`{"match":"shared-key","region":"eu-west-1","value":{"priority":9}}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchAWSBedrockKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.AWSBedrockKey[0].Priority; got != 1 {
		t.Fatalf("entry[0].priority = %d, want %d", got, 1)
	}
	if got := h.cfg.AWSBedrockKey[1].Priority; got != 9 {
		t.Fatalf("entry[1].priority = %d, want %d", got, 9)
	}
}

func TestDeleteAWSBedrockKey_AmbiguousAPIKeyReturnsConflict(t *testing.T) {
	h := newBedrockConfigListHandler(t, []config.AWSBedrockKey{
		{APIKey: "shared-key", Region: "us-east-1"},
		{APIKey: "shared-key", Region: "eu-west-1"},
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/aws-bedrock-api-key?api-key=shared-key", nil)

	h.DeleteAWSBedrockKey(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if got := len(h.cfg.AWSBedrockKey); got != 2 {
		t.Fatalf("len(aws-bedrock-api-key) = %d, want %d", got, 2)
	}
}

func TestDeleteAWSBedrockKey_CompositeSelectorDeletesOnlyOneEntry(t *testing.T) {
	h := newBedrockConfigListHandler(t, []config.AWSBedrockKey{
		{APIKey: "shared-key", Region: "us-east-1"},
		{APIKey: "shared-key", Region: "eu-west-1"},
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/aws-bedrock-api-key?api-key=shared-key&region=eu-west-1", nil)

	h.DeleteAWSBedrockKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.AWSBedrockKey); got != 1 {
		t.Fatalf("len(aws-bedrock-api-key) = %d, want %d", got, 1)
	}
	remaining := h.cfg.AWSBedrockKey[0]
	if remaining.Region != "us-east-1" {
		t.Fatalf("remaining region = %q, want %q", remaining.Region, "us-east-1")
	}
}
