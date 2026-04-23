package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type imageCaptureExecutor struct {
	alt          string
	sourceFormat string
	model        string
	payload      []byte
	calls        int
}

func (e *imageCaptureExecutor) Identifier() string { return "image-test-provider" }
func (e *imageCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.alt = opts.Alt
	e.sourceFormat = opts.SourceFormat.String()
	e.model = req.Model
	payload := e.payload
	if len(payload) == 0 {
		payload = []byte(`{"created":1,"data":[{"b64_json":"ok"}]}`)
	}
	return coreexecutor.Response{Payload: payload}, nil
}
func (e *imageCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}
func (e *imageCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (e *imageCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}
func (e *imageCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIImagesGenerationExecute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &imageCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "image-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "codex/gpt-image"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIImagesAPIHandler(base)
	router := gin.New()
	var capturedOverride string
	router.Use(func(c *gin.Context) {
		c.Next()
		if raw, ok := c.Get("RESPONSE_BODY_OVERRIDE"); ok {
			if body, ok := raw.([]byte); ok {
				capturedOverride = string(body)
			}
		}
	})
	router.POST("/v1/images/generations", h.ImagesGenerations)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"codex/gpt-image","prompt":"draw"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if executor.alt != "images/generations" {
		t.Fatalf("alt = %q", executor.alt)
	}
	if executor.sourceFormat != "openai" {
		t.Fatalf("source format = %q", executor.sourceFormat)
	}
	if !strings.Contains(capturedOverride, "<redacted>") || strings.Contains(capturedOverride, `"ok"`) {
		t.Fatalf("response override not sanitized: %q", capturedOverride)
	}
}

func TestOpenAIImagesGenerationRejectsUnsupportedResponseFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIImagesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	router := gin.New()
	router.POST("/v1/images/generations", h.ImagesGenerations)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"codex/gpt-image","prompt":"draw","response_format":"url"}`))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestOpenAIImagesGenerationRejectsUnsupportedN(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIImagesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	router := gin.New()
	router.POST("/v1/images/generations", h.ImagesGenerations)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"codex/gpt-image","prompt":"draw","n":2}`))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestImagesGenerations_AcceptsGPTImage2Model(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("image2-auth", "image-test-provider", []*registry.ModelInfo{{ID: "gpt-image-2"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("image2-auth") })

	executor := &imageCaptureExecutor{payload: []byte(`{"data":[{"b64_json":"ok"}]}`)}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "image2-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	h := NewOpenAIImagesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.POST("/v1/images/generations", h.ImagesGenerations)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"draw"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if executor.model != "gpt-image-2" {
		t.Fatalf("model = %q, want gpt-image-2", executor.model)
	}
	if executor.alt != "images/generations" {
		t.Fatalf("alt = %q, want images/generations", executor.alt)
	}
}

func TestOpenAIImagesGenerationRejectsTextOnlyModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registry.GetGlobalRegistry().RegisterClient("text-only-auth", "image-test-provider", []*registry.ModelInfo{{ID: "gpt-5.4"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("text-only-auth") })

	h := NewOpenAIImagesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	router := gin.New()
	router.POST("/v1/images/generations", h.ImagesGenerations)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-5.4","prompt":"draw"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "does not support image generation") {
		t.Fatalf("unexpected body=%s", resp.Body.String())
	}
}
