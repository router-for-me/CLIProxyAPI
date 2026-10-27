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
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
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
	expectedMessage := "Model " + model + " is not supported on " + imagesGenerationsPath + " or " + imagesEditsPath + ". Use " + gptImage15Model + ", " + defaultImagesToolModel + ", " + gptImage25FlareModel + ", " + gptImage25SunburstModel + ", " + gptImage25Model + ", " + defaultXAIImagesModel + ", " + xaiImagesQualityModel + ", " + xaiImages20Model + ", or a configured openai-compatibility image model."
	if message != expectedMessage {
		t.Fatalf("error message = %q, want %q", message, expectedMessage)
	}
	if errorType := gjson.GetBytes(resp.Body.Bytes(), "error.type").String(); errorType != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", errorType)
	}
}

func TestImagesModelValidationAllowsGPTImageAndXAIModels(t *testing.T) {
	for _, model := range []string{
		"gpt-image-1.5",
		"codex/gpt-image-1.5",
		"gpt-image-2",
		"codex/gpt-image-2",
		"gpt-image-2.5-flare",
		"codex/gpt-image-2.5-flare",
		"gpt-image-2.5-sunburst",
		"codex/gpt-image-2.5-sunburst",
		"gpt-image-2.5",
		"codex/gpt-image-2.5",
		"grok-imagine-image",
		"xai/grok-imagine-image",
		"grok-imagine-image-quality",
		"xai/grok-imagine-image-quality",
		"grok-imagine-image-2.0",
		"xai/grok-imagine-image-2.0",
	} {
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

func TestCanonicalXAIImagesModelPreservesImage20(t *testing.T) {
	for _, model := range []string{"grok-imagine-image-2.0", "xai/grok-imagine-image-2.0", "XAI/Grok-Imagine-Image-2.0"} {
		if got := canonicalXAIImagesModel(model); got != xaiImages20Model {
			t.Fatalf("canonicalXAIImagesModel(%q) = %q, want %s", model, got, xaiImages20Model)
		}
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

func TestBuildXAIImagesGenerationsRequestPreservesQuality(t *testing.T) {
	for _, tc := range []struct {
		name        string
		rawJSON     string
		wantQuality string
		hasQuality  bool
	}{
		{
			name:        "explicit medium",
			rawJSON:     `{"model":"grok-imagine-image-2.0","prompt":"circle","quality":"medium"}`,
			wantQuality: "medium",
			hasQuality:  true,
		},
		{
			name:        "explicit low",
			rawJSON:     `{"model":"grok-imagine-image-2.0","prompt":"circle","quality":"low"}`,
			wantQuality: "low",
			hasQuality:  true,
		},
		{
			name:        "trimmed whitespace",
			rawJSON:     `{"model":"grok-imagine-image-2.0","prompt":"circle","quality":"  high  "}`,
			wantQuality: "high",
			hasQuality:  true,
		},
		{
			name:       "empty quality omitted",
			rawJSON:    `{"model":"grok-imagine-image-2.0","prompt":"circle","quality":""}`,
			hasQuality: false,
		},
		{
			name:       "blank quality omitted",
			rawJSON:    `{"model":"grok-imagine-image-2.0","prompt":"circle","quality":"   "}`,
			hasQuality: false,
		},
		{
			name:       "omitted quality omitted",
			rawJSON:    `{"model":"grok-imagine-image-2.0","prompt":"circle"}`,
			hasQuality: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := buildXAIImagesGenerationsRequest([]byte(tc.rawJSON), "grok-imagine-image-2.0", "b64_json")
			q := gjson.GetBytes(req, "quality")
			if tc.hasQuality {
				if !q.Exists() || q.String() != tc.wantQuality {
					t.Fatalf("quality = %q (exists=%v), want %q; req = %s", q.String(), q.Exists(), tc.wantQuality, string(req))
				}
			} else {
				if q.Exists() {
					t.Fatalf("expected quality to be omitted, got %q; req = %s", q.String(), string(req))
				}
			}
		})
	}
}

func TestBuildXAIImagesGenerationsRequestNineByTwenty(t *testing.T) {
	rawJSON := []byte(`{"model":"grok-imagine-image-quality","prompt":"kitten","aspect_ratio":"9:20","resolution":"2k","n":1,"response_format":"b64_json"}`)

	req := buildXAIImagesGenerationsRequest(rawJSON, "grok-imagine-image-quality", "b64_json")

	if got := gjson.GetBytes(req, "aspect_ratio").String(); got != "9:20" {
		t.Fatalf("aspect_ratio = %q, want 9:20", got)
	}
	if got := gjson.GetBytes(req, "resolution").String(); got != "2k" {
		t.Fatalf("resolution = %q, want 2k", got)
	}
}

func TestBuildXAIImagesGenerationsRequestNineByTwentyFromSize(t *testing.T) {
	rawJSON := []byte(`{"model":"grok-imagine-image-quality","prompt":"kitten","size":"9:20","resolution":"2k"}`)

	req := buildXAIImagesGenerationsRequest(rawJSON, "grok-imagine-image-quality", "b64_json")

	if got := gjson.GetBytes(req, "aspect_ratio").String(); got != "9:20" {
		t.Fatalf("aspect_ratio from size = %q, want 9:20", got)
	}
	if got := gjson.GetBytes(req, "resolution").String(); got != "2k" {
		t.Fatalf("resolution = %q, want 2k", got)
	}
}

func TestBuildXAIImagesGenerationsRequestTwentyByNine(t *testing.T) {
	rawJSON := []byte(`{"model":"grok-imagine-image-quality","prompt":"kitten","aspect_ratio":"20:9","resolution":"2k","n":1,"response_format":"b64_json"}`)

	req := buildXAIImagesGenerationsRequest(rawJSON, "grok-imagine-image-quality", "b64_json")

	if got := gjson.GetBytes(req, "aspect_ratio").String(); got != "20:9" {
		t.Fatalf("aspect_ratio = %q, want 20:9", got)
	}
	if got := gjson.GetBytes(req, "resolution").String(); got != "2k" {
		t.Fatalf("resolution = %q, want 2k", got)
	}
}

func TestBuildXAIImagesGenerationsRequestTwentyByNineFromSize(t *testing.T) {
	rawJSON := []byte(`{"model":"grok-imagine-image-quality","prompt":"kitten","size":"20:9","resolution":"2k"}`)

	req := buildXAIImagesGenerationsRequest(rawJSON, "grok-imagine-image-quality", "b64_json")

	if got := gjson.GetBytes(req, "aspect_ratio").String(); got != "20:9" {
		t.Fatalf("aspect_ratio from size = %q, want 20:9", got)
	}
	if got := gjson.GetBytes(req, "resolution").String(); got != "2k" {
		t.Fatalf("resolution = %q, want 2k", got)
	}
}

func TestXAIImagesAspectRatioNineByTwenty(t *testing.T) {
	if got := xaiImagesAspectRatio("9:20", "1:1"); got != "9:20" {
		t.Fatalf("xaiImagesAspectRatio(9:20) = %q, want 9:20", got)
	}
	if got := xaiImagesAspectRatio("20:9", "1:1"); got != "20:9" {
		t.Fatalf("xaiImagesAspectRatio(20:9) = %q, want 20:9", got)
	}
	if got := xaiImagesAspectRatio("9:21", "1:1"); got != "1:1" {
		t.Fatalf("unknown ratio should keep fallback, got %q", got)
	}
	if got := xaiImagesAspectRatioFromSize("9:20", ""); got != "9:20" {
		t.Fatalf("xaiImagesAspectRatioFromSize(9:20) = %q, want 9:20", got)
	}
	if got := xaiImagesAspectRatioFromSize("20:9", ""); got != "20:9" {
		t.Fatalf("xaiImagesAspectRatioFromSize(20:9) = %q, want 20:9", got)
	}
}

func TestBuildXAIImagesEditRequest(t *testing.T) {
	req := buildXAIImagesEditRequest("grok-imagine-image", "edit it", []string{"data:image/png;base64,AA==", "https://example.com/image.png"}, "b64_json", "3:2", "1k", "medium", 0)

	if got := gjson.GetBytes(req, "model").String(); got != "grok-imagine-image" {
		t.Fatalf("model = %q, want grok-imagine-image", got)
	}
	if got := gjson.GetBytes(req, "quality").String(); got != "medium" {
		t.Fatalf("quality = %q, want medium", got)
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
	req := buildXAIImagesEditRequest("grok-imagine-image", "edit it", []string{"https://example.com/image.png"}, "url", "", "", "", 0)

	if got := gjson.GetBytes(req, "image.type").String(); got != "image_url" {
		t.Fatalf("image.type = %q, want image_url", got)
	}
	if got := gjson.GetBytes(req, "image.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("image.url = %q", got)
	}
	if gjson.GetBytes(req, "quality").Exists() {
		t.Fatalf("empty quality should be omitted: %s", string(req))
	}
	if gjson.GetBytes(req, "images").Exists() {
		t.Fatalf("single image edit must use image object: %s", string(req))
	}
}

func TestXAIImagesEditOptionsFromJSONPreservesQuality(t *testing.T) {
	aspectRatio, resolution, quality, n := xaiImagesEditOptionsFromJSON([]byte(`{"quality":"low","size":"1024x1024","resolution":"1k","n":1}`))
	if quality != "low" {
		t.Fatalf("quality = %q, want low", quality)
	}
	if aspectRatio != "1:1" {
		t.Fatalf("aspectRatio = %q, want 1:1", aspectRatio)
	}
	if resolution != "1k" {
		t.Fatalf("resolution = %q, want 1k", resolution)
	}
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}

	_, _, emptyQuality, _ := xaiImagesEditOptionsFromJSON([]byte(`{"n":1}`))
	if emptyQuality != "" {
		t.Fatalf("emptyQuality = %q, want empty", emptyQuality)
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

func TestSSEFrameAccumulatorFlushesDataOnlyFrame(t *testing.T) {
	accumulator := &sseFrameAccumulator{}
	chunk := []byte(`data: {"type":"image_generation.partial","partial_image_index":0}`)

	if frames := accumulator.AddChunk(chunk); len(frames) != 0 {
		t.Fatalf("AddChunk() emitted an unterminated data-only frame: %q", frames)
	}
	frames := accumulator.Flush()
	if len(frames) != 1 || string(frames[0]) != string(chunk) {
		t.Fatalf("Flush() frames = %q, want [%q]", frames, chunk)
	}
}

func TestWriteImagesStreamErrorEventSanitizesPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	raw := `{"error":{"code":"upstream_failed","message":"token=image-secret"},"debug":"` + strings.Repeat("x", 8192) + `"}`
	writeImagesStreamErrorEvent(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New(raw)})

	body := recorder.Body.String()
	if strings.Contains(body, "image-secret") || len(body) > 4096 || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("image stream error was not safely bounded: len=%d body=%q", len(body), body)
	}
}

func TestCollectImagesRejectsPayloadErrorBeforeCompleted(t *testing.T) {
	data := make(chan []byte, 1)
	data <- []byte("event: error\ndata: {\"type\":\"provider.error\",\"error\":{\"code\":\"failed\",\"message\":\"token=image-secret\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\"}]}}\n\n")
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")
	if len(out) != 0 || errMsg == nil || errMsg.Error == nil {
		t.Fatalf("payload error result out=%q err=%#v", out, errMsg)
	}
	if strings.Contains(errMsg.Error.Error(), "image-secret") || !strings.Contains(errMsg.Error.Error(), "[REDACTED]") {
		t.Fatalf("payload error was not sanitized: %q", errMsg.Error.Error())
	}
}

func TestForwardImagesStreamCancelsWithPayloadError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}
	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)
	var canceled error
	firstChunk := []byte("event: error\ndata: {\"error\":{\"message\":\"token=image-secret\"}}\n\n")

	h.forwardImagesStream(context.Background(), c, flusher, func(err error) { canceled = err }, data, errs, firstChunk, "b64_json", "image_generation", func(string, []byte) {})
	if canceled == nil || strings.Contains(canceled.Error(), "image-secret") || !strings.Contains(canceled.Error(), "[REDACTED]") {
		t.Fatalf("payload error cancel = %v body=%q", canceled, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("payload error event missing: %q", recorder.Body.String())
	}
}

