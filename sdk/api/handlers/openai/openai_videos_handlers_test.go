package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
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

func performVideosRouteRequest(t *testing.T, method string, routePath string, requestPath string, contentType string, body io.Reader, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	switch method {
	case http.MethodGet:
		router.GET(routePath, handler)
	default:
		router.POST(routePath, handler)
	}

	req := httptest.NewRequest(method, requestPath, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

type videoAuthCaptureExecutor struct {
	mu         sync.Mutex
	requestID  string
	contentURL string
	authIDs    []string
	models     []string
}

func (e *videoAuthCaptureExecutor) Identifier() string { return "xai" }

func (e *videoAuthCaptureExecutor) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.authIDs = append(e.authIDs, authID)
	e.models = append(e.models, req.Model)
	e.mu.Unlock()

	requestID := strings.TrimSpace(gjson.GetBytes(req.Payload, "request_id").String())
	if requestID == "" {
		requestID = e.requestID
	}
	contentURL := strings.TrimSpace(e.contentURL)
	if contentURL == "" {
		contentURL = "https://vidgen.x.ai/video.mp4"
	}
	payload := []byte(`{"request_id":` + strconv.Quote(requestID) + `,"status":"completed","progress":100,"video":{"url":` + strconv.Quote(contentURL) + `,"duration":4}}`)
	return coreexecutor.Response{Payload: payload}, nil
}

func (e *videoAuthCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "ExecuteStream not implemented"}
}

func (e *videoAuthCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *videoAuthCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *videoAuthCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func (e *videoAuthCaptureExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.authIDs))
	copy(out, e.authIDs)
	return out
}

func (e *videoAuthCaptureExecutor) Models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.models))
	copy(out, e.models)
	return out
}

func resetVideoAuthBindingsForTest(t *testing.T) {
	t.Helper()
	previous := videoAuthBindings
	videoAuthBindings = newVideoAuthBindingStore()
	t.Cleanup(func() {
		videoAuthBindings = previous
	})
}

