package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
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
	expectedMessage := "Model " + model + " is not supported on " + imagesGenerationsPath + " or " + imagesEditsPath + ". Use " + defaultImagesToolModel + ", " + defaultXAIImagesModel + ", " + xaiImagesQualityModel + ", or a configured openai-compatibility image model."
	if message != expectedMessage {
		t.Fatalf("error message = %q, want %q", message, expectedMessage)
	}
	if errorType := gjson.GetBytes(resp.Body.Bytes(), "error.type").String(); errorType != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", errorType)
	}
}

func TestImagesModelValidationAllowsGPTImage2AndXAIModels(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "codex/gpt-image-2", "grok-imagine-image", "xai/grok-imagine-image", "grok-imagine-image-quality", "xai/grok-imagine-image-quality"} {
		if !isSupportedImagesModel(model) {
			t.Fatalf("expected %s to be supported", model)
		}
	}
	if isSupportedImagesModel("gpt-5.4-mini") {
		t.Fatal("expected gpt-5.4-mini to be rejected")
	}
	if isSupportedImagesModel("codex/grok-imagine-image") {
		t.Fatal("expected codex/grok-imagine-image to be rejected")
	}
}

func TestImagesModelValidationAllowsOpenAICompatImageModels(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-openai-compat-image-model-validation"
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{
		{ID: "compat-image-model", Object: "model", OwnedBy: "compat", Type: registry.OpenAIImageModelType},
		{ID: "compat-chat-model", Object: "model", OwnedBy: "compat", Type: "openai-compatibility"},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	if !isSupportedImagesModel("compat-image-model") {
		t.Fatal("expected configured openai-compatibility image model to be supported")
	}
	if isSupportedImagesModel("compat-chat-model") {
		t.Fatal("expected non-image openai-compatibility model to be rejected")
	}
}

func TestBuildXAIImagesGenerationsRequest(t *testing.T) {
	rawJSON := []byte(`{"model":"xai/grok-imagine-image-quality","prompt":"abstract art","aspect_ratio":"landscape","resolution":"2k","n":2,"response_format":"url"}`)

	req := buildXAIImagesGenerationsRequest(rawJSON, "xai/grok-imagine-image-quality", "url")

	if got := gjson.GetBytes(req, "model").String(); got != "grok-imagine-image-quality" {
		t.Fatalf("model = %q, want grok-imagine-image-quality", got)
	}
	if got := gjson.GetBytes(req, "prompt").String(); got != "abstract art" {
		t.Fatalf("prompt = %q, want abstract art", got)
	}
	if got := gjson.GetBytes(req, "aspect_ratio").String(); got != "16:9" {
		t.Fatalf("aspect_ratio = %q, want 16:9", got)
	}
	if got := gjson.GetBytes(req, "resolution").String(); got != "2k" {
		t.Fatalf("resolution = %q, want 2k", got)
	}
	if got := gjson.GetBytes(req, "response_format").String(); got != "url" {
		t.Fatalf("response_format = %q, want url", got)
	}
	if got := gjson.GetBytes(req, "n").Int(); got != 2 {
		t.Fatalf("n = %d, want 2", got)
	}
}

func TestBuildXAIImagesEditRequest(t *testing.T) {
	req := buildXAIImagesEditRequest("grok-imagine-image", "edit it", []string{"data:image/png;base64,AA==", "https://example.com/image.png"}, "b64_json", "3:2", "1k", 0)

	if got := gjson.GetBytes(req, "model").String(); got != "grok-imagine-image" {
		t.Fatalf("model = %q, want grok-imagine-image", got)
	}
	if got := gjson.GetBytes(req, "images.0.type").String(); got != "image_url" {
		t.Fatalf("images.0.type = %q, want image_url", got)
	}
	if got := gjson.GetBytes(req, "images.0.url").String(); got != "data:image/png;base64,AA==" {
		t.Fatalf("images.0.url = %q", got)
	}
	if got := gjson.GetBytes(req, "images.1.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("images.1.url = %q", got)
	}
	if gjson.GetBytes(req, "image").Exists() {
		t.Fatalf("multiple image edits must use images array: %s", string(req))
	}
}

func TestBuildXAIImagesEditRequestSingleImage(t *testing.T) {
	req := buildXAIImagesEditRequest("grok-imagine-image", "edit it", []string{"https://example.com/image.png"}, "url", "", "", 0)

	if got := gjson.GetBytes(req, "image.type").String(); got != "image_url" {
		t.Fatalf("image.type = %q, want image_url", got)
	}
	if got := gjson.GetBytes(req, "image.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("image.url = %q", got)
	}
	if gjson.GetBytes(req, "images").Exists() {
		t.Fatalf("single image edit must use image object: %s", string(req))
	}
}

func TestBuildOpenAICompatImagesJSONRequestPreservesStreamForStreaming(t *testing.T) {
	req := buildOpenAICompatImagesJSONRequest([]byte(`{"model":"compat-image","prompt":"draw","stream":false}`), "upstream-image", true)

	if got := gjson.GetBytes(req, "model").String(); got != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image; body=%s", got, string(req))
	}
	if !gjson.GetBytes(req, "stream").Bool() {
		t.Fatalf("stream flag missing: %s", string(req))
	}
}