func TestForwardRawImageStreamPrefersPendingErrorOnClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
	for i := 0; i < 100; i++ {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		data := make(chan []byte)
		close(data)
		errs := make(chan *interfaces.ErrorMessage, 1)
		errs <- &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: errors.New("image upstream busy")}
		close(errs)
		var canceled error

		h.forwardRawImageStream(context.Background(), c, func(err error) { canceled = err }, data, errs)
		if canceled == nil || !strings.Contains(canceled.Error(), "image upstream busy") {
			t.Fatalf("iteration %d: cancel=%v body=%q", i, canceled, recorder.Body.String())
		}
	}
}

func TestCollectImagesPrefersPendingErrorWhenDataChannelCloses(t *testing.T) {
	for i := 0; i < 100; i++ {
		data := make(chan []byte)
		close(data)
		errs := make(chan *interfaces.ErrorMessage, 1)
		want := &interfaces.ErrorMessage{
			StatusCode:     http.StatusTooManyRequests,
			Error:          errors.New("image upstream busy"),
			DirectResponse: true,
			Headers:        http.Header{"Retry-After": []string{"9"}},
		}
		errs <- want
		close(errs)

		_, got := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")
		if got != want {
			t.Fatalf("iteration %d: pending error = %#v, want original %#v", i, got, want)
		}
	}
}

