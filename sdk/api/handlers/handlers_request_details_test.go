package handlers

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type requestDetailsProviderExecutor struct {
	id string
}

func (e *requestDetailsProviderExecutor) Identifier() string { return e.id }

func (e *requestDetailsProviderExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *requestDetailsProviderExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *requestDetailsProviderExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, nil
}

func (e *requestDetailsProviderExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *requestDetailsProviderExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
}

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

func TestFilterProvidersByToolCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		providers []string
		payload   []byte
		want      []string
	}{
		{
			name:      "mixed tools exclude antigravity",
			providers: []string{"antigravity", "gemini"},
			payload:   []byte(`{"tools":[{"type":"function","function":{"name":"f"}},{"google_search":{}}]}`),
			want:      []string{"gemini"},
		},
		{
			name:      "search only keeps antigravity",
			providers: []string{"antigravity", "gemini"},
			payload:   []byte(`{"tools":[{"google_search":{}}]}`),
			want:      []string{"antigravity", "gemini"},
		},
		{
			name:      "function only keeps antigravity",
			providers: []string{"antigravity", "gemini"},
			payload:   []byte(`{"tools":[{"type":"function","function":{"name":"f"}}]}`),
			want:      []string{"antigravity", "gemini"},
		},
		{
			name:      "mixed tools only antigravity becomes empty",
			providers: []string{"antigravity"},
			payload:   []byte(`{"tools":[{"type":"function","function":{"name":"f"}},{"google_search":{}}]}`),
			want:      []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterProvidersByToolCompatibility(tt.providers, tt.payload)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("filterProvidersByToolCompatibility() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetRequestDetails_ImageModelReturns400(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	_, _, errMsg := handler.getRequestDetails("gpt-image-2")
	if errMsg == nil {
		t.Fatalf("expected error for gpt-image-2, got nil")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected status code: got %d want %d", errMsg.StatusCode, http.StatusBadRequest)
	}
	if errMsg.Error == nil {
		t.Fatalf("expected error message, got nil")
	}
	msg := errMsg.Error.Error()
	if !strings.Contains(msg, "/v1/images/generations") || !strings.Contains(msg, "/v1/images/edits") {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestGetRequestDetails_FallsBackMiniMaxToClaudeProvider(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	providers, model, errMsg := handler.getRequestDetails("MiniMax-M2.7")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() unexpected error: %v", errMsg.Error)
	}
	if want := []string{"claude"}; !reflect.DeepEqual(providers, want) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, want)
	}
	if model != "MiniMax-M2.7" {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, "MiniMax-M2.7")
	}
}

func TestGetRequestDetails_FallsBackToConfiguredProviderWhenRegistryMissesGLM47(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeKey: []internalconfig.ClaudeKey{
			{
				APIKey:  "glm47-key",
				BaseURL: "https://open.bigmodel.cn/api/anthropic",
				Models: []internalconfig.ClaudeModel{
					{Name: "glm-4.7", Alias: ""},
				},
			},
		},
	})
	manager.RegisterExecutor(&requestDetailsProviderExecutor{id: "claude"})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "glm47-auth",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "glm47-key",
			"base_url": "https://open.bigmodel.cn/api/anthropic",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails("glm-4.7")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() unexpected error: %v", errMsg.Error)
	}
	if want := []string{"claude"}; !reflect.DeepEqual(providers, want) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, want)
	}
	if model != "glm-4.7" {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, "glm-4.7")
	}
}

func TestGetRequestDetails_FallsBackToClaudeProviderForDirectOAuthModel(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&requestDetailsProviderExecutor{id: "claude"})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-oauth-auth",
		Provider: "claude",
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	providers, model, errMsg := handler.getRequestDetails("claude-sonnet-4-6")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() unexpected error: %v", errMsg.Error)
	}
	if want := []string{"claude"}; !reflect.DeepEqual(providers, want) {
		t.Fatalf("getRequestDetails() providers = %v, want %v", providers, want)
	}
	if model != "claude-sonnet-4-6" {
		t.Fatalf("getRequestDetails() model = %q, want %q", model, "claude-sonnet-4-6")
	}
}
