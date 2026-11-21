package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestGetAuthFileModelsV8ByAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	const (
		keyID  = "models-index-config-key"
		fileID = "models-index-file"
	)
	manager := coreauth.NewManager(nil, nil, nil)
	// An API key from config.yaml has no FileName.
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       keyID,
		Index:    "idx-config-key",
		Provider: "gemini",
		Status:   coreauth.StatusActive,
	})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       fileID,
		Index:    "idx-file",
		FileName: "models-index-file.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	})
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(keyID, "gemini", []*registry.ModelInfo{{ID: "key-model"}})
	reg.RegisterClient(fileID, "codex", []*registry.ModelInfo{{ID: "file-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(keyID)
		reg.UnregisterClient(fileID)
	})
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantModels []string
		wantError  string
	}{
		{"auth_index of a credential without file", "auth_index=idx-config-key", http.StatusOK, []string{"key-model"}, ""},
		{"name", "name=models-index-file.json", http.StatusOK, []string{"file-model"}, ""},
		{"name wins over auth_index", "name=models-index-file.json&auth_index=idx-config-key", http.StatusOK, []string{"file-model"}, ""},
		{"unknown name", "name=missing.json", http.StatusOK, []string{}, ""},
		{"unknown auth_index", "auth_index=idx-missing", http.StatusOK, []string{}, ""},
		{"neither", "", http.StatusBadRequest, nil, "name or auth_index is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials/models?"+tt.query, nil)

			h.GetAuthFileModelsV8(ctx)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var resp struct {
				Models []struct {
					ID string `json:"id"`
				} `json:"models"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode body %s: %v", rec.Body.String(), err)
			}
			if resp.Error != tt.wantError {
				t.Fatalf("error = %q, want %q", resp.Error, tt.wantError)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			got := make([]string, 0, len(resp.Models))
			for _, m := range resp.Models {
				got = append(got, m.ID)
			}
			if len(got) != len(tt.wantModels) || (len(got) == 1 && got[0] != tt.wantModels[0]) {
				t.Fatalf("models = %v, want %v", got, tt.wantModels)
			}
		})
	}
}

// /v0/management is frozen: auth_index alone is still rejected there.
func TestGetAuthFileModelsV0IgnoresAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       "models-v0-config-key",
		Index:    "idx-v0-config-key",
		Provider: "gemini",
		Status:   coreauth.StatusActive,
	})
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?auth_index=idx-v0-config-key", nil)

	h.GetAuthFileModels(ctx)

	if rec.Code != http.StatusBadRequest || rec.Body.String() != `{"error":"name is required"}` {
		t.Fatalf("status = %d body=%s, want 400 {\"error\":\"name is required\"}", rec.Code, rec.Body.String())
	}
}
