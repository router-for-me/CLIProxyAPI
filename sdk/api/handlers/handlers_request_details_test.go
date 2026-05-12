package handlers

import (
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestGetRequestDetails_PreservesSuffix(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	modelRegistry.RegisterClient("test-request-details-gemini", "gemini", []*registry.ModelInfo{
		{ID: "gemini-2.5-pro", Created: now + 30},
		{ID: "gemini-2.5-flash", Created: now + 25},
	})
	modelRegistry.RegisterClient("test-request-details-openai", "openai", []*registry.ModelInfo{
		{ID: "gpt-5.2", Created: now + 20},
	})
	modelRegistry.RegisterClient("test-request-details-claude", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5", Created: now + 5},
	})

	// Ensure cleanup of all test registrations.
	clientIDs := []string{
		"test-request-details-gemini",
		"test-request-details-openai",
		"test-request-details-claude",
	}
	for _, clientID := range clientIDs {
		id := clientID
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(id)
		})
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
		wantErr       bool
	}{
		{
			name:          "numeric suffix preserved",
			inputModel:    "gemini-2.5-pro(8192)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(8192)",
			wantErr:       false,
		},
		{
			name:          "level suffix preserved",
			inputModel:    "gpt-5.2(high)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5.2(high)",
			wantErr:       false,
		},
		{
			name:          "no suffix unchanged",
			inputModel:    "claude-sonnet-4-5",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5",
			wantErr:       false,
		},
		{
			name:          "unknown model with suffix",
			inputModel:    "unknown-model(8192)",
			wantProviders: nil,
			wantModel:     "",
			wantErr:       true,
		},
		{
			name:          "auto suffix resolved",
			inputModel:    "auto(high)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(high)",
			wantErr:       false,
		},
		{
			name:          "special suffix none preserved",
			inputModel:    "gemini-2.5-flash(none)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-flash(none)",
			wantErr:       false,
		},
		{
			name:          "special suffix auto preserved",
			inputModel:    "claude-sonnet-4-5(auto)",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5(auto)",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, model, errMsg := handler.getRequestDetails(tt.inputModel)
			if (errMsg != nil) != tt.wantErr {
				t.Fatalf("getRequestDetails() error = %v, wantErr %v", errMsg, tt.wantErr)
			}
			if errMsg != nil {
				return
			}
			if !reflect.DeepEqual(providers, tt.wantProviders) {
				t.Fatalf("getRequestDetails() providers = %v, want %v", providers, tt.wantProviders)
			}
			if model != tt.wantModel {
				t.Fatalf("getRequestDetails() model = %v, want %v", model, tt.wantModel)
			}
		})
	}
}

func TestGetRequestDetails_VirtualModels(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	// Register upstream models
	modelRegistry.RegisterClient("test-virtual-gemini", "gemini", []*registry.ModelInfo{
		{ID: "gemini-2.5-pro", Created: now + 30},
	})
	modelRegistry.RegisterClient("test-virtual-openai", "openai", []*registry.ModelInfo{
		{ID: "gpt-5-codex-mini", Created: now + 20},
	})

	clientIDs := []string{"test-virtual-gemini", "test-virtual-openai"}
	for _, clientID := range clientIDs {
		id := clientID
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(id)
		})
	}

	// Configure virtual models
	virtualModels := []sdkconfig.VirtualModel{
		{Name: "fast", Model: "gpt-5-codex-mini"},
		{Name: "smart", Model: "gemini-2.5-pro"},
	}

	authManager := coreauth.NewManager(nil, nil, nil)
	authManager.SetVirtualModels(virtualModels)
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{VirtualModels: virtualModels}, authManager)

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
		wantErr       bool
	}{
		{
			name:          "virtual model without suffix",
			inputModel:    "fast",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5-codex-mini",
			wantErr:       false,
		},
		{
			name:          "virtual model with numeric suffix",
			inputModel:    "fast(8192)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5-codex-mini(8192)",
			wantErr:       false,
		},
		{
			name:          "virtual model with level suffix",
			inputModel:    "fast(high)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5-codex-mini(high)",
			wantErr:       false,
		},
		{
			name:          "virtual model pointing to gemini",
			inputModel:    "smart",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro",
			wantErr:       false,
		},
		{
			name:          "virtual model with auto suffix",
			inputModel:    "smart(auto)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(auto)",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, model, errMsg := handler.getRequestDetails(tt.inputModel)
			if (errMsg != nil) != tt.wantErr {
				t.Fatalf("getRequestDetails() error = %v, wantErr %v", errMsg, tt.wantErr)
			}
			if errMsg != nil {
				return
			}
			if !reflect.DeepEqual(providers, tt.wantProviders) {
				t.Fatalf("getRequestDetails() providers = %v, want %v", providers, tt.wantProviders)
			}
			if model != tt.wantModel {
				t.Fatalf("getRequestDetails() model = %v, want %v", model, tt.wantModel)
			}
		})
	}
}

