package handlers

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
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

func TestGetRequestDetails_ImageModelReturns503(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	imageOnlyModels := []string{
		"gpt-image-1.5",
		"gpt-image-2",
		"codex/gpt-image-2",
		"grok-imagine-image",
		"xai/grok-imagine-image",
		"grok-imagine-image-quality",
		"xai/grok-imagine-image-quality",
	}
	for _, model := range imageOnlyModels {
		t.Run(model, func(t *testing.T) {
			_, _, errMsg := handler.getRequestDetails(model)
			if errMsg == nil {
				t.Fatalf("expected error for %s, got nil", model)
			}
			if errMsg.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("unexpected status code: got %d want %d", errMsg.StatusCode, http.StatusServiceUnavailable)
			}
			if errMsg.Error == nil {
				t.Fatalf("expected error message, got nil")
			}
			msg := errMsg.Error.Error()
			if !strings.Contains(msg, "/v1/images/generations") || !strings.Contains(msg, "/v1/images/edits") {
				t.Fatalf("unexpected error message: %q", msg)
			}
		})
	}
}

func TestValidateImageOnlyModel_AllowsImageEndpoints(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	imageOnlyModels := []string{
		"gpt-image-1.5",
		"gpt-image-2",
		"codex/gpt-image-2",
		"grok-imagine-image",
		"xai/grok-imagine-image",
		"grok-imagine-image-quality",
		"xai/grok-imagine-image-quality",
	}
	for _, model := range imageOnlyModels {
		t.Run(model, func(t *testing.T) {
			if errMsg := handler.validateImageOnlyModel(model, true); errMsg != nil {
				t.Fatalf("validateImageOnlyModel(%q, true) = %+v, want nil", model, errMsg)
			}
			if errMsg := handler.validateImageOnlyModel(model, false); errMsg == nil {
				t.Fatalf("validateImageOnlyModel(%q, false) = nil, want image-only error", model)
			} else if errMsg.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("unexpected status code: got %d want %d", errMsg.StatusCode, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestIsOpenAIImageOnlyModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "gpt-image-1.5", want: true},
		{model: "gpt-image-2", want: true},
		{model: "codex/gpt-image-1.5", want: true},
		{model: "grok-imagine-image", want: true},
		{model: "xai/grok-imagine-image", want: true},
		{model: "XAI/Grok-Imagine-Image-Quality", want: true},
		{model: "grok-imagine-image-quality", want: true},
		{model: "grok-3", want: false},
		{model: "gpt-5.2", want: false},
		{model: "grok-imagine-video", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isOpenAIImageOnlyModel(tt.model); got != tt.want {
				t.Fatalf("isOpenAIImageOnlyModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestExecuteImageWithAuthManager_AllowsImageOnlyModels(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	imageOnlyModels := []string{
		"gpt-image-1.5",
		"gpt-image-2",
		"grok-imagine-image",
		"grok-imagine-image-quality",
		"xai/grok-imagine-image-quality",
	}
	for _, model := range imageOnlyModels {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","prompt":"draw"}`)
			_, _, errMsg := handler.ExecuteImageWithAuthManager(context.Background(), "openai-image", model, body, "")
			if errMsg == nil {
				t.Fatal("expected auth selection error, got nil")
			}
			if errMsg.Error == nil {
				t.Fatal("expected error message, got nil")
			}
			msg := errMsg.Error.Error()
			if strings.Contains(msg, "only supported on /v1/images/generations") {
				t.Fatalf("ExecuteImageWithAuthManager rejected image-only model: %q", msg)
			}

			_, _, errMsg = handler.ExecuteWithAuthManager(context.Background(), "openai-image", model, body, "")
			if errMsg == nil {
				t.Fatal("expected image-only rejection for non-image execution path, got nil")
			}
			if errMsg.Error == nil || !strings.Contains(errMsg.Error.Error(), "only supported on /v1/images/generations") {
				t.Fatalf("unexpected non-image execution error: %+v", errMsg)
			}
		})
	}
}
