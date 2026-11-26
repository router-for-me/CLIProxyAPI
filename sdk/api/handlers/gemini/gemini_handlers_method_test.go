package gemini

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestGeminiHandlerUnsupportedMethodReturnsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	tests := []struct {
		name        string
		path        string
		wantMessage string
	}{
		{
			name:        "unsupported embedContent method",
			path:        "/v1beta/models/gemini-2.5-pro:embedContent",
			wantMessage: "Unsupported method: embedContent",
		},
		{
			name:        "misspelled method name",
			path:        "/v1beta/models/gemini-2.5-pro:generateContnt",
			wantMessage: "Unsupported method: generateContnt",
		},
		{
			name:        "empty method after colon",
			path:        "/v1beta/models/gemini-2.5-pro:",
			wantMessage: "/v1beta/models/gemini-2.5-pro: not found.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected status %d, got %d (body=%q)", http.StatusNotFound, rec.Code, rec.Body.String())
			}

			var errResp handlers.ErrorResponse
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &errResp); errUnmarshal != nil {
				t.Fatalf("expected valid JSON error response, unmarshal error: %v (body=%q)", errUnmarshal, rec.Body.String())
			}

			if errResp.Error.Type != "invalid_request_error" {
				t.Errorf("expected error.type %q, got %q", "invalid_request_error", errResp.Error.Type)
			}
			if errResp.Error.Message != tc.wantMessage {
				t.Errorf("expected error.message %q, got %q", tc.wantMessage, errResp.Error.Message)
			}
		})
	}
}

type recordingGeminiExecutor struct {
	id         string
	lastMethod string
	lastModel  string
}

func (e *recordingGeminiExecutor) Identifier() string {
	return e.id
}

func (e *recordingGeminiExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.lastMethod = "generateContent"
	e.lastModel = req.Model
	return coreexecutor.Response{Payload: []byte(`{"candidates":[]}`)}, nil
}

func (e *recordingGeminiExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.lastMethod = "streamGenerateContent"
	e.lastModel = req.Model
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"candidates":[]}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *recordingGeminiExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *recordingGeminiExecutor) CountTokens(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.lastMethod = "countTokens"
	e.lastModel = req.Model
	return coreexecutor.Response{Payload: []byte(`{"totalTokens":1}`)}, nil
}

func (e *recordingGeminiExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestGeminiHandlerSupportsModelIDsContainingColons(t *testing.T) {
	gin.SetMode(gin.TestMode)

	models := []string{
		"qwen3:32b",
		"gemini-2.5-pro:free",
		"provider:org/model:latest",
	}

	executor := &recordingGeminiExecutor{id: "colon-model-gemini-executor"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "colon-model-gemini-auth",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	modelInfos := make([]*registry.ModelInfo, 0, len(models))
	for _, m := range models {
		modelInfos = append(modelInfos, &registry.ModelInfo{ID: m})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, modelInfos)
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	tests := []struct {
		name       string
		path       string
		wantModel  string
		wantMethod string
	}{
		{
			name:       "single colon model generateContent",
			path:       "/v1beta/models/qwen3:32b:generateContent",
			wantModel:  "qwen3:32b",
			wantMethod: "generateContent",
		},
		{
			name:       "single colon model streamGenerateContent",
			path:       "/v1beta/models/gemini-2.5-pro:free:streamGenerateContent",
			wantModel:  "gemini-2.5-pro:free",
			wantMethod: "streamGenerateContent",
		},
		{
			name:       "multiple colons and slash model countTokens",
			path:       "/v1beta/models/provider:org/model:latest:countTokens",
			wantModel:  "provider:org/model:latest",
			wantMethod: "countTokens",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor.lastMethod = ""
			executor.lastModel = ""

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d (body=%q)", http.StatusOK, rec.Code, rec.Body.String())
			}
			if executor.lastModel != tc.wantModel {
				t.Errorf("expected routed model %q, got %q", tc.wantModel, executor.lastModel)
			}
			if executor.lastMethod != tc.wantMethod {
				t.Errorf("expected routed method %q, got %q", tc.wantMethod, executor.lastMethod)
			}
		})
	}
}

func TestGeminiHandlerActionParsingEdgeCases(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewGeminiAPIHandler(base)
	router := gin.New()
	router.POST("/v1beta/models/*action", h.GeminiHandler)

	tests := []struct {
		name        string
		path        string
		wantMessage string
	}{
		{
			name:        "colon model with unsupported method",
			path:        "/v1beta/models/qwen3:32b:embedContent",
			wantMessage: "Unsupported method: embedContent",
		},
		{
			name:        "colon model with trailing empty method",
			path:        "/v1beta/models/qwen3:32b:",
			wantMessage: "/v1beta/models/qwen3:32b: not found.",
		},
		{
			name:        "missing colon separator",
			path:        "/v1beta/models/qwen3",
			wantMessage: "/v1beta/models/qwen3 not found.",
		},
		{
			name:        "empty model name before colon",
			path:        "/v1beta/models/:generateContent",
			wantMessage: "/v1beta/models/:generateContent not found.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected status %d, got %d (body=%q)", http.StatusNotFound, rec.Code, rec.Body.String())
			}

			var errResp handlers.ErrorResponse
			if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &errResp); errUnmarshal != nil {
				t.Fatalf("expected valid JSON error response, unmarshal error: %v (body=%q)", errUnmarshal, rec.Body.String())
			}

			if errResp.Error.Type != "invalid_request_error" {
				t.Errorf("expected error.type %q, got %q", "invalid_request_error", errResp.Error.Type)
			}
			if errResp.Error.Message != tc.wantMessage {
				t.Errorf("expected error.message %q, got %q", tc.wantMessage, errResp.Error.Message)
			}
		})
	}
}