func newVideoAuthBindingTestHandler(t *testing.T, executor *videoAuthCaptureExecutor) *OpenAIAPIHandler {
	t.Helper()

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)

	authIDs := []string{executor.requestID + "-auth-a", executor.requestID + "-auth-b"}
	for _, authID := range authIDs {
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "xai",
			Status:   coreauth.StatusActive,
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("manager.Register(%s): %v", authID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: defaultXAIVideosModel}})
		manager.RefreshSchedulerEntry(authID)
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			registry.GetGlobalRegistry().UnregisterClient(authID)
		}
	})

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	return NewOpenAIAPIHandler(base)
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
	if meta.Model != defaultXAIVideosModel {
		t.Fatalf("response model = %q, want %s", meta.Model, defaultXAIVideosModel)
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
	if got := gjson.GetBytes(out, "video_url").String(); got != "https://vidgen.x.ai/xai-vidgen-bucket/xai-video-08609066-e7e9-43ba-bd8d-bd29cb6221d9.mp4" {
		t.Fatalf("video_url = %q", got)
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

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)
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

func TestWriteVideoContentFromURLUsesPinnedAuthProxy(t *testing.T) {
	resetVideoAuthBindingsForTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	authID := "video-content-auth"
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "xai",
		Status:   coreauth.StatusActive,
		ProxyURL: "direct",
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register() error = %v", errRegister)
	}

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}, manager)
	handler := NewOpenAIAPIHandler(base)
	videoAuthBindings.set("video_123", authID, time.Hour)

	gin.SetMode(gin.TestMode)
	resp := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(resp)
	ctx.Params = gin.Params{{Key: "video_id", Value: "video_123"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/videos/video_123/content", nil)

	if err := handler.writeVideoContentFromURL(ctx, upstream.URL+"/video.mp4"); err != nil {
		t.Fatalf("writeVideoContentFromURL() error = %v", err)
	}

	client := handler.videoContentHTTPClient(ctx)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected pinned auth direct proxy to bypass global proxy")
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
}

func TestWriteVideoContentFromURLFallsBackToGlobalProxy(t *testing.T) {
	resetVideoAuthBindingsForTest(t)

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}, nil)
	handler := NewOpenAIAPIHandler(base)

	gin.SetMode(gin.TestMode)
	resp := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(resp)
	ctx.Params = gin.Params{{Key: "video_id", Value: "video_456"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/videos/video_456/content", nil)

	client := handler.videoContentHTTPClient(ctx)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com/video.mp4", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest() error = %v", errRequest)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("transport.Proxy() error = %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestVideosContentUsesSelectedAuthProxyForDownload(t *testing.T) {
	resetVideoAuthBindingsForTest(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("video-bytes"))
	}))
	defer upstream.Close()

	var proxyMu sync.Mutex
	proxyHits := 0
	globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyMu.Lock()
		proxyHits++
		proxyMu.Unlock()
		http.Error(w, "unexpected proxy", http.StatusBadGateway)
	}))
	defer globalProxy.Close()

	videoID := "video-content-selected"
	authID := "video-content-selected-auth"
	executor := &videoAuthCaptureExecutor{
		requestID:  videoID,
		contentURL: upstream.URL + "/video.mp4",
	}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "xai",
		Status:   coreauth.StatusActive,
		ProxyURL: "direct",
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: defaultXAIVideosModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{ProxyURL: globalProxy.URL}, manager)
	handler := NewOpenAIAPIHandler(base)

	resp := performVideosRouteRequest(t, http.MethodGet, openAIVideosPath+"/:video_id/content", openAIVideosPath+"/"+videoID+"/content", "", nil, handler.VideosContent)
	if resp.Code != http.StatusOK {
		t.Fatalf("content status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := resp.Body.String(); got != "video-bytes" {
		t.Fatalf("content body = %q, want video-bytes", got)
	}
	authIDs := executor.AuthIDs()
	if len(authIDs) != 1 || authIDs[0] != authID {
		t.Fatalf("authIDs = %v, want [%s]", authIDs, authID)
	}
	if boundAuthID, ok := videoAuthBindings.get(videoID); !ok || boundAuthID != authID {
		t.Fatalf("bound auth = %q ok=%v, want %s", boundAuthID, ok, authID)
	}
	proxyMu.Lock()
	gotProxyHits := proxyHits
	proxyMu.Unlock()
	if gotProxyHits != 0 {
		t.Fatalf("global proxy hits = %d, want 0", gotProxyHits)
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
	if got := gjson.GetBytes(resp.Body.Bytes(), "model").String(); got != defaultXAIVideosModel {
		t.Fatalf("model = %q, want %s", got, defaultXAIVideosModel)
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

func TestVideosCreateBindsRetrieveToSelectedAuth(t *testing.T) {
	resetVideoAuthBindingsForTest(t)
	executor := &videoAuthCaptureExecutor{requestID: "video-openai-bound"}
	handler := newVideoAuthBindingTestHandler(t, executor)

	createResp := performVideosEndpointRequest(t, http.MethodPost, openAIVideosPath, "application/json", strings.NewReader(`{"model":"sora-2","prompt":"make a video"}`), handler.VideosCreate)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", createResp.Code, http.StatusOK, createResp.Body.String())
	}
	videoID := gjson.GetBytes(createResp.Body.Bytes(), "id").String()
	if videoID != executor.requestID {
		t.Fatalf("created video id = %q, want %q", videoID, executor.requestID)
	}
	if got := gjson.GetBytes(createResp.Body.Bytes(), "model").String(); got != defaultXAIVideosModel {
		t.Fatalf("created model = %q, want %s", got, defaultXAIVideosModel)
	}

	retrieveResp := performVideosRouteRequest(t, http.MethodGet, openAIVideosPath+"/:video_id", openAIVideosPath+"/"+videoID, "", nil, handler.VideosRetrieve)
	if retrieveResp.Code != http.StatusOK {
		t.Fatalf("retrieve status = %d, want %d: %s", retrieveResp.Code, http.StatusOK, retrieveResp.Body.String())
	}

	authIDs := executor.AuthIDs()
	if len(authIDs) != 2 {
		t.Fatalf("authIDs = %v, want two calls", authIDs)
	}
	if authIDs[1] != authIDs[0] {
		t.Fatalf("retrieve auth = %q, want create auth %q; sequence=%v", authIDs[1], authIDs[0], authIDs)
	}
}

func TestXAIVideosNativeCreateBindsRetrieveToSelectedAuth(t *testing.T) {
	resetVideoAuthBindingsForTest(t)
	executor := &videoAuthCaptureExecutor{requestID: "video-xai-bound"}
	handler := newVideoAuthBindingTestHandler(t, executor)

	createResp := performVideosEndpointRequest(t, http.MethodPost, xaiVideosGenerationsAPI, "application/json", strings.NewReader(`{"model":"grok-imagine-video","prompt":"make a video"}`), handler.XAIVideosGenerations)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", createResp.Code, http.StatusOK, createResp.Body.String())
	}
	videoID := gjson.GetBytes(createResp.Body.Bytes(), "request_id").String()
	if videoID != executor.requestID {
		t.Fatalf("created request_id = %q, want %q", videoID, executor.requestID)
	}

	retrieveResp := performVideosRouteRequest(t, http.MethodGet, videosPath+"/:request_id", videosPath+"/"+videoID, "", nil, handler.XAIVideosRetrieve)
	if retrieveResp.Code != http.StatusOK {
		t.Fatalf("retrieve status = %d, want %d: %s", retrieveResp.Code, http.StatusOK, retrieveResp.Body.String())
	}

	authIDs := executor.AuthIDs()
	if len(authIDs) != 2 {
		t.Fatalf("authIDs = %v, want two calls", authIDs)
	}
	if authIDs[1] != authIDs[0] {
		t.Fatalf("retrieve auth = %q, want create auth %q; sequence=%v", authIDs[1], authIDs[0], authIDs)
	}
}

func TestXAIVideosNativeRetrieveUsesBoundModel(t *testing.T) {
	resetVideoAuthBindingsForTest(t)
	executor := &videoAuthCaptureExecutor{requestID: "video-xai-preview-bound"}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)

	authModels := []struct {
		authID string
		model  string
	}{
		{authID: "video-xai-preview-default-auth", model: defaultXAIVideosModel},
		{authID: "video-xai-preview-auth", model: xaiVideos15PreviewModel},
	}
	for _, entry := range authModels {
		auth := &coreauth.Auth{
			ID:       entry.authID,
			Provider: "xai",
			Status:   coreauth.StatusActive,
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("manager.Register(%s): %v", entry.authID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(entry.authID, auth.Provider, []*registry.ModelInfo{{ID: entry.model}})
		manager.RefreshSchedulerEntry(entry.authID)
	}
	t.Cleanup(func() {
		for _, entry := range authModels {
			registry.GetGlobalRegistry().UnregisterClient(entry.authID)
		}
	})

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)

	createResp := performVideosEndpointRequest(t, http.MethodPost, xaiVideosGenerationsAPI, "application/json", strings.NewReader(`{"model":"grok-imagine-video-1.5-preview","prompt":"make a video"}`), handler.XAIVideosGenerations)
	if createResp.Code != http.StatusOK {
		t.Fatalf("create status = %d, want %d: %s", createResp.Code, http.StatusOK, createResp.Body.String())
	}
	videoID := gjson.GetBytes(createResp.Body.Bytes(), "request_id").String()
	if videoID != executor.requestID {
		t.Fatalf("created request_id = %q, want %q", videoID, executor.requestID)
	}

	retrieveResp := performVideosRouteRequest(t, http.MethodGet, videosPath+"/:request_id", videosPath+"/"+videoID, "", nil, handler.XAIVideosRetrieve)
	if retrieveResp.Code != http.StatusOK {
		t.Fatalf("retrieve status = %d, want %d: %s", retrieveResp.Code, http.StatusOK, retrieveResp.Body.String())
	}

	authIDs := executor.AuthIDs()
	if len(authIDs) != 2 {
		t.Fatalf("authIDs = %v, want two calls", authIDs)
	}
	if authIDs[0] != "video-xai-preview-auth" || authIDs[1] != authIDs[0] {
		t.Fatalf("authIDs = %v, want both calls to use video-xai-preview-auth", authIDs)
	}
	models := executor.Models()
	if len(models) != 2 {
		t.Fatalf("models = %v, want two calls", models)
	}
	if models[0] != xaiVideos15PreviewModel || models[1] != xaiVideos15PreviewModel {
		t.Fatalf("models = %v, want both calls to use %s", models, xaiVideos15PreviewModel)
	}
	binding, ok := videoAuthBindings.getBinding(videoID)
	if !ok {
		t.Fatal("video auth binding was not stored")
	}
	if binding.authID != "video-xai-preview-auth" || binding.model != xaiVideos15PreviewModel {
		t.Fatalf("binding = {authID:%q model:%q}, want {authID:%q model:%q}", binding.authID, binding.model, "video-xai-preview-auth", xaiVideos15PreviewModel)
	}
}

func TestVideoAuthBindingTTLUsesConfig(t *testing.T) {
	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{VideoResultAuthCacheTTL: "45m"}, nil)
	handler := NewOpenAIAPIHandler(base)
	if got := handler.videoAuthBindingTTL(); got != 45*time.Minute {
		t.Fatalf("videoAuthBindingTTL() = %v, want 45m", got)
	}

	base = apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{VideoResultAuthCacheTTL: "invalid"}, nil)
	handler = NewOpenAIAPIHandler(base)
	if got := handler.videoAuthBindingTTL(); got != defaultVideoAuthBindingTTL {
		t.Fatalf("invalid videoAuthBindingTTL() = %v, want %v", got, defaultVideoAuthBindingTTL)
	}
}

func TestVideoAuthBindingStoreExpiresEntries(t *testing.T) {
	store := newVideoAuthBindingStore()
	store.entries["video-expired"] = videoAuthBinding{
		authID:    "auth-expired",
		expiresAt: time.Now().Add(-time.Second),
	}

	if authID, ok := store.get("video-expired"); ok {
		t.Fatalf("expired binding returned authID=%q", authID)
	}
	if _, exists := store.entries["video-expired"]; exists {
		t.Fatal("expired binding was not removed")
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