func TestBuildOpenAICompatImagesJSONRequestDropsStreamForNonStreaming(t *testing.T) {
	req := buildOpenAICompatImagesJSONRequest([]byte(`{"model":"compat-image","prompt":"draw","stream":true}`), "upstream-image", false)

	if got := gjson.GetBytes(req, "model").String(); got != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image; body=%s", got, string(req))
	}
	if gjson.GetBytes(req, "stream").Exists() {
		t.Fatalf("stream flag should be removed from non-streaming request: %s", string(req))
	}
}

func TestBuildOpenAICompatImagesMultipartRequestPreservesStreamAndFileContentType(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if errWrite := writer.WriteField("model", "compat-image"); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("stream", "false"); errWrite != nil {
		t.Fatalf("write stream field: %v", errWrite)
	}
	if errWrite := writer.WriteField("prompt", "edit"); errWrite != nil {
		t.Fatalf("write prompt field: %v", errWrite)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
	header.Set("Content-Type", "image/png")
	part, errCreate := writer.CreatePart(header)
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := part.Write([]byte("png-data")); errWrite != nil {
		t.Fatalf("write image field: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	reader := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary())
	form, errRead := reader.ReadForm(32 << 20)
	if errRead != nil {
		t.Fatalf("read source form: %v", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			t.Fatalf("remove source form files: %v", errRemove)
		}
	}()

	out, contentType, errBuild := buildOpenAICompatImagesMultipartRequest(form, "upstream-image", true)
	if errBuild != nil {
		t.Fatalf("buildOpenAICompatImagesMultipartRequest error: %v", errBuild)
	}
	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil {
		t.Fatalf("parse content type: %v", errParse)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q, want multipart/form-data", mediaType)
	}
	rewrittenReader := multipart.NewReader(bytes.NewReader(out), params["boundary"])
	rewrittenForm, errRead := rewrittenReader.ReadForm(32 << 20)
	if errRead != nil {
		t.Fatalf("read rewritten form: %v", errRead)
	}
	defer func() {
		if errRemove := rewrittenForm.RemoveAll(); errRemove != nil {
			t.Fatalf("remove rewritten form files: %v", errRemove)
		}
	}()
	if got := rewrittenForm.Value["model"]; len(got) != 1 || got[0] != "upstream-image" {
		t.Fatalf("model values = %#v, want upstream-image", got)
	}
	if got := rewrittenForm.Value["stream"]; len(got) != 1 || got[0] != "true" {
		t.Fatalf("stream values = %#v, want true", got)
	}
	if got := rewrittenForm.Value["prompt"]; len(got) != 1 || got[0] != "edit" {
		t.Fatalf("prompt values = %#v, want edit", got)
	}
	if got := rewrittenForm.File["image"]; len(got) != 1 || got[0].Header.Get("Content-Type") != "image/png" {
		t.Fatalf("image headers = %#v, want image/png", got)
	}
}

func TestBuildImagesAPIResponseFromXAI(t *testing.T) {
	payload := []byte(`{"created":123,"data":[{"b64_json":"AA==","revised_prompt":"refined","mime_type":"image/png"}],"usage":{"total_tokens":0}}`)

	out, err := buildImagesAPIResponseFromXAI(payload, "b64_json")
	if err != nil {
		t.Fatalf("buildImagesAPIResponseFromXAI() error = %v", err)
	}

	if got := gjson.GetBytes(out, "created").Int(); got != 123 {
		t.Fatalf("created = %d, want 123", got)
	}
	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("data.0.b64_json = %q, want AA==", got)
	}
	if got := gjson.GetBytes(out, "data.0.revised_prompt").String(); got != "refined" {
		t.Fatalf("data.0.revised_prompt = %q, want refined", got)
	}
	if !gjson.GetBytes(out, "usage").Exists() {
		t.Fatalf("usage missing: %s", string(out))
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

func TestCollectImagesFromResponsesStreamCompleted(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`event: response.completed
data: {"type":"response.completed","response":{"created_at":123,"output":[{"type":"image_generation_call","result":"image-data","output_format":"png","revised_prompt":"refined"}],"tool_usage":{"image_gen":{"total_tokens":7}}}}

`)
	close(data)
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if errMsg != nil {
		t.Fatalf("collectImagesFromResponsesStream() error = %v", errMsg.Error)
	}
	if got := gjson.GetBytes(out, "created").Int(); got != 123 {
		t.Fatalf("created = %d, want 123", got)
	}
	if got := gjson.GetBytes(out, "data.0.b64_json").String(); got != "image-data" {
		t.Fatalf("data.0.b64_json = %q, want image-data", got)
	}
	if got := gjson.GetBytes(out, "data.0.revised_prompt").String(); got != "refined" {
		t.Fatalf("data.0.revised_prompt = %q, want refined", got)
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 7 {
		t.Fatalf("usage.total_tokens = %d, want 7", got)
	}
}

func TestCollectImagesFromResponsesStreamMissingCompleted(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`event: response.created
data: {"type":"response.created","response":{"id":"resp-1"}}

`)
	close(data)
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=missing_response_completed")
	requireImagesStreamErrorContains(t, errMsg, "saw_response_completed=false")
	requireImagesStreamErrorContains(t, errMsg, "saw_first_event=true")
	requireImagesStreamErrorContains(t, errMsg, `last_event_type="response.created"`)
	requireImagesStreamErrorContains(t, errMsg, `last_data_type="response.created"`)
	requireImagesStreamErrorContains(t, errMsg, "event_count=1")
	requireImagesStreamErrorContains(t, errMsg, "data_count=1")
	requireImagesStreamErrorContains(t, errMsg, "chunk_count=1")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="data_channel_closed"`)
	requireImagesStreamErrorContains(t, errMsg, "cause=upstream_stream_closed")
}

func TestCollectImagesFromResponsesStreamUpstreamClosedWithoutPayload(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=upstream_stream_closed")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="upstream_stream_closed"`)
	requireImagesStreamErrorContains(t, errMsg, "saw_response_completed=false")
}

func TestCollectImagesFromResponsesStreamPreservesAddonOnWrappedErrors(t *testing.T) {
	tests := []struct {
		name string
		msg  *interfaces.ErrorMessage
	}{
		{
			name: "scanner error",
			msg: &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      errors.New("scanner read failed"),
				Addon: http.Header{
					"Retry-After":  {"30"},
					"X-Request-Id": {"req-1", "req-2"},
				},
			},
		},
		{
			name: "closed without error",
			msg: &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
				Addon: http.Header{
					"Retry-After":  {"60"},
					"X-Request-Id": {"req-3"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- tt.msg
			close(errs)

			_, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

			if errMsg == nil {
				t.Fatal("errMsg = nil, want wrapped error")
			}
			if got := errMsg.Addon.Get("Retry-After"); got != tt.msg.Addon.Get("Retry-After") {
				t.Fatalf("Retry-After = %q, want %q", got, tt.msg.Addon.Get("Retry-After"))
			}
			if got, want := errMsg.Addon.Values("X-Request-Id"), tt.msg.Addon.Values("X-Request-Id"); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("X-Request-Id = %#v, want %#v", got, want)
			}

			tt.msg.Addon.Set("Retry-After", "mutated")
			if got := errMsg.Addon.Get("Retry-After"); got == "mutated" {
				t.Fatal("wrapped Addon shares source header map")
			}
		})
	}
}

func TestCollectImagesFromResponsesStreamScannerError(t *testing.T) {
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errors.New("scanner read failed")}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=scanner_error")
	requireImagesStreamErrorContains(t, errMsg, "cause=scanner_error")
	requireImagesStreamErrorContains(t, errMsg, `scanner_error_type="scanner_error"`)
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="scanner_error"`)
}

func TestCollectImagesFromResponsesStreamContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)

	out, errMsg := collectImagesFromResponsesStream(ctx, data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusGatewayTimeout, "classification=context_timeout")
	requireImagesStreamErrorContains(t, errMsg, "cause=context_deadline_exceeded")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="context_timeout"`)
}

func TestCollectImagesFromResponsesStreamContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)

	out, errMsg := collectImagesFromResponsesStream(ctx, data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusRequestTimeout, "classification=context_canceled")
	requireImagesStreamErrorContains(t, errMsg, "cause=context_canceled")
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="context_canceled"`)
}

func TestCollectImagesFromResponsesStreamHTTP2Reset(t *testing.T) {
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusInternalServerError,
		Error:      errors.New("stream error: stream ID 15; INTERNAL_ERROR; received from peer"),
	}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusInternalServerError, "classification=h2_stream_reset")
	requireImagesStreamErrorContains(t, errMsg, "cause=http2_stream_reset")
	requireImagesStreamErrorContains(t, errMsg, `scanner_error_type="http2_stream_reset"`)
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="h2_stream_reset"`)
	requireImagesStreamErrorNotContains(t, errMsg, "stream ID 15")
}

func TestCollectImagesFromResponsesStreamHTTP2ResetRSTStream(t *testing.T) {
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("http2: RST_STREAM closed stream"),
	}
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=h2_stream_reset")
	requireImagesStreamErrorContains(t, errMsg, "cause=http2_stream_reset")
	requireImagesStreamErrorNotContains(t, errMsg, "RST_STREAM")
}

func TestCollectImagesFromResponsesStreamUpstreamErrorEvent(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`event: error
data: {"type":"error","message":"safe upstream error"}

`)
	close(data)
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	if out != nil {
		t.Fatalf("out = %s, want nil", string(out))
	}
	requireImagesStreamError(t, errMsg, http.StatusBadGateway, "classification=upstream_error_event")
	requireImagesStreamErrorContains(t, errMsg, "saw_error_event=true")
	requireImagesStreamErrorContains(t, errMsg, `last_event_type="error"`)
	requireImagesStreamErrorContains(t, errMsg, `last_data_type="error"`)
	requireImagesStreamErrorContains(t, errMsg, `stream_end_reason="upstream_error_event"`)
	requireImagesStreamErrorNotContains(t, errMsg, "safe upstream error")
}