func TestAppendVirtualModels_ClaudeFormat(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		VirtualModels: []sdkconfig.VirtualModel{
			{Name: "fast", Model: "gpt-5-codex-mini"},
			{Name: "smart", Model: "gemini-2.5-pro"},
		},
	}, nil)

	models := []map[string]any{
		{"id": "claude-sonnet-4-5", "object": "model", "created_at": int64(1234567890), "type": "model", "display_name": "Claude Sonnet 4.5", "owned_by": "anthropic"},
	}

	result := handler.AppendVirtualModels(models, "claude")

	if len(result) != 3 {
		t.Fatalf("expected 3 models, got %d", len(result))
	}

	// Check first original model is unchanged
	if result[0]["id"] != "claude-sonnet-4-5" {
		t.Errorf("first model id = %v, want claude-sonnet-4-5", result[0]["id"])
	}

	// Check virtual model format for Claude
	virtualModel1 := result[1]
	if virtualModel1["id"] != "fast" {
		t.Errorf("virtual model id = %v, want fast", virtualModel1["id"])
	}
	if virtualModel1["object"] != "model" {
		t.Errorf("virtual model object = %v, want model", virtualModel1["object"])
	}
	if virtualModel1["created_at"] != int64(0) {
		t.Errorf("virtual model created_at = %v, want 0", virtualModel1["created_at"])
	}
	if virtualModel1["type"] != "model" {
		t.Errorf("virtual model type = %v, want model", virtualModel1["type"])
	}
	if virtualModel1["display_name"] != "fast" {
		t.Errorf("virtual model display_name = %v, want fast", virtualModel1["display_name"])
	}
	if virtualModel1["owned_by"] != "virtual" {
		t.Errorf("virtual model owned_by = %v, want virtual", virtualModel1["owned_by"])
	}

	// Check second virtual model
	virtualModel2 := result[2]
	if virtualModel2["id"] != "smart" {
		t.Errorf("second virtual model id = %v, want smart", virtualModel2["id"])
	}
}

func TestAppendVirtualModels_OpenAIFormat(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		VirtualModels: []sdkconfig.VirtualModel{
			{Name: "fast", Model: "gpt-5-codex-mini"},
			{Name: "smart", Model: "gemini-2.5-pro"},
		},
	}, nil)

	models := []map[string]any{
		{"id": "gpt-4", "object": "model", "created": int64(1234567890), "owned_by": "openai"},
	}

	result := handler.AppendVirtualModels(models, "openai")

	if len(result) != 3 {
		t.Fatalf("expected 3 models, got %d", len(result))
	}

	// Check first original model is unchanged
	if result[0]["id"] != "gpt-4" {
		t.Errorf("first model id = %v, want gpt-4", result[0]["id"])
	}

	// Check virtual model format for OpenAI
	virtualModel1 := result[1]
	if virtualModel1["id"] != "fast" {
		t.Errorf("virtual model id = %v, want fast", virtualModel1["id"])
	}
	if virtualModel1["object"] != "model" {
		t.Errorf("virtual model object = %v, want model", virtualModel1["object"])
	}
	if virtualModel1["created"] != int64(0) {
		t.Errorf("virtual model created = %v, want 0", virtualModel1["created"])
	}
	if virtualModel1["owned_by"] != "virtual" {
		t.Errorf("virtual model owned_by = %v, want virtual", virtualModel1["owned_by"])
	}
	// OpenAI format should NOT have display_name for virtual models
	if _, exists := virtualModel1["display_name"]; exists {
		t.Errorf("virtual model should not have display_name in OpenAI format")
	}
}

func TestAppendVirtualModels_EmptyConfig(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)

	models := []map[string]any{
		{"id": "gpt-4", "object": "model"},
	}

	result := handler.AppendVirtualModels(models, "openai")

	if len(result) != 1 {
		t.Fatalf("expected 1 model, got %d", len(result))
	}
}

func TestAppendVirtualModels_NilConfig(t *testing.T) {
	handler := NewBaseAPIHandlers(nil, nil)

	models := []map[string]any{
		{"id": "gpt-4", "object": "model"},
	}

	result := handler.AppendVirtualModels(models, "openai")

	if len(result) != 1 {
		t.Fatalf("expected 1 model, got %d", len(result))
	}
}

func TestAppendVirtualModels_NoVirtualModels(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		VirtualModels: []sdkconfig.VirtualModel{},
	}, nil)

	models := []map[string]any{
		{"id": "gpt-4", "object": "model"},
	}

	result := handler.AppendVirtualModels(models, "openai")

	if len(result) != 1 {
		t.Fatalf("expected 1 model, got %d", len(result))
	}
}