func TestCollectImagesAllowsMultilineSSEData(t *testing.T) {
	data := make(chan []byte, 1)
	data <- []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"created_at\":1,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\"}]}}\n\n")
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	out, errMsg := collectImagesFromResponsesStream(context.Background(), data, errs, "b64_json")
	if errMsg != nil {
		t.Fatalf("collectImagesFromResponsesStream() error = %v", errMsg.Error)
	}
	if !strings.Contains(string(out), `"b64_json":"aW1hZ2U="`) {
		t.Fatalf("multiline image response = %q", out)
	}
}

func TestSSEFrameAccumulatorKeepsMultipleFramesDistinct(t *testing.T) {
	accumulator := &sseFrameAccumulator{}
	first := "event: first\ndata: {\"type\":\"first\"}\n\n"
	second := "event: second\ndata: {\"type\":\"second\"}\n\n"

	frames := accumulator.AddChunk([]byte(first + second))
	if len(frames) != 2 {
		t.Fatalf("AddChunk() returned %d frames, want 2: %q", len(frames), frames)
	}
	if string(frames[0]) != first || string(frames[1]) != second {
		t.Fatalf("frames were overwritten during buffer compaction: %q", frames)
	}
}

type xaiImagesCaptureCall struct {
	authID         string
	reqModel       string
	requestedModel string
	payloadModel   string
}

