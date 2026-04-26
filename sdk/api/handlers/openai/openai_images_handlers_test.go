package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type imagesCaptureExecutor struct {
	provider     string
	nativeImages bool
	calls        int
	model        string
	alt          string
	payload      string
}

func (e *imagesCaptureExecutor) Identifier() string { return e.provider }

func (e *imagesCaptureExecutor) SupportsNativeImagesEndpoint(endpoint string) bool {
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	return e.nativeImages && endpoint == "images/generations"
}

func (e *imagesCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.model = req.Model
	e.alt = opts.Alt
	e.payload = string(req.Payload)
	return coreexecutor.Response{Payload: []byte(`{"created":1,"data":[{"b64_json":"native"}]}`)}, nil
}

func (e *imagesCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *imagesCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *imagesCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *imagesCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestImagesGenerationsRoutesNativeProviderByRequestedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &imagesCaptureExecutor{provider: "openai-compatibility", nativeImages: true}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "images-native-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "qwen-image-test"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	router := imagesTestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"qwen-image-test","prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.model != "qwen-image-test" {
		t.Fatalf("model = %q, want qwen-image-test", executor.model)
	}
	if executor.alt != "images/generations" {
		t.Fatalf("alt = %q, want images/generations", executor.alt)
	}
	if !strings.Contains(executor.payload, `"model":"qwen-image-test"`) {
		t.Fatalf("payload = %s, want requested image model", executor.payload)
	}
}

func TestImagesGenerationsPrefersNativeProviderForGPTImage2(t *testing.T) {
	gin.SetMode(gin.TestMode)
	nativeExecutor := &imagesCaptureExecutor{provider: "openai-compatibility", nativeImages: true}
	codexExecutor := &imagesCaptureExecutor{provider: "codex"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(nativeExecutor)
	manager.RegisterExecutor(codexExecutor)

	nativeAuth := &coreauth.Auth{ID: "images-gpt-image-native", Provider: nativeExecutor.Identifier(), Status: coreauth.StatusActive}
	codexAuth := &coreauth.Auth{ID: "images-gpt-image-codex", Provider: codexExecutor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), nativeAuth); err != nil {
		t.Fatalf("Register native auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), codexAuth); err != nil {
		t.Fatalf("Register codex auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(nativeAuth.ID, nativeAuth.Provider, []*registry.ModelInfo{{ID: defaultImagesToolModel}})
	registry.GetGlobalRegistry().RegisterClient(codexAuth.ID, codexAuth.Provider, []*registry.ModelInfo{{ID: defaultImagesToolModel}, {ID: defaultImagesMainModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(nativeAuth.ID)
		registry.GetGlobalRegistry().UnregisterClient(codexAuth.ID)
	})

	router := imagesTestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if nativeExecutor.calls != 1 {
		t.Fatalf("native executor calls = %d, want 1", nativeExecutor.calls)
	}
	if codexExecutor.calls != 0 {
		t.Fatalf("codex executor calls = %d, want 0", codexExecutor.calls)
	}
}

func TestImagesGenerationsUsesOpenAICompatAliasUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &imagesCaptureExecutor{provider: "native-images", nativeImages: true}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "native-images",
			Models: []internalconfig.OpenAICompatibilityModel{{
				Name:  "upstream-image-model",
				Alias: "alias-image-model",
			}},
		}},
	})
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "images-native-alias-auth",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"compat_name":  "native-images",
			"provider_key": "native-images",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "alias-image-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	router := imagesTestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"alias-image-model","prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.model != "upstream-image-model" {
		t.Fatalf("model = %q, want upstream-image-model", executor.model)
	}
	if !strings.Contains(executor.payload, `"model":"alias-image-model"`) {
		t.Fatalf("payload = %s, want original requested image model", executor.payload)
	}
}

func TestImagesGenerationsRejectsRegisteredNonNativeProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &imagesCaptureExecutor{provider: "gemini"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "images-non-native-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "imagen-test"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	router := imagesTestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"imagen-test","prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusBadGateway, resp.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}

func TestImagesGenerationsDoesNotFallbackToCodexForCustomImageModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	imageExecutor := &imagesCaptureExecutor{provider: "gemini"}
	codexExecutor := &imagesCaptureExecutor{provider: "codex"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(imageExecutor)
	manager.RegisterExecutor(codexExecutor)

	imageAuth := &coreauth.Auth{ID: "images-custom-non-native-auth", Provider: imageExecutor.Identifier(), Status: coreauth.StatusActive}
	codexAuth := &coreauth.Auth{ID: "images-custom-codex-auth", Provider: codexExecutor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), imageAuth); err != nil {
		t.Fatalf("Register image auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), codexAuth); err != nil {
		t.Fatalf("Register codex auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(imageAuth.ID, imageAuth.Provider, []*registry.ModelInfo{{ID: "imagen-test"}})
	registry.GetGlobalRegistry().RegisterClient(codexAuth.ID, codexAuth.Provider, []*registry.ModelInfo{{ID: defaultImagesMainModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(imageAuth.ID)
		registry.GetGlobalRegistry().UnregisterClient(codexAuth.ID)
	})

	router := imagesTestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"imagen-test","prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusBadGateway, resp.Body.String())
	}
	if imageExecutor.calls != 0 {
		t.Fatalf("image executor calls = %d, want 0", imageExecutor.calls)
	}
	if codexExecutor.calls != 0 {
		t.Fatalf("codex executor calls = %d, want 0", codexExecutor.calls)
	}
}

func TestImagesGenerationsRejectsUnknownNonBuiltinModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)

	router := imagesTestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"unknown-image-model","prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusBadGateway, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "no native image provider") {
		t.Fatalf("body = %s, want no native image provider error", resp.Body.String())
	}
}

func TestImagesEditsRejectsCustomImageModelWithoutNativeEditSupport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	codexExecutor := &imagesCaptureExecutor{provider: "codex"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(codexExecutor)

	codexAuth := &coreauth.Auth{ID: "images-edit-codex-auth", Provider: codexExecutor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), codexAuth); err != nil {
		t.Fatalf("Register codex auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(codexAuth.ID, codexAuth.Provider, []*registry.ModelInfo{{ID: defaultImagesMainModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(codexAuth.ID)
	})

	router := imagesTestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{"model":"imagen-test","prompt":"edit a cat","images":[{"image_url":"data:image/png;base64,aW1hZ2U="}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusBadGateway, resp.Body.String())
	}
	if codexExecutor.calls != 0 {
		t.Fatalf("codex executor calls = %d, want 0", codexExecutor.calls)
	}
}

func imagesTestRouter(manager *coreauth.Manager) *gin.Engine {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/images/generations", h.ImagesGenerations)
	router.POST("/v1/images/edits", h.ImagesEdits)
	return router
}
