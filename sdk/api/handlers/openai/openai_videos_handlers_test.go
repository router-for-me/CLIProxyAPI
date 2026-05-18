package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func performVideosEndpointRequest(t *testing.T, method string, endpointPath string, contentType string, body io.Reader, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	switch method {
	case http.MethodGet:
		router.GET(endpointPath, handler)
	default:
		router.POST(endpointPath, handler)
	}

	req := httptest.NewRequest(method, endpointPath, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func TestVideosModelValidationAllowsXAIVideoModel(t *testing.T) {
	for _, model := range []string{"grok-imagine-video", "xai/grok-imagine-video", "x-ai/grok-imagine-video", "grok/grok-imagine-video"} {
		if !isSupportedVideosModel(model) {
			t.Fatalf("expected %s to be supported", model)
		}
	}
	if isSupportedVideosModel("sora-2") {
		t.Fatal("expected sora-2 to be rejected")
	}
	if isSupportedVideosModel("codex/grok-imagine-video") {
		t.Fatal("expected codex/grok-imagine-video to be rejected")
	}
}

func TestBuildXAIVideosCreateRequest(t *testing.T) {
	rawJSON := []byte(`{"model":"xai/grok-imagine-video","prompt":"a cat playing piano","seconds":"8","size":"1280x720","input_reference":{"image_url":"https://example.com/cat.png"}}`)

	req, meta, err := buildXAIVideosCreateRequest(rawJSON, "xai/grok-imagine-video")
	if err != nil {
		t.Fatalf("buildXAIVideosCreateRequest() error = %v", err)
	}

	if got := gjson.GetBytes(req, "model").String(); got != defaultXAIVideosModel {
		t.Fatalf("model = %q, want %s", got, defaultXAIVideosModel)
	}
	if got := gjson.GetBytes(req, "prompt").String(); got != "a cat playing piano" {
		t.Fatalf("prompt = %q", got)
	}
	if got := gjson.GetBytes(req, "duration").Int(); got != 8 {
		t.Fatalf("duration = %d, want 8", got)
	}
	if got := gjson.GetBytes(req, "aspect_ratio").String(); got != "16:9" {
		t.Fatalf("aspect_ratio = %q, want 16:9", got)
	}
	if got := gjson.GetBytes(req, "resolution").String(); got != "720p" {
		t.Fatalf("resolution = %q, want 720p", got)
	}
	if got := gjson.GetBytes(req, "image.url").String(); got != "https://example.com/cat.png" {
		t.Fatalf("image.url = %q", got)
	}
	if meta.Seconds != "8" || meta.Size != "1280x720" || meta.Prompt != "a cat playing piano" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

func TestBuildXAIVideosCreateRequestAllowsCustomSeconds(t *testing.T) {
	rawJSON := []byte(`{"model":"grok-imagine-video","prompt":"a cat playing piano","seconds":"6"}`)

	req, meta, err := buildXAIVideosCreateRequest(rawJSON, "grok-imagine-video")
	if err != nil {
		t.Fatalf("buildXAIVideosCreateRequest() error = %v", err)
	}

	if got := gjson.GetBytes(req, "duration").Int(); got != 6 {
		t.Fatalf("duration = %d, want 6", got)
	}
	if meta.Seconds != "6" {
		t.Fatalf("meta seconds = %q, want 6", meta.Seconds)
	}
}

func TestBuildXAIVideosCreateRequestRejectsFileIDReference(t *testing.T) {
	rawJSON := []byte(`{"prompt":"animate","input_reference":{"file_id":"file_123"}}`)

	_, _, err := buildXAIVideosCreateRequest(rawJSON, defaultXAIVideosModel)
	if err == nil || !strings.Contains(err.Error(), "input_reference.file_id is not supported") {
		t.Fatalf("error = %v, want unsupported file_id error", err)
	}
}

func TestBuildVideosCreateAPIResponseFromXAI(t *testing.T) {
	meta := xaiVideoCreateMetadata{
		Model:     defaultXAIVideosModel,
		Prompt:    "animate",
		Seconds:   "4",
		Size:      "720x1280",
		CreatedAt: 123,
	}
	out, err := buildVideosCreateAPIResponseFromXAI([]byte(`{"request_id":"vid_123"}`), meta)
	if err != nil {
		t.Fatalf("buildVideosCreateAPIResponseFromXAI() error = %v", err)
	}

	if got := gjson.GetBytes(out, "id").String(); got != "vid_123" {
		t.Fatalf("id = %q, want vid_123", got)
	}
	if got := gjson.GetBytes(out, "object").String(); got != "video" {
		t.Fatalf("object = %q, want video", got)
	}
	if got := gjson.GetBytes(out, "status").String(); got != "queued" {
		t.Fatalf("status = %q, want queued", got)
	}
	if got := gjson.GetBytes(out, "created_at").Int(); got != 123 {
		t.Fatalf("created_at = %d, want 123", got)
	}
}

func TestBuildVideosRetrieveAPIResponseFromXAI(t *testing.T) {
	payload := []byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/video.mp4","duration":6,"respect_moderation":true},"model":"grok-imagine-video","usage":{"cost_in_usd_ticks":500000000},"progress":100}`)

	out, err := buildVideosRetrieveAPIResponseFromXAI("vid_123", payload, defaultXAIVideosModel)
	if err != nil {
		t.Fatalf("buildVideosRetrieveAPIResponseFromXAI() error = %v", err)
	}

	if got := gjson.GetBytes(out, "id").String(); got != "vid_123" {
		t.Fatalf("id = %q, want vid_123", got)
	}
	if got := gjson.GetBytes(out, "status").String(); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if got := gjson.GetBytes(out, "seconds").String(); got != "6" {
		t.Fatalf("seconds = %q, want 6", got)
	}
	if got := gjson.GetBytes(out, "video.url").String(); got != "https://vidgen.x.ai/video.mp4" {
		t.Fatalf("video.url = %q", got)
	}
	if !gjson.GetBytes(out, "usage").Exists() {
		t.Fatalf("usage missing: %s", string(out))
	}
}

func TestVideosCreateRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"sora-2","prompt":"make a video"}`)

	resp := performVideosEndpointRequest(t, http.MethodPost, videosPath, "application/json", body, handler.VideosCreate)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	message := gjson.GetBytes(resp.Body.Bytes(), "error.message").String()
	expectedMessage := "Model sora-2 is not supported on " + videosPath + ". Use " + defaultXAIVideosModel + "."
	if message != expectedMessage {
		t.Fatalf("error message = %q, want %q", message, expectedMessage)
	}
}

func TestXAIVideosNativeRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"sora-2","prompt":"make a video"}`)

	resp := performVideosEndpointRequest(t, http.MethodPost, xaiVideosGenerationsAPI, "application/json", body, handler.XAIVideosGenerations)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	message := gjson.GetBytes(resp.Body.Bytes(), "error.message").String()
	expectedMessage := "Model sora-2 is not supported on " + xaiVideosGenerationsAPI + ", " + xaiVideosEditsAPI + ", or " + xaiVideosExtensionsAPI + ". Use " + defaultXAIVideosModel + "."
	if message != expectedMessage {
		t.Fatalf("error message = %q, want %q", message, expectedMessage)
	}
}

func TestXAIVideosNativeRejectsInvalidJSON(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":`)

	resp := performVideosEndpointRequest(t, http.MethodPost, xaiVideosEditsAPI, "application/json", body, handler.XAIVideosEdits)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error type = %q, want invalid_request_error", got)
	}
}

func TestVideosCreateFormRequest(t *testing.T) {
	rawJSON, err := videosCreateRequestFromFormContext("model=grok-imagine-video&prompt=make+a+video&seconds=4&size=720x1280&input_reference%5Bimage_url%5D=https%3A%2F%2Fexample.com%2Fa.png")
	if err != nil {
		t.Fatalf("videosCreateRequestFromFormContext() error = %v", err)
	}

	if got := gjson.GetBytes(rawJSON, "input_reference.image_url").String(); got != "https://example.com/a.png" {
		t.Fatalf("input_reference.image_url = %q", got)
	}
}

func videosCreateRequestFromFormContext(body string) ([]byte, error) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var rawJSON []byte
	var err error
	router.POST(videosPath, func(c *gin.Context) {
		rawJSON, err = videosCreateRequestFromForm(c)
	})
	req := httptest.NewRequest(http.MethodPost, videosPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return rawJSON, err
}