type xaiImagesCaptureExecutor struct {
	mu    sync.Mutex
	calls []xaiImagesCaptureCall
}

func (e *xaiImagesCaptureExecutor) Identifier() string { return "xai" }

func (e *xaiImagesCaptureExecutor) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	requestedModel := ""
	if opts.Metadata != nil {
		if v, ok := opts.Metadata[coreexecutor.RequestedModelMetadataKey].(string); ok {
			requestedModel = v
		}
	}
	e.mu.Lock()
	e.calls = append(e.calls, xaiImagesCaptureCall{
		authID:         authID,
		reqModel:       req.Model,
		requestedModel: requestedModel,
		payloadModel:   strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String()),
	})
	e.mu.Unlock()
	return coreexecutor.Response{
		Payload: []byte(`{"created":123,"data":[{"b64_json":"aW1hZ2U=","revised_prompt":"ok","mime_type":"image/png"}]}`),
	}, nil
}

func (e *xaiImagesCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "ExecuteStream not implemented"}
}

func (e *xaiImagesCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *xaiImagesCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *xaiImagesCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func (e *xaiImagesCaptureExecutor) Calls() []xaiImagesCaptureCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]xaiImagesCaptureCall, len(e.calls))
	copy(out, e.calls)
	return out
}

func newXAIImagesRoutingTestHandler(t *testing.T, forceModelPrefix bool, authID string, prefix string, modelIDs []string) (*OpenAIAPIHandler, *xaiImagesCaptureExecutor) {
	t.Helper()

	executor := &xaiImagesCaptureExecutor{}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "xai",
		Prefix:   prefix,
		Status:   coreauth.StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register: %v", errRegister)
	}

	models := make([]*registry.ModelInfo, 0, len(modelIDs))
	for _, id := range modelIDs {
		models = append(models, &registry.ModelInfo{
			ID:      id,
			Object:  "model",
			OwnedBy: "xai",
			Type:    "xai",
		})
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(auth.ID, auth.Provider, models)
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{ForceModelPrefix: forceModelPrefix}, manager)
	return NewOpenAIAPIHandler(base), executor
}

