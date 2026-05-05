package openai

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

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

func TestCollectImagesFromResponsesStreamUsesCompletedOutputItemOnDisconnect(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"image_generation_call","result":"aGVsbG8=","revised_prompt":"A concise prompt","output_format":"png","size":"1024x1024","background":"auto","quality":"high"}}` + "\n\n")
	close(data)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")
	if errMsg != nil {
		t.Fatalf("errMsg = %v, want nil", errMsg.Error)
	}

	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != "aGVsbG8=" {
		t.Fatalf("b64_json = %q, want image result", got)
	}
	if got := gjson.GetBytes(out, "data.0.revised_prompt").String(); got != "A concise prompt" {
		t.Fatalf("revised_prompt = %q, want recovered prompt", got)
	}
	if got := gjson.GetBytes(out, "output_format").String(); got != "png" {
		t.Fatalf("output_format = %q, want png", got)
	}
	if got := gjson.GetBytes(out, "size").String(); got != "1024x1024" {
		t.Fatalf("size = %q, want 1024x1024", got)
	}
	if got := gjson.GetBytes(out, "quality").String(); got != "high" {
		t.Fatalf("quality = %q, want high", got)
	}
}

func TestCollectImagesFromResponsesWithRetryRetriesDisconnectedStream(t *testing.T) {
	attempts := 0

	out, upstreamHeaders, errMsg := collectImagesFromResponsesWithRetry(context.Background(), 2, "b64_json", func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		attempts++
		data := make(chan []byte, 1)
		errs := make(chan *interfaces.ErrorMessage)
		close(errs)
		headers := http.Header{}

		if attempts == 1 {
			headers.Set("X-Attempt", "first")
			close(data)
			return data, headers, errs
		}

		headers.Set("X-Attempt", "second")
		data <- []byte(`data: {"type":"response.completed","response":{"created_at":123,"output":[{"type":"image_generation_call","result":"Zm9v","output_format":"png"}]}}` + "\n\n")
		close(data)
		return data, headers, errs
	})

	if errMsg != nil {
		t.Fatalf("errMsg = %v, want nil", errMsg.Error)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if got := upstreamHeaders.Get("X-Attempt"); got != "second" {
		t.Fatalf("upstream header = %q, want second attempt header", got)
	}
	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != "Zm9v" {
		t.Fatalf("b64_json = %q, want retry result", got)
	}
}

func TestImagesGenerationsRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"gpt-5.4-mini","prompt":"draw a square"}`)

	resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)

	assertUnsupportedImagesModelResponse(t, resp, "gpt-5.4-mini")
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
