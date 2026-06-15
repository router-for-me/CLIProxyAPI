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
	for _, model := range []string{
		"grok-imagine-video",
		"xai/grok-imagine-video",
		"x-ai/grok-imagine-video",
		"grok/grok-imagine-video",
		"grok-imagine-video-1.5-preview",
		"xai/grok-imagine-video-1.5-preview",
		"x-ai/grok-imagine-video-1.5-preview",
		"grok/grok-imagine-video-1.5-preview",
	} {
		if !isSupportedVideosModel(model) {
			t.Fatalf("expected %s to be supported", model)
		}
	}
	if !isSupportedVideosModel("sora-2") {
		t.Fatal("expected sora-2 to be supported by the OpenAI video wrapper")
	}
	if isXAIVideosModel("sora-2") {
		t.Fatal("expected sora-2 not to be treated as a native xAI video model")
	}
	if isSupportedVideosModel("codex/grok-imagine-video") {
		t.Fatal("expected codex/grok-imagine-video to be rejected")
	}
	if isSupportedVideosModel("codex/grok-imagine-video-1.5-preview") {
		t.Fatal("expected codex/grok-imagine-video-1.5-preview to be rejected")
	}
}

func TestBuildXAIVideosCreateRequestMapsSoraModelToXAIBackend(t *testing.T) {
	rawJSON := []byte(`{"model":"sora-2","prompt":"a cat playing piano","seconds":"8"}`)

	req, meta, err := buildXAIVideosCreateRequest(rawJSON, "sora-2")
	if err != nil {
		t.Fatalf("buildXAIVideosCreateRequest() error = %v", err)
	}

	if got := gjson.GetBytes(req, "model").String(); got != defaultXAIVideosModel {
		t.Fatalf("upstream model = %q, want %s", got, defaultXAIVideosModel)
	}
	if meta.Model != "sora-2" {
		t.Fatalf("response model = %q, want sora-2", meta.Model)
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

func TestBuildXAIVideosCreateRequestAllowsPreviewModel(t *testing.T) {
	rawJSON := []byte(`{"model":"xai/grok-imagine-video-1.5-preview","prompt":"a cat playing piano","seconds":"8"}`)

	req, meta, err := buildXAIVideosCreateRequest(rawJSON, "xai/grok-imagine-video-1.5-preview")
	if err != nil {
		t.Fatalf("buildXAIVideosCreateRequest() error = %v", err)
	}

	if got := gjson.GetBytes(req, "model").String(); got != xaiVideos15PreviewModel {
		t.Fatalf("model = %q, want %s", got, xaiVideos15PreviewModel)
	}
	if meta.Model != xaiVideos15PreviewModel {
		t.Fatalf("meta model = %q, want %s", meta.Model, xaiVideos15PreviewModel)
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
	payload := []byte(`{"object":"video","id":"91989464-273f-95df-8197-703b4fefd40e","model":"grok-imagine-video","status":"completed","progress":100,"seconds":"4","video":{"url":"https://vidgen.x.ai/xai-vidgen-bucket/xai-video-08609066-e7e9-43ba-bd8d-bd29cb6221d9.mp4","duration":4,"respect_moderation":true},"usage":{"cost_in_usd_ticks":2800000000}}`)

	out, err := buildVideosRetrieveAPIResponseFromXAI("91989464-273f-95df-8197-703b4fefd40e", payload, defaultOpenAIVideosModel)
	if err != nil {
		t.Fatalf("buildVideosRetrieveAPIResponseFromXAI() error = %v", err)
	}

	if got := gjson.GetBytes(out, "id").String(); got != "91989464-273f-95df-8197-703b4fefd40e" {
		t.Fatalf("id = %q", got)
	}
	if got := gjson.GetBytes(out, "object").String(); got != "video" {
		t.Fatalf("object = %q, want video", got)
	}
	if got := gjson.GetBytes(out, "model").String(); got != defaultXAIVideosModel {
		t.Fatalf("model = %q, want %s", got, defaultXAIVideosModel)
	}
	if got := gjson.GetBytes(out, "status").String(); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if got := gjson.GetBytes(out, "progress").Int(); got != 100 {
		t.Fatalf("progress = %d, want 100", got)
	}
	if got := gjson.GetBytes(out, "seconds").String(); got != "4" {
		t.Fatalf("seconds = %q, want 4", got)
	}
	if gjson.GetBytes(out, "video").Exists() {
		t.Fatalf("video field must not be exposed in OpenAI retrieve response: %s", string(out))
	}
	if gjson.GetBytes(out, "usage").Exists() {
		t.Fatalf("usage field must not be exposed in OpenAI retrieve response: %s", string(out))
	}
}

func TestBuildVideosRetrieveAPIResponseFromXAINormalizesTopLevelError(t *testing.T) {
	payload := []byte(`{"code":"invalid-argument","error":"1080p video resolution is not available for your team."}`)

	out, err := buildVideosRetrieveAPIResponseFromXAI("video_123", payload, defaultOpenAIVideosModel)
	if err != nil {
		t.Fatalf("buildVideosRetrieveAPIResponseFromXAI() error = %v", err)
	}

	if got := gjson.GetBytes(out, "status").String(); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	if got := gjson.GetBytes(out, "progress").Int(); got != 0 {
		t.Fatalf("progress = %d, want 0", got)
	}
	if got := gjson.GetBytes(out, "error.code").String(); got != "invalid-argument" {
		t.Fatalf("error.code = %q, want invalid-argument", got)
	}
	if got := gjson.GetBytes(out, "error.message").String(); got != "1080p video resolution is not available for your team." {
		t.Fatalf("error.message = %q", got)
	}
}

func TestBuildVideosRetrieveAPIResponseFromXAINormalizesNestedError(t *testing.T) {
	payload := []byte(`{"status":"failed","error":{"message":"The request was rejected by the safety system.","type":"invalid_request_error","code":"content_policy_violation"}}`)

	out, err := buildVideosRetrieveAPIResponseFromXAI("video_123", payload, defaultOpenAIVideosModel)
	if err != nil {
		t.Fatalf("buildVideosRetrieveAPIResponseFromXAI() error = %v", err)
	}

	if got := gjson.GetBytes(out, "error.code").String(); got != "content_policy_violation" {
		t.Fatalf("error.code = %q, want content_policy_violation", got)
	}
	if got := gjson.GetBytes(out, "error.message").String(); got != "The request was rejected by the safety system." {
		t.Fatalf("error.message = %q", got)
	}
	if gjson.GetBytes(out, "error.type").Exists() {
		t.Fatalf("error.type must not be present: %s", string(out))
	}
}

func TestXAIVideoContentURLFromPayload(t *testing.T) {
	payload := []byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/video.mp4","duration":6}}`)

	got, err := xaiVideoContentURLFromPayload(payload)
	if err != nil {
		t.Fatalf("xaiVideoContentURLFromPayload() error = %v", err)
	}
	if got != "https://vidgen.x.ai/video.mp4" {
		t.Fatalf("url = %q, want https://vidgen.x.ai/video.mp4", got)
	}
}

func TestWriteVideoContentFromURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Disposition", `attachment; filename="video.mp4"`)
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	resp := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(resp)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/videos/video_123/content", nil)

	handler := &OpenAIAPIHandler{}
	if err := handler.writeVideoContentFromURL(ctx, upstream.URL+"/video.mp4"); err != nil {
		t.Fatalf("writeVideoContentFromURL() error = %v", err)
	}

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	if got := resp.Header().Get("Content-Disposition"); got != `attachment; filename="video.mp4"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := resp.Body.String(); got != "video-bytes" {
		t.Fatalf("body = %q, want video-bytes", got)
	}
}

func TestVideosCreateRejectsUnsupportedModel(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"not-a-video-model","prompt":"make a video"}`)

	resp := performVideosEndpointRequest(t, http.MethodPost, openAIVideosPath, "application/json", body, handler.VideosCreate)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "object").String(); got != "video" {
		t.Fatalf("object = %q, want video", got)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "model").String(); got != "not-a-video-model" {
		t.Fatalf("model = %q, want not-a-video-model", got)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "status").String(); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "progress").Int(); got != 0 {
		t.Fatalf("progress = %d, want 0", got)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "error.code").String(); got != "invalid_request_error" {
		t.Fatalf("error.code = %q, want invalid_request_error", got)
	}
	expectedMessage := "Model not-a-video-model is not supported on " + openAIVideosPath + ". Use " + defaultOpenAIVideosModel + "."
	if got := gjson.GetBytes(resp.Body.Bytes(), "error.message").String(); got != expectedMessage {
		t.Fatalf("error.message = %q, want %q", got, expectedMessage)
	}
	if gjson.GetBytes(resp.Body.Bytes(), "error.type").Exists() {
		t.Fatalf("error.type must not be present: %s", resp.Body.String())
	}
	if id := gjson.GetBytes(resp.Body.Bytes(), "id").String(); !strings.HasPrefix(id, "video_") {
		t.Fatalf("id = %q, want video_ prefix", id)
	}
}