func TestXAIImagesRoutesWithPrefixedModelWhenForceModelPrefixEnabled(t *testing.T) {
	handler, executor := newXAIImagesRoutingTestHandler(t, true, "xai-prefixed-auth", "xai", []string{
		"xai/" + defaultXAIImagesModel,
		"xai/" + xaiImagesQualityModel,
		"xai/" + xaiImages20Model,
	})

	t.Run("generations non-stream", func(t *testing.T) {
		before := len(executor.Calls())
		body := strings.NewReader(`{"model":"xai/grok-imagine-image-2.0","prompt":"a red square","response_format":"b64_json"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").String(); got != "aW1hZ2U=" {
			t.Fatalf("data.0.b64_json = %q, want aW1hZ2U=", got)
		}
		calls := executor.Calls()
		if len(calls) != before+1 {
			t.Fatalf("executor calls = %d, want %d", len(calls), before+1)
		}
		last := calls[len(calls)-1]
		if last.authID != "xai-prefixed-auth" {
			t.Fatalf("authID = %q, want xai-prefixed-auth", last.authID)
		}
		if last.requestedModel != "xai/grok-imagine-image-2.0" {
			t.Fatalf("requestedModel = %q, want xai/grok-imagine-image-2.0", last.requestedModel)
		}
		if last.reqModel != xaiImages20Model {
			t.Fatalf("req.Model = %q, want %s", last.reqModel, xaiImages20Model)
		}
		if last.payloadModel != xaiImages20Model {
			t.Fatalf("payload model = %q, want %s", last.payloadModel, xaiImages20Model)
		}
	})

	t.Run("generations stream", func(t *testing.T) {
		before := len(executor.Calls())
		body := strings.NewReader(`{"model":"xai/grok-imagine-image-2.0","prompt":"a red square","response_format":"b64_json","stream":true}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), `"type":"image_generation.completed"`) {
			t.Fatalf("stream body = %q, want image_generation.completed event", resp.Body.String())
		}
		calls := executor.Calls()
		if len(calls) != before+1 {
			t.Fatalf("executor calls = %d, want %d", len(calls), before+1)
		}
		last := calls[len(calls)-1]
		if last.requestedModel != "xai/grok-imagine-image-2.0" || last.payloadModel != xaiImages20Model {
			t.Fatalf("call = %+v, want requestedModel=xai/grok-imagine-image-2.0 payloadModel=%s", last, xaiImages20Model)
		}
	})

	t.Run("edits JSON", func(t *testing.T) {
		before := len(executor.Calls())
		body := strings.NewReader(`{"model":"xai/grok-imagine-image-2.0","prompt":"edit this","images":[{"image_url":"data:image/png;base64,AA=="}]}`)
		resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		calls := executor.Calls()
		if len(calls) != before+1 {
			t.Fatalf("executor calls = %d, want %d", len(calls), before+1)
		}
		last := calls[len(calls)-1]
		if last.requestedModel != "xai/grok-imagine-image-2.0" || last.payloadModel != xaiImages20Model {
			t.Fatalf("call = %+v, want requestedModel=xai/grok-imagine-image-2.0 payloadModel=%s", last, xaiImages20Model)
		}
	})

	t.Run("edits multipart", func(t *testing.T) {
		before := len(executor.Calls())
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("model", "xai/grok-imagine-image-2.0"); err != nil {
			t.Fatalf("write model field: %v", err)
		}
		if err := writer.WriteField("prompt", "edit this"); err != nil {
			t.Fatalf("write prompt field: %v", err)
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create image part: %v", err)
		}
		if _, err := part.Write([]byte("png-bytes")); err != nil {
			t.Fatalf("write image part: %v", err)
		}
		if errClose := writer.Close(); errClose != nil {
			t.Fatalf("close multipart writer: %v", errClose)
		}

		resp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &body, handler.ImagesEdits)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		calls := executor.Calls()
		if len(calls) != before+1 {
			t.Fatalf("executor calls = %d, want %d", len(calls), before+1)
		}
		last := calls[len(calls)-1]
		if last.requestedModel != "xai/grok-imagine-image-2.0" || last.payloadModel != xaiImages20Model {
			t.Fatalf("call = %+v, want requestedModel=xai/grok-imagine-image-2.0 payloadModel=%s", last, xaiImages20Model)
		}
	})

	t.Run("case-insensitive prefixed model routes and strips prefix for upstream", func(t *testing.T) {
		before := len(executor.Calls())
		body := strings.NewReader(`{"model":"XAI/Grok-Imagine-Image-2.0","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		calls := executor.Calls()
		if len(calls) != before+1 {
			t.Fatalf("executor calls = %d, want %d", len(calls), before+1)
		}
		last := calls[len(calls)-1]
		if last.reqModel != xaiImages20Model || last.payloadModel != xaiImages20Model {
			t.Fatalf("call = %+v, want reqModel=%s payloadModel=%s", last, xaiImages20Model, xaiImages20Model)
		}
	})

	t.Run("unprefixed model rejected when force-model-prefix is enabled", func(t *testing.T) {
		before := len(executor.Calls())
		body := strings.NewReader(`{"model":"grok-imagine-image-2.0","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "error.message").String(); got != "unknown provider for model grok-imagine-image-2.0" {
			t.Fatalf("error.message = %q, want %q", got, "unknown provider for model grok-imagine-image-2.0")
		}
		if len(executor.Calls()) != before {
			t.Fatalf("expected no executor call for unprefixed model")
		}
	})
}

