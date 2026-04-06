package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestGetAuthFileModelsReturnsDynamicQwenModels_WhenAuthFileNameIsAbsolutePath(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("qwen-auth", "qwen", []*registry.ModelInfo{
		{ID: "qwen3.6-plus", DisplayName: "Qwen3.6-Plus"},
	})
	defer reg.UnregisterClient("qwen-auth")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "qwen-auth",
		FileName: "/tmp/qwen-user.json",
		Provider: "qwen",
	}); err != nil {
		t.Fatalf("failed to register auth record: %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/mgmt/auth-files/models?name=qwen-user.json", nil)

	handler.GetAuthFileModels(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, rec.Body.String())
	}

	found := false
	for _, m := range payload.Models {
		if m.ID == "qwen3.6-plus" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("models=%#v, want model %q", payload.Models, "qwen3.6-plus")
	}
}

func TestGetAuthFileModelsReturnsDynamicQwenModels_WhenQueryNameIsAbsolutePath(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("qwen-auth", "qwen", []*registry.ModelInfo{
		{ID: "qwen3.6-plus", DisplayName: "Qwen3.6-Plus"},
	})
	defer reg.UnregisterClient("qwen-auth")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "qwen-auth",
		FileName: "qwen-user.json",
		Provider: "qwen",
	}); err != nil {
		t.Fatalf("failed to register auth record: %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/mgmt/auth-files/models?name=/tmp/qwen-user.json", nil)

	handler.GetAuthFileModels(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, rec.Body.String())
	}

	found := false
	for _, m := range payload.Models {
		if m.ID == "qwen3.6-plus" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("models=%#v, want model %q", payload.Models, "qwen3.6-plus")
	}
}

func TestGetAuthFileModelsIncludesQwenStatusMetadataWhenAvailable(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("qwen-auth", "qwen", []*registry.ModelInfo{
		{ID: "qwen3.6-plus", DisplayName: "Qwen3.6-Plus"},
	})
	defer reg.UnregisterClient("qwen-auth")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "qwen-auth",
		FileName: "qwen-user.json",
		Provider: "qwen",
		Metadata: map[string]any{
			"qwen_status": map[string]any{
				"available":   true,
				"model_count": 1,
			},
		},
	}); err != nil {
		t.Fatalf("failed to register auth record: %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/mgmt/auth-files/models?name=qwen-user.json", nil)

	handler.GetAuthFileModels(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
		Status map[string]any `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, rec.Body.String())
	}
	if len(payload.Models) != 1 || payload.Models[0].ID != "qwen3.6-plus" {
		t.Fatalf("models=%#v, want qwen3.6-plus", payload.Models)
	}
	if payload.Status == nil {
		t.Fatal("status field missing")
	}
	if _, ok := payload.Status["qwen_status"]; !ok {
		t.Fatalf("status=%#v, want qwen_status field", payload.Status)
	}
}

func TestGetAuthFileModelsStripsBalanceLikeFieldsFromQwenStatus(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("qwen-auth", "qwen", []*registry.ModelInfo{
		{ID: "qwen3.6-plus", DisplayName: "Qwen3.6-Plus"},
	})
	defer reg.UnregisterClient("qwen-auth")

	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "qwen-auth",
		FileName: "qwen-user.json",
		Provider: "qwen",
		Metadata: map[string]any{
			"qwen_status": map[string]any{
				"available": true,
				"balance":   100,
				"quota": map[string]any{
					"remaining": 12,
				},
				"nested": map[string]any{
					"credits": 5,
					"ok":      true,
				},
			},
		},
	}); err != nil {
		t.Fatalf("failed to register auth record: %v", err)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/mgmt/auth-files/models?name=qwen-user.json", nil)

	handler.GetAuthFileModels(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Status map[string]map[string]any `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v; body=%s", err, rec.Body.String())
	}
	qwenStatus, ok := payload.Status["qwen_status"]
	if !ok {
		t.Fatalf("status=%#v, want qwen_status field", payload.Status)
	}
	if _, exists := qwenStatus["balance"]; exists {
		t.Fatalf("qwen_status=%#v, balance should be stripped", qwenStatus)
	}
	if _, exists := qwenStatus["quota"]; exists {
		t.Fatalf("qwen_status=%#v, quota should be stripped", qwenStatus)
	}
	nested, _ := qwenStatus["nested"].(map[string]any)
	if _, exists := nested["credits"]; exists {
		t.Fatalf("nested=%#v, credits should be stripped", nested)
	}
	if nested["ok"] != true {
		t.Fatalf("nested=%#v, want preserved non-balance field", nested)
	}
}