func TestVideosCreateInvalidSizeReturnsFailedVideoResource(t *testing.T) {
	handler := &OpenAIAPIHandler{}
	body := strings.NewReader(`{"model":"sora-2","prompt":"make a video","size":"1080x1920"}`)

	resp := performVideosEndpointRequest(t, http.MethodPost, openAIVideosPath, "application/json", body, handler.VideosCreate)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "object").String(); got != "video" {
		t.Fatalf("object = %q, want video", got)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "model").String(); got != "sora-2" {
		t.Fatalf("model = %q, want sora-2", got)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "status").String(); got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "progress").Int(); got != 0 {
		t.Fatalf("progress = %d, want 0", got)
	}
	if got := gjson.GetBytes(resp.Body.Bytes(), "error.code").String(); got != "invalid_request_error" {
		t.Fatalf("error.code = %q, want invalid_request_error", got)
	}
	expectedMessage := "Invalid request: size must be one of 720x1280, 1280x720, 1024x1792, or 1792x1024"
	if got := gjson.GetBytes(resp.Body.Bytes(), "error.message").String(); got != expectedMessage {
		t.Fatalf("error.message = %q, want %q", got, expectedMessage)
	}
	if gjson.GetBytes(resp.Body.Bytes(), "error.type").Exists() {
		t.Fatalf("error.type must not be present: %s", resp.Body.String())
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
