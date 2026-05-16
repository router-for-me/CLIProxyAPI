package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

type imagesCaptureExecutor struct {
	id           string
	alt          string
	sourceFormat string
	calls        int
	payload      []byte
}

func (e *imagesCaptureExecutor) Identifier() string { return e.id }

func (e *imagesCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.alt = opts.Alt
	e.sourceFormat = opts.SourceFormat.String()
	e.payload = append([]byte(nil), req.Payload...)
	return coreexecutor.Response{Payload: []byte(`{"data":[{"url":"https://example.test/image.png"}]}`)}, nil
}

func (e *imagesCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *imagesCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *imagesCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *imagesCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func performImagesEndpointRequest(t *testing.T, endpointPath string, contentType string, body io.Reader, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST(endpointPath, handler)

	req := httptest.NewRequest(http.MethodPost, endpointPath, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func assertUnsupportedImagesModelResponse(t *testing.T, resp *httptest.ResponseRecorder, model string) {
	t.Helper()

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}

	message := gjson.GetBytes(resp.Body.Bytes(), "error.message").String()
	expectedMessage := "Model " + model + " is not supported on " + imagesGenerationsPath + " or " + imagesEditsPath + ". Use " + defaultImagesToolModel + "."
	if message != expectedMessage {
		t.Fatalf("error message = %q, want %q", message, expectedMessage)
	}
	if errorType := gjson.GetBytes(resp.Body.Bytes(), "error.type").String(); errorType != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", errorType)
	}
}

func TestImagesModelValidationAllowsGPTImage2WithOptionalPrefix(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "codex/gpt-image-2"} {
		if !isSupportedImagesModel(model) {
			t.Fatalf("expected %s to be supported", model)
		}
	}
	if isSupportedImagesModel("gpt-5.4-mini") {
		t.Fatal("expected gpt-5.4-mini to be rejected")
	}
}

func TestImagesGenerationsRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesGenerationsRoutesOpenAICompatModelThroughAuthManager(t *testing.T) {
	executor := &imagesCaptureExecutor{id: "xai"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "xai-images-auth", Provider: "xai", Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "grok-imagine-image-quality", Type: "openai-compatibility"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"grok-imagine-image-quality","prompt":"draw a square","size":"1024x1024"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.alt != "images/generations" {
		t.Fatalf("alt = %q, want images/generations", executor.alt)
	}
	if executor.sourceFormat != "openai" {
		t.Fatalf("source format = %q, want openai", executor.sourceFormat)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "https://example.test/image.png" {
		t.Fatalf("response url = %q", got)
	}
}

func TestImagesEditsJSONRoutesOpenAICompatModelThroughAuthManager(t *testing.T) {
	executor := &imagesCaptureExecutor{id: "xai"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "xai-images-edit-auth", Provider: "xai", Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "grok-imagine-image-quality", Type: "openai-compatibility"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"grok-imagine-image-quality","prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}],"size":"1024x1024"}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.alt != "images/edits" {
		t.Fatalf("alt = %q, want images/edits", executor.alt)
	}
	if executor.sourceFormat != "openai" {
		t.Fatalf("source format = %q, want openai", executor.sourceFormat)
	}
	if got := gjson.GetBytes(executor.payload, "image.type").String(); got != "image_url" {
		t.Fatalf("payload image type = %q, want image_url; body=%s", got, string(executor.payload))
	}
	if got := gjson.GetBytes(executor.payload, "image.url").String(); got != "data:image/png;base64,AA==" {
		t.Fatalf("payload image url = %q; body=%s", got, string(executor.payload))
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "https://example.test/image.png" {
		t.Fatalf("response url = %q", got)
	}
}

func TestImagesEditsMultipartRoutesOpenAICompatModelThroughAuthManager(t *testing.T) {
	executor := &imagesCaptureExecutor{id: "xai"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "xai-images-edit-multipart-auth", Provider: "xai", Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "grok-imagine-image-quality", Type: "openai-compatibility"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "grok-imagine-image-quality"); err != nil {
		t.Fatalf("write model field: %v", err)
	}
	if err := writer.WriteField("prompt", "edit this"); err != nil {
		t.Fatalf("write prompt field: %v", err)
	}
	if err := writer.WriteField("size", "1024x1024"); err != nil {
		t.Fatalf("write size field: %v", err)
	}
	imageWriter, err := writer.CreateFormFile("image", "reference.png")
	if err != nil {
		t.Fatalf("create image field: %v", err)
	}
	if _, err := imageWriter.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}); err != nil {
		t.Fatalf("write image bytes: %v", err)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)
	resp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &body, handler.ImagesEdits)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.alt != "images/edits" {
		t.Fatalf("alt = %q, want images/edits", executor.alt)
	}
	if got := gjson.GetBytes(executor.payload, "image.type").String(); got != "image_url" {
		t.Fatalf("payload image type = %q, want image_url; body=%s", got, string(executor.payload))
	}
	if got := gjson.GetBytes(executor.payload, "image.url").String(); !strings.HasPrefix(got, "data:application/octet-stream;base64,") {
		t.Fatalf("payload image url = %q; body=%s", got, string(executor.payload))
	}
}

func TestImagesEditsJSONRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesEditsMultipartRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-5.4-mini"); err != nil {
		t.Fatalf("write model field: %v", err)
	}
	if err := writer.WriteField("prompt", "edit this"); err != nil {
		t.Fatalf("write prompt field: %v", err)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	resp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &body, handler.ImagesEdits)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
}

func TestImagesGenerations_DisableImageGeneration_Returns404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationAll}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
}

func TestImagesEdits_DisableImageGeneration_Returns404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationAll}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
}

func TestImagesGenerations_DisableImageGenerationChat_DoesNotReturn404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationChat}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}

func TestImagesEdits_DisableImageGenerationChat_DoesNotReturn404(t *testing.T) {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{DisableImageGeneration: internalconfig.DisableImageGenerationChat}, nil)
	handler := NewOpenAIAPIHandler(base)
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)

	resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
}
