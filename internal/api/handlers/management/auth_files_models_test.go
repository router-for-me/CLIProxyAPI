package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetAuthFileModelsIncludesGrokCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const authID = "grok-management-auth-capabilities"
	const filename = "grok-management-auth-capabilities.json"

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "grok",
		FileName: filename,
		Status:   coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	registry.UnregisterAuth(authID)
	t.Cleanup(func() { registry.UnregisterAuth(authID) })
	reg.RegisterClient(authID, "grok", []*registry.ModelInfo{
		{
			ID:                  "grok-code-fast-1",
			Object:              "model",
			OwnedBy:             "xai",
			Type:                "grok",
			DisplayName:         "Grok Code Fast 1",
			ContextLength:       131072,
			MaxCompletionTokens: 16384,
			SupportedParameters: []string{"tools"},
		},
	})

	h := &Handler{authManager: manager}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/models?name="+url.QueryEscape(filename), nil)

	h.GetAuthFileModels(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Models []struct {
			ID                  string   `json:"id"`
			Type                string   `json:"type"`
			OwnedBy             string   `json:"owned_by"`
			ContextLength       int      `json:"context_length"`
			MaxCompletionTokens int      `json:"max_completion_tokens"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Models) != 1 {
		t.Fatalf("expected one model, got %#v", body.Models)
	}
	model := body.Models[0]
	if model.ID != "grok-code-fast-1" || model.Type != "grok" || model.OwnedBy != "xai" {
		t.Fatalf("unexpected model identity: %#v", model)
	}
	if model.ContextLength != 131072 || model.MaxCompletionTokens != 16384 {
		t.Fatalf("missing model limits in management response: %#v", model)
	}
	if !slices.Contains(model.SupportedParameters, "tools") {
		t.Fatalf("expected tools support in management response, got %v", model.SupportedParameters)
	}
}