func TestXAIImagesSupportsBuiltInPrefixFallbackAndCustomCredentialPrefix(t *testing.T) {
	t.Run("built-in prefix falls back to unprefixed registered model when force-model-prefix is disabled", func(t *testing.T) {
		handler, executor := newXAIImagesRoutingTestHandler(t, false, "xai-unprefixed-auth", "", []string{
			defaultXAIImagesModel,
			xaiImagesQualityModel,
			xaiImages20Model,
		})
		body := strings.NewReader(`{"model":"xai/grok-imagine-image-2.0","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		calls := executor.Calls()
		if len(calls) != 1 {
			t.Fatalf("executor calls = %d, want 1", len(calls))
		}
		if calls[0].payloadModel != xaiImages20Model {
			t.Fatalf("payload model = %q, want %s", calls[0].payloadModel, xaiImages20Model)
		}
	})

	t.Run("custom xai credential prefix routes when force-model-prefix is enabled", func(t *testing.T) {
		handler, executor := newXAIImagesRoutingTestHandler(t, true, "xai-custom-prefix-auth", "my-xai", []string{
			"my-xai/" + xaiImages20Model,
		})
		body := strings.NewReader(`{"model":"my-xai/grok-imagine-image-2.0","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		calls := executor.Calls()
		if len(calls) != 1 {
			t.Fatalf("executor calls = %d, want 1", len(calls))
		}
		if calls[0].authID != "xai-custom-prefix-auth" || calls[0].requestedModel != "my-xai/grok-imagine-image-2.0" || calls[0].payloadModel != xaiImages20Model {
			t.Fatalf("call = %+v, want authID=xai-custom-prefix-auth requestedModel=my-xai/grok-imagine-image-2.0 payloadModel=%s", calls[0], xaiImages20Model)
		}
	})

	t.Run("built-in vendor aliases fall back to unprefixed registered model when force-model-prefix is enabled", func(t *testing.T) {
		handler, executor := newXAIImagesRoutingTestHandler(t, true, "xai-unprefixed-force-auth", "", []string{
			defaultXAIImagesModel,
			xaiImagesQualityModel,
			xaiImages20Model,
		})
		for _, aliasModel := range []string{
			"xai/grok-imagine-image-2.0",
			"x-ai/grok-imagine-image-2.0",
			"grok/grok-imagine-image-2.0",
		} {
			before := len(executor.Calls())
			body := strings.NewReader(`{"model":"` + aliasModel + `","prompt":"a red square"}`)
			resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
			if resp.Code != http.StatusOK {
				t.Fatalf("%s status = %d, want %d: %s", aliasModel, resp.Code, http.StatusOK, resp.Body.String())
			}
			calls := executor.Calls()
			if len(calls) != before+1 {
				t.Fatalf("%s executor calls = %d, want %d", aliasModel, len(calls), before+1)
			}
			last := calls[len(calls)-1]
			if last.authID != "xai-unprefixed-force-auth" || last.requestedModel != xaiImages20Model || last.reqModel != xaiImages20Model || last.payloadModel != xaiImages20Model {
				t.Fatalf("%s call = %+v, want authID=xai-unprefixed-force-auth requestedModel=%s reqModel=%s payloadModel=%s", aliasModel, last, xaiImages20Model, xaiImages20Model, xaiImages20Model)
			}
		}
	})

	t.Run("built-in vendor alias rejected when force-model-prefix is enabled and only custom-prefixed credential exists", func(t *testing.T) {
		handler, executor := newXAIImagesRoutingTestHandler(t, true, "xai-custom-prefix-only-auth", "my-xai", []string{
			"my-xai/" + xaiImages20Model,
		})
		body := strings.NewReader(`{"model":"xai/grok-imagine-image-2.0","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "error.message").String(); got != "unknown provider for model xai/grok-imagine-image-2.0" {
			t.Fatalf("error.message = %q, want %q", got, "unknown provider for model xai/grok-imagine-image-2.0")
		}
		if len(executor.Calls()) != 0 {
			t.Fatalf("expected no executor call, got %d", len(executor.Calls()))
		}
	})
}

func TestXAIImagesPayloadDefaultAndOverrideRules(t *testing.T) {
	var (
		mu                sync.Mutex
		gotRequests       [][]byte
		forceBase64Output bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotRequests = append(gotRequests, body)
		forceB64 := forceBase64Output
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if !forceB64 && gjson.GetBytes(body, "response_format").String() == "url" {
			_, _ = w.Write([]byte(`{"created":123,"data":[{"url":"https://img.x.ai/generated.png","revised_prompt":"ok"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"aW1hZ2U=","revised_prompt":"ok","mime_type":"image/png"}]}`))
	}))
	defer server.Close()

	newHandlerWithConfig := func(t *testing.T, authID string, cfg *internalconfig.Config) *OpenAIAPIHandler {
		t.Helper()
		manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
		manager.RegisterExecutor(runtimeexecutor.NewXAIExecutor(cfg))
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "xai",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"base_url":  server.URL,
				"auth_kind": "oauth",
			},
			Metadata: map[string]any{"access_token": "xai-token"},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("manager.Register: %v", errRegister)
		}
		modelRegistry := registry.GetGlobalRegistry()
		modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
			{ID: defaultXAIImagesModel, Object: "model", OwnedBy: "xai", Type: "xai"},
			{ID: xaiImagesQualityModel, Object: "model", OwnedBy: "xai", Type: "xai"},
			{ID: xaiImages20Model, Object: "model", OwnedBy: "xai", Type: "xai"},
		})
		manager.RefreshSchedulerEntry(auth.ID)
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(auth.ID)
		})
		base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
		return NewOpenAIAPIHandler(base)
	}

	defaultURLCfg := &internalconfig.Config{
		Payload: internalconfig.PayloadConfig{
			Default: []internalconfig.PayloadRule{{
				Models: []internalconfig.PayloadModelRule{{Name: "grok-imagine-*"}},
				Params: map[string]any{
					"response_format": "url",
					"aspect_ratio":    "16:9",
					"resolution":      "2k",
				},
			}},
		},
	}
	handler := newHandlerWithConfig(t, "xai-payload-default-auth", defaultURLCfg)

	t.Run("generations applies payload default response_format url when omitted by client", func(t *testing.T) {
		body := strings.NewReader(`{"model":"grok-imagine-image","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "url" {
			t.Fatalf("upstream response_format = %q, want url; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "aspect_ratio").String(); got != "16:9" {
			t.Fatalf("upstream aspect_ratio = %q, want 16:9; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "resolution").String(); got != "2k" {
			t.Fatalf("upstream resolution = %q, want 2k; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "https://img.x.ai/generated.png" {
			t.Fatalf("data.0.url = %q, want https://img.x.ai/generated.png; resp=%s", got, resp.Body.String())
		}
		if gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").Exists() {
			t.Fatalf("unexpected data.0.b64_json in response: %s", resp.Body.String())
		}
	})

	t.Run("generations preserves explicit client response_format and size under payload default", func(t *testing.T) {
		body := strings.NewReader(`{"model":"grok-imagine-image-quality","prompt":"a blue circle","response_format":"b64_json","size":"1024x1024"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "b64_json" {
			t.Fatalf("upstream response_format = %q, want b64_json; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "aspect_ratio").String(); got != "1:1" {
			t.Fatalf("upstream aspect_ratio = %q, want 1:1; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "resolution").String(); got != "2k" {
			t.Fatalf("upstream resolution = %q, want 2k; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").String(); got != "aW1hZ2U=" {
			t.Fatalf("data.0.b64_json = %q, want aW1hZ2U=; resp=%s", got, resp.Body.String())
		}

		explicitAllBody := strings.NewReader(`{"model":"grok-imagine-image-quality","prompt":"a blue circle","response_format":"b64_json","aspect_ratio":"4:3","resolution":"1k"}`)
		explicitAllResp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", explicitAllBody, handler.ImagesGenerations)
		if explicitAllResp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", explicitAllResp.Code, http.StatusOK, explicitAllResp.Body.String())
		}
		mu.Lock()
		explicitLastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(explicitLastReq, "response_format").String(); got != "b64_json" {
			t.Fatalf("upstream response_format = %q, want b64_json; body=%s", got, explicitLastReq)
		}
		if got := gjson.GetBytes(explicitLastReq, "aspect_ratio").String(); got != "4:3" {
			t.Fatalf("upstream aspect_ratio = %q, want 4:3; body=%s", got, explicitLastReq)
		}
		if got := gjson.GetBytes(explicitLastReq, "resolution").String(); got != "1k" {
			t.Fatalf("upstream resolution = %q, want 1k; body=%s", got, explicitLastReq)
		}
	})

	t.Run("generations stream applies payload default response_format url", func(t *testing.T) {
		mu.Lock()
		forceBase64Output = true
		mu.Unlock()
		defer func() {
			mu.Lock()
			forceBase64Output = false
			mu.Unlock()
		}()

		body := strings.NewReader(`{"model":"grok-imagine-image-2.0","prompt":"a green triangle","stream":true}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "url" {
			t.Fatalf("upstream response_format = %q, want url; body=%s", got, lastReq)
		}
		if !strings.Contains(resp.Body.String(), `"url":"data:image/png;base64,aW1hZ2U="`) {
			t.Fatalf("stream response missing data url formatted from finalized response_format: %s", resp.Body.String())
		}
	})

	t.Run("edits JSON applies payload default response_format url", func(t *testing.T) {
		body := strings.NewReader(`{"model":"grok-imagine-image","prompt":"edit this","images":[{"image_url":"https://example.com/in.png"}]}`)
		resp := performImagesEndpointRequest(t, imagesEditsPath, "application/json", body, handler.ImagesEdits)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "url" {
			t.Fatalf("upstream response_format = %q, want url; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "aspect_ratio").String(); got != "16:9" {
			t.Fatalf("upstream aspect_ratio = %q, want 16:9; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "resolution").String(); got != "2k" {
			t.Fatalf("upstream resolution = %q, want 2k; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "https://img.x.ai/generated.png" {
			t.Fatalf("data.0.url = %q, want https://img.x.ai/generated.png; resp=%s", got, resp.Body.String())
		}
	})

	t.Run("edits multipart applies payload default response_format url", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if errWrite := writer.WriteField("model", "grok-imagine-image"); errWrite != nil {
			t.Fatalf("write model field: %v", errWrite)
		}
		if errWrite := writer.WriteField("prompt", "edit this"); errWrite != nil {
			t.Fatalf("write prompt field: %v", errWrite)
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
		header.Set("Content-Type", "image/png")
		part, errCreate := writer.CreatePart(header)
		if errCreate != nil {
			t.Fatalf("create image part: %v", errCreate)
		}
		if _, errWrite := part.Write([]byte("png-bytes")); errWrite != nil {
			t.Fatalf("write image part: %v", errWrite)
		}
		if errClose := writer.Close(); errClose != nil {
			t.Fatalf("close multipart writer: %v", errClose)
		}

		resp := performImagesEndpointRequest(t, imagesEditsPath, writer.FormDataContentType(), &body, handler.ImagesEdits)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "url" {
			t.Fatalf("upstream response_format = %q, want url; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "aspect_ratio").String(); got != "16:9" {
			t.Fatalf("upstream aspect_ratio = %q, want 16:9; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "resolution").String(); got != "2k" {
			t.Fatalf("upstream resolution = %q, want 2k; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "https://img.x.ai/generated.png" {
			t.Fatalf("data.0.url = %q, want https://img.x.ai/generated.png; resp=%s", got, resp.Body.String())
		}
	})

	t.Run("generations response formatting uses finalized response_format when upstream returns b64_json", func(t *testing.T) {
		mu.Lock()
		forceBase64Output = true
		mu.Unlock()
		defer func() {
			mu.Lock()
			forceBase64Output = false
			mu.Unlock()
		}()

		body := strings.NewReader(`{"model":"grok-imagine-image","prompt":"a red square"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, handler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.url").String(); got != "data:image/png;base64,aW1hZ2U=" {
			t.Fatalf("data.0.url = %q, want data:image/png;base64,aW1hZ2U=; resp=%s", got, resp.Body.String())
		}
	})

	t.Run("generations override to b64_json overrides client url for both upstream and downstream response", func(t *testing.T) {
		overrideB64Cfg := &internalconfig.Config{
			Payload: internalconfig.PayloadConfig{
				Override: []internalconfig.PayloadRule{{
					Models: []internalconfig.PayloadModelRule{{Name: "grok-imagine-*"}},
					Params: map[string]any{
						"response_format": "b64_json",
						"aspect_ratio":    "9:16",
						"resolution":      "2k",
					},
				}},
			},
		}
		overrideHandler := newHandlerWithConfig(t, "xai-payload-override-auth", overrideB64Cfg)

		body := strings.NewReader(`{"model":"grok-imagine-image","prompt":"a red square","response_format":"url","aspect_ratio":"1:1","resolution":"1k"}`)
		resp := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json", body, overrideHandler.ImagesGenerations)
		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
		}
		mu.Lock()
		lastReq := gotRequests[len(gotRequests)-1]
		mu.Unlock()
		if got := gjson.GetBytes(lastReq, "response_format").String(); got != "b64_json" {
			t.Fatalf("upstream response_format = %q, want b64_json; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "aspect_ratio").String(); got != "9:16" {
			t.Fatalf("upstream aspect_ratio = %q, want 9:16; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(lastReq, "resolution").String(); got != "2k" {
			t.Fatalf("upstream resolution = %q, want 2k; body=%s", got, lastReq)
		}
		if got := gjson.GetBytes(resp.Body.Bytes(), "data.0.b64_json").String(); got != "aW1hZ2U=" {
			t.Fatalf("data.0.b64_json = %q, want aW1hZ2U=; resp=%s", got, resp.Body.String())
		}
		if gjson.GetBytes(resp.Body.Bytes(), "data.0.url").Exists() {
			t.Fatalf("unexpected data.0.url in response: %s", resp.Body.String())
		}
	})
}