func TestCollectImagesFromResponsesStreamErrorSummaryDoesNotLeakSensitiveData(t *testing.T) {
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`event: response.created
data: {"type":"response.created","response":{"id":"resp-1"}}

`)
	close(data)
	close(errs)

	_, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")

	for _, forbidden := range []string{
		"Authorization",
		"Cookie",
		"API key",
		"api_key",
		"prompt",
		"base64",
		"b64_json",
		`"response":{"id":"resp-1"}`,
		"data:",
		"event: response.created",
	} {
		requireImagesStreamErrorNotContains(t, errMsg, forbidden)
	}
}

func requireImagesStreamError(t *testing.T, errMsg *interfaces.ErrorMessage, status int, contains string) {
	t.Helper()
	if errMsg == nil {
		t.Fatalf("errMsg = nil, want status %d containing %q", status, contains)
	}
	if errMsg.StatusCode != status {
		t.Fatalf("status = %d, want %d: %v", errMsg.StatusCode, status, errMsg.Error)
	}
	requireImagesStreamErrorContains(t, errMsg, contains)
}

func requireImagesStreamErrorContains(t *testing.T, errMsg *interfaces.ErrorMessage, contains string) {
	t.Helper()
	if errMsg == nil || errMsg.Error == nil {
		t.Fatalf("errMsg/error is nil, want containing %q", contains)
	}
	if !strings.Contains(errMsg.Error.Error(), contains) {
		t.Fatalf("error = %q, want containing %q", errMsg.Error.Error(), contains)
	}
}

func requireImagesStreamErrorNotContains(t *testing.T, errMsg *interfaces.ErrorMessage, contains string) {
	t.Helper()
	if errMsg == nil || errMsg.Error == nil {
		return
	}
	if strings.Contains(errMsg.Error.Error(), contains) {
		t.Fatalf("error = %q, want not containing %q", errMsg.Error.Error(), contains)
	}
}
