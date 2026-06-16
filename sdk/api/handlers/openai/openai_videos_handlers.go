package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	videosPath               = "/v1/videos"
	openAIVideosPath         = "/openai/v1/videos"
	xaiVideosGenerationsAPI  = "/v1/videos/generations"
	xaiVideosEditsAPI        = "/v1/videos/edits"
	xaiVideosExtensionsAPI   = "/v1/videos/extensions"
	defaultOpenAIVideosModel = "sora-2"
	defaultXAIVideosModel    = "grok-imagine-video"
	xaiVideos15PreviewModel  = "grok-imagine-video-1.5-preview"
	xaiVideosHandlerType     = "openai-video"
	defaultVideosSeconds     = "4"
	defaultVideosSize        = "720x1280"
	defaultVideosResolution  = "720p"
	maxXAIVideoReferences    = 7
)

const defaultVideoAuthBindingTTL = 3 * time.Hour

var videoAuthBindings = newVideoAuthBindingStore()

type xaiVideoCreateMetadata struct {
	Model         string
	UpstreamModel string
	Prompt        string
	Seconds       string
	Size          string
	CreatedAt     int64
}

type videoAuthBinding struct {
	authID    string
	expiresAt time.Time
}

type videoAuthBindingStore struct {
	mu      sync.RWMutex
	entries map[string]videoAuthBinding
}

func newVideoAuthBindingStore() *videoAuthBindingStore {
	return &videoAuthBindingStore{
		entries: make(map[string]videoAuthBinding),
	}
}

func (s *videoAuthBindingStore) set(videoID string, authID string, ttl time.Duration) {
	if s == nil {
		return
	}
	videoID = strings.TrimSpace(videoID)
	authID = strings.TrimSpace(authID)
	if videoID == "" || authID == "" {
		return
	}
	if ttl <= 0 {
		ttl = defaultVideoAuthBindingTTL
	}
	now := time.Now()
	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	s.entries[videoID] = videoAuthBinding{
		authID:    authID,
		expiresAt: now.Add(ttl),
	}
	s.mu.Unlock()
}

func (s *videoAuthBindingStore) get(videoID string) (string, bool) {
	if s == nil {
		return "", false
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return "", false
	}
	now := time.Now()
	s.mu.RLock()
	entry, ok := s.entries[videoID]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	if now.After(entry.expiresAt) {
		s.mu.Lock()
		if current, exists := s.entries[videoID]; exists && now.After(current.expiresAt) {
			delete(s.entries, videoID)
		}
		s.mu.Unlock()
		return "", false
	}
	return entry.authID, true
}

func (s *videoAuthBindingStore) cleanupExpiredLocked(now time.Time) {
	for videoID, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, videoID)
		}
	}
}

func videosModelBase(model string) string {
	_, baseModel := imagesModelParts(model)
	return strings.ToLower(strings.TrimSpace(baseModel))
}

func isXAIVideosModel(model string) bool {
	prefix, baseModel := imagesModelParts(model)
	baseModel = strings.ToLower(strings.TrimSpace(baseModel))
	if baseModel != defaultXAIVideosModel && baseModel != xaiVideos15PreviewModel {
		return false
	}

	prefix = strings.ToLower(strings.TrimSpace(prefix))
	return prefix == "" || prefix == "xai" || prefix == "x-ai" || prefix == "grok"
}

func isSoraVideosModel(model string) bool {
	_, baseModel := imagesModelParts(model)
	baseModel = strings.ToLower(strings.TrimSpace(baseModel))
	return baseModel == defaultOpenAIVideosModel || strings.HasPrefix(baseModel, defaultOpenAIVideosModel+"-")
}

func isSupportedVideosModel(model string) bool {
	return isXAIVideosModel(model) || isSoraVideosModel(model)
}

func rejectUnsupportedVideosModel(c *gin.Context, model string) bool {
	if isSupportedVideosModel(model) {
		return false
	}

	path := strings.TrimSpace(c.Request.URL.Path)
	if path == "" {
		path = openAIVideosPath
	}
	writeVideosFailedError(c, http.StatusBadRequest, model, "invalid_request_error", fmt.Sprintf("Model %s is not supported on %s. Use %s.", model, path, defaultOpenAIVideosModel))
	return true
}

func rejectUnsupportedNativeVideosModel(c *gin.Context, model string) bool {
	if isXAIVideosModel(model) {
		return false
	}

	c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Model %s is not supported on %s, %s, or %s. Use %s.", model, xaiVideosGenerationsAPI, xaiVideosEditsAPI, xaiVideosExtensionsAPI, defaultXAIVideosModel),
			Type:    "invalid_request_error",
		},
	})
	return true
}

func canonicalXAIVideosModel(model string) string {
	if isSoraVideosModel(model) {
		return defaultXAIVideosModel
	}
	switch videosModelBase(model) {
	case defaultXAIVideosModel:
		return defaultXAIVideosModel
	case xaiVideos15PreviewModel:
		return xaiVideos15PreviewModel
	}
	return defaultXAIVideosModel
}

func responseVideosModel(model string) string {
	return canonicalXAIVideosModel(model)
}

func readVideosCreateRequest(c *gin.Context) ([]byte, error) {
	contentType := strings.ToLower(strings.TrimSpace(c.ContentType()))
	switch contentType {
	case "multipart/form-data", "application/x-www-form-urlencoded":
		return videosCreateRequestFromForm(c)
	default:
		rawJSON, err := handlers.ReadRequestBody(c)
		if err != nil {
			return nil, err
		}
		if !json.Valid(rawJSON) {
			return nil, fmt.Errorf("body must be valid JSON")
		}
		return rawJSON, nil
	}
}

func readXAIVideosNativeRequest(c *gin.Context) ([]byte, error) {
	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		return nil, err
	}
	if !json.Valid(rawJSON) {
		return nil, fmt.Errorf("body must be valid JSON")
	}
	return rawJSON, nil
}

func videosCreateRequestFromForm(c *gin.Context) ([]byte, error) {
	rawJSON := []byte(`{}`)
	for _, field := range []string{"model", "prompt", "seconds", "size", "aspect_ratio", "resolution"} {
		if value := strings.TrimSpace(c.PostForm(field)); value != "" {
			rawJSON, _ = sjson.SetBytes(rawJSON, field, value)
		}
	}
	if value := strings.TrimSpace(firstPostForm(c, "input_reference[image_url]", "input_reference.image_url", "image_url")); value != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "input_reference.image_url", value)
	}
	if value := strings.TrimSpace(firstPostForm(c, "input_reference[file_id]", "input_reference.file_id", "file_id")); value != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "input_reference.file_id", value)
	}
	if refs := strings.TrimSpace(c.PostForm("reference_image_urls")); refs != "" {
		for _, ref := range strings.Split(refs, ",") {
			if ref = strings.TrimSpace(ref); ref != "" {
				rawJSON, _ = sjson.SetBytes(rawJSON, "reference_image_urls.-1", ref)
			}
		}
	}
	return rawJSON, nil
}

func firstPostForm(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := c.PostForm(key); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (h *OpenAIAPIHandler) videoAuthBindingTTL() time.Duration {
	if h != nil && h.BaseAPIHandler != nil && h.Cfg != nil {
		raw := strings.TrimSpace(h.Cfg.VideoResultAuthCacheTTL)
		if raw != "" {
			if ttl, err := time.ParseDuration(raw); err == nil && ttl > 0 {
				return ttl
			}
		}
	}
	return defaultVideoAuthBindingTTL
}

func videoIDFromPayload(payload []byte) string {
	videoID := strings.TrimSpace(gjson.GetBytes(payload, "request_id").String())
	if videoID == "" {
		videoID = strings.TrimSpace(gjson.GetBytes(payload, "id").String())
	}
	return videoID
}

func (h *OpenAIAPIHandler) bindVideoAuthIDFromPayload(payload []byte, authID string) {
	videoID := videoIDFromPayload(payload)
	if videoID == "" {
		return
	}
	videoAuthBindings.set(videoID, authID, h.videoAuthBindingTTL())
}

func (h *OpenAIAPIHandler) contextWithVideoAuthBinding(ctx context.Context, videoID string) context.Context {
	if authID, ok := videoAuthBindings.get(videoID); ok {
		return handlers.WithPinnedAuthID(ctx, authID)
	}
	return ctx
}

func buildXAIVideosCreateRequest(rawJSON []byte, model string) ([]byte, xaiVideoCreateMetadata, error) {
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	if prompt == "" {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("prompt is required")
	}

	seconds, duration, err := normalizeXAIVideosSeconds(gjson.GetBytes(rawJSON, "seconds").String())
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}

	size, aspectRatio, resolution, err := xaiVideosSizeOptions(gjson.GetBytes(rawJSON, "size").String())
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}
	if value := xaiVideosAspectRatio(gjson.GetBytes(rawJSON, "aspect_ratio").String(), ""); value != "" {
		aspectRatio = value
	}
	if value := xaiVideosResolution(gjson.GetBytes(rawJSON, "resolution").String(), ""); value != "" {
		resolution = value
	}

	imageURL, err := xaiVideosInputImageURL(rawJSON)
	if err != nil {
		return nil, xaiVideoCreateMetadata{}, err
	}
	referenceImages := collectXAIVideoReferenceImages(rawJSON)
	if len(referenceImages) > maxXAIVideoReferences {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("reference_images supports at most %d images on xAI", maxXAIVideoReferences)
	}
	if imageURL != "" && len(referenceImages) > 0 {
		return nil, xaiVideoCreateMetadata{}, fmt.Errorf("image and reference_images cannot be combined on xAI")
	}
	if len(referenceImages) > 0 && duration > 10 {
		duration = 10
		seconds = "10"
	}

	videoModel := canonicalXAIVideosModel(model)
	req := []byte(`{}`)
	req, _ = sjson.SetBytes(req, "model", videoModel)
	req, _ = sjson.SetBytes(req, "prompt", prompt)
	req, _ = sjson.SetRawBytes(req, "duration", []byte(strconv.FormatInt(duration, 10)))
	req, _ = sjson.SetBytes(req, "aspect_ratio", aspectRatio)
	req, _ = sjson.SetBytes(req, "resolution", resolution)
	if imageURL != "" {
		req, _ = sjson.SetBytes(req, "image.url", imageURL)
	}
	for _, image := range referenceImages {
		req, _ = sjson.SetBytes(req, "reference_images.-1.url", image)
	}

	meta := xaiVideoCreateMetadata{
		Model:         responseVideosModel(model),
		UpstreamModel: videoModel,
		Prompt:        prompt,
		Seconds:       seconds,
		Size:          size,
		CreatedAt:     time.Now().Unix(),
	}
	return req, meta, nil
}

func normalizeXAIVideosSeconds(raw string) (string, int64, error) {
	seconds := strings.TrimSpace(raw)
	if seconds == "" {
		seconds = defaultVideosSeconds
	}
	duration, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("seconds must be an integer")
	}
	if duration < 1 {
		duration = 1
	}
	if duration > 15 {
		duration = 15
	}
	return strconv.FormatInt(duration, 10), duration, nil
}

func xaiVideosSizeOptions(raw string) (size string, aspectRatio string, resolution string, err error) {
	size = strings.TrimSpace(raw)
	if size == "" {
		size = defaultVideosSize
	}
	switch size {
	case "720x1280", "1024x1792":
		return size, "9:16", defaultVideosResolution, nil
	case "1280x720", "1792x1024":
		return size, "16:9", defaultVideosResolution, nil
	default:
		return "", "", "", fmt.Errorf("size must be one of 720x1280, 1280x720, 1024x1792, or 1792x1024")
	}
}

func xaiVideosAspectRatio(raw string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1:1", "square":
		return "1:1"
	case "16:9", "landscape":
		return "16:9"
	case "9:16", "portrait":
		return "9:16"
	case "4:3":
		return "4:3"
	case "3:4":
		return "3:4"
	case "3:2":
		return "3:2"
	case "2:3":
		return "2:3"
	default:
		return fallback
	}
}

func xaiVideosResolution(raw string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "480p":
		return "480p"
	case "720p":
		return "720p"
	default:
		return fallback
	}
}

func xaiVideosInputImageURL(rawJSON []byte) (string, error) {
	inputRef := gjson.GetBytes(rawJSON, "input_reference")
	if inputRef.Exists() {
		imageURL := strings.TrimSpace(inputRef.Get("image_url").String())
		fileID := strings.TrimSpace(inputRef.Get("file_id").String())
		if imageURL != "" && fileID != "" {
			return "", fmt.Errorf("input_reference must provide exactly one of image_url or file_id")
		}
		if fileID != "" {
			return "", fmt.Errorf("input_reference.file_id is not supported for xAI video generation; use input_reference.image_url")
		}
		if imageURL != "" {
			return imageURL, nil
		}
	}

	image := gjson.GetBytes(rawJSON, "image")
	if image.Exists() {
		if image.Type == gjson.String {
			return strings.TrimSpace(image.String()), nil
		}
		if value := strings.TrimSpace(image.Get("url").String()); value != "" {
			return value, nil
		}
		if value := strings.TrimSpace(image.Get("image_url.url").String()); value != "" {
			return value, nil
		}
	}

	return strings.TrimSpace(gjson.GetBytes(rawJSON, "image_url").String()), nil
}

func collectXAIVideoReferenceImages(rawJSON []byte) []string {
	out := make([]string, 0)
	appendRef := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	collectArray := func(result gjson.Result) {
		if !result.IsArray() {
			return
		}
		result.ForEach(func(_, item gjson.Result) bool {
			if item.Type == gjson.String {
				appendRef(item.String())
				return true
			}
			if value := item.Get("url").String(); value != "" {
				appendRef(value)
				return true
			}
			if value := item.Get("image_url.url").String(); value != "" {
				appendRef(value)
			}
			return true
		})
	}
	collectArray(gjson.GetBytes(rawJSON, "reference_images"))
	collectArray(gjson.GetBytes(rawJSON, "reference_image_urls"))
	return out
}

func buildVideosCreateAPIResponseFromXAI(payload []byte, meta xaiVideoCreateMetadata) ([]byte, error) {
	requestID := strings.TrimSpace(gjson.GetBytes(payload, "request_id").String())
	if requestID == "" {
		requestID = strings.TrimSpace(gjson.GetBytes(payload, "id").String())
	}
	if requestID == "" {
		return nil, fmt.Errorf("xAI video response did not include request_id")
	}

	out := []byte(`{"object":"video","progress":0,"status":"queued"}`)
	out, _ = sjson.SetBytes(out, "id", requestID)
	out, _ = sjson.SetBytes(out, "model", meta.Model)
	out, _ = sjson.SetBytes(out, "prompt", meta.Prompt)
	out, _ = sjson.SetBytes(out, "seconds", meta.Seconds)
	out, _ = sjson.SetBytes(out, "size", meta.Size)
	out, _ = sjson.SetBytes(out, "created_at", meta.CreatedAt)
	if status := openAIVideoStatus(gjson.GetBytes(payload, "status").String()); status != "" {
		out, _ = sjson.SetBytes(out, "status", status)
	}
	if progress := gjson.GetBytes(payload, "progress"); progress.Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte(progress.Raw))
	}
	return out, nil
}

func buildVideosFailedAPIResponse(model string, code string, message string) []byte {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultXAIVideosModel
	}
	code = strings.TrimSpace(code)
	if code == "" {
		code = "invalid_request_error"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Video generation failed"
	}

	out := []byte(`{"object":"video","status":"failed","progress":0}`)
	out, _ = sjson.SetBytes(out, "id", "video_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "error.code", code)
	out, _ = sjson.SetBytes(out, "error.message", message)
	return out
}

func writeVideosFailedError(c *gin.Context, status int, model string, code string, message string) {
	if status <= 0 {
		status = http.StatusBadRequest
	}
	c.Data(status, "application/json", buildVideosFailedAPIResponse(model, code, message))
}

func buildVideosRetrieveAPIResponseFromXAI(videoID string, payload []byte, fallbackModel string) ([]byte, error) {
	out := []byte(`{"object":"video"}`)
	out, _ = sjson.SetBytes(out, "id", videoID)
	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if model == "" {
		model = responseVideosModel(fallbackModel)
	}
	out, _ = sjson.SetBytes(out, "model", model)

	for _, field := range []string{"created_at", "completed_at", "expires_at", "prompt", "remixed_from_video_id", "size"} {
		if value := gjson.GetBytes(payload, field); value.Exists() {
			out, _ = sjson.SetRawBytes(out, field, []byte(value.Raw))
		}
	}

	if status := openAIVideoStatus(gjson.GetBytes(payload, "status").String()); status != "" {
		out, _ = sjson.SetBytes(out, "status", status)
	}
	if progress := gjson.GetBytes(payload, "progress"); progress.Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte(progress.Raw))
	}
	if seconds := gjson.GetBytes(payload, "seconds"); seconds.Exists() {
		out, _ = sjson.SetRawBytes(out, "seconds", []byte(seconds.Raw))
	} else if duration := gjson.GetBytes(payload, "video.duration"); duration.Exists() {
		out, _ = sjson.SetBytes(out, "seconds", duration.String())
	}
	if videoURL := strings.TrimSpace(gjson.GetBytes(payload, "video.url").String()); videoURL != "" {
		out, _ = sjson.SetBytes(out, "video_url", videoURL)
	}
	out = setOpenAIVideoErrorFromXAI(out, payload)
	return out, nil
}

func setOpenAIVideoErrorFromXAI(out []byte, payload []byte) []byte {
	if errPayload := gjson.GetBytes(payload, "error"); errPayload.Exists() {
		out = markOpenAIVideoFailed(out)
		if errPayload.Type == gjson.JSON && json.Valid([]byte(errPayload.Raw)) {
			message := strings.TrimSpace(errPayload.Get("message").String())
			if message != "" {
				code := strings.TrimSpace(gjson.GetBytes(payload, "code").String())
				if code == "" {
					code = strings.TrimSpace(errPayload.Get("code").String())
				}
				if code == "" {
					code = "video_generation_failed"
				}
				out, _ = sjson.SetBytes(out, "error.code", code)
				out, _ = sjson.SetBytes(out, "error.message", message)
			}
			return out
		}
		message := strings.TrimSpace(errPayload.String())
		if message != "" {
			code := strings.TrimSpace(gjson.GetBytes(payload, "code").String())
			if code == "" {
				code = "video_generation_failed"
			}
			out, _ = sjson.SetBytes(out, "error.code", code)
			out, _ = sjson.SetBytes(out, "error.message", message)
		}
		return out
	}

	code := strings.TrimSpace(gjson.GetBytes(payload, "code").String())
	if code != "" {
		out = markOpenAIVideoFailed(out)
		out, _ = sjson.SetBytes(out, "error.code", code)
		out, _ = sjson.SetBytes(out, "error.message", code)
	}
	return out
}

func markOpenAIVideoFailed(out []byte) []byte {
	if !gjson.GetBytes(out, "status").Exists() {
		out, _ = sjson.SetBytes(out, "status", "failed")
	}
	if !gjson.GetBytes(out, "progress").Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte("0"))
	}
	return out
}

func xaiVideoContentURLFromPayload(payload []byte) (string, error) {
	rawURL := strings.TrimSpace(gjson.GetBytes(payload, "video.url").String())
	if rawURL == "" {
		return "", fmt.Errorf("xAI video response did not include video.url")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("xAI video response included invalid video.url")
	}
	return rawURL, nil
}

func openAIVideoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending":
		return "queued"
	case "in_progress", "processing", "running":
		return "in_progress"
	case "completed", "done", "succeeded", "success":
		return "completed"
	case "failed", "error", "expired", "cancelled", "canceled":
		return "failed"
	default:
		return ""
	}
}

func (h *OpenAIAPIHandler) VideosCreate(c *gin.Context) {
	rawJSON, err := readVideosCreateRequest(c)
	if err != nil {
		writeVideosFailedError(c, http.StatusBadRequest, defaultXAIVideosModel, "invalid_request_error", fmt.Sprintf("Invalid request: %v", err))
		return
	}

	videoModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if videoModel == "" {
		videoModel = defaultXAIVideosModel
	}
	if rejectUnsupportedVideosModel(c, videoModel) {
		return
	}

	xaiReq, meta, err := buildXAIVideosCreateRequest(rawJSON, videoModel)
	if err != nil {
		writeVideosFailedError(c, http.StatusBadRequest, responseVideosModel(videoModel), "invalid_request_error", fmt.Sprintf("Invalid request: %v", err))
		return
	}

	h.collectXAIVideosCreate(c, xaiReq, meta)
}

func (h *OpenAIAPIHandler) XAIVideosGenerations(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) XAIVideosEdits(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) XAIVideosExtensions(c *gin.Context) {
	h.handleXAIVideosNativePost(c)
}

func (h *OpenAIAPIHandler) handleXAIVideosNativePost(c *gin.Context) {
	rawJSON, err := readXAIVideosNativeRequest(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	videoModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if videoModel == "" {
		videoModel = defaultXAIVideosModel
	}
	if rejectUnsupportedNativeVideosModel(c, videoModel) {
		return
	}

	h.collectXAIVideosNative(c, rawJSON, videoModel, true)
}

func (h *OpenAIAPIHandler) XAIVideosRetrieve(c *gin.Context) {
	requestID := strings.TrimSpace(c.Param("request_id"))
	if requestID == "" {
		requestID = strings.TrimSpace(c.Param("video_id"))
	}
	if requestID == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: request_id is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	payload := []byte(`{}`)
	payload, _ = sjson.SetBytes(payload, "request_id", requestID)
	h.collectXAIVideosNative(c, payload, defaultXAIVideosModel, false)
}

func (h *OpenAIAPIHandler) VideosRetrieve(c *gin.Context) {
	videoID := strings.TrimSpace(c.Param("video_id"))
	if videoID == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: video_id is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	payload := []byte(`{}`)
	payload, _ = sjson.SetBytes(payload, "request_id", videoID)

	c.Header("Content-Type", "application/json")
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	cliCtx = h.contextWithVideoAuthBinding(cliCtx, videoID)
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, defaultXAIVideosModel, payload, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	out, err := buildVideosRetrieveAPIResponseFromXAI(videoID, resp, defaultOpenAIVideosModel)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(out)
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) VideosContent(c *gin.Context) {
	videoID := strings.TrimSpace(c.Param("video_id"))
	if videoID == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: video_id is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	variant := strings.TrimSpace(c.Query("variant"))
	if variant == "" {
		variant = "video"
	}
	if variant != "video" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: variant %q is not available for xAI video downloads", variant),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	payload := []byte(`{}`)
	payload, _ = sjson.SetBytes(payload, "request_id", videoID)

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	cliCtx = h.contextWithVideoAuthBinding(cliCtx, videoID)
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, _, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, defaultXAIVideosModel, payload, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	contentURL, err := xaiVideoContentURLFromPayload(resp)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	if errDownload := h.writeVideoContentFromURL(c, contentURL); errDownload != nil {
		cliCancel(errDownload)
		return
	}
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) writeVideoContentFromURL(c *gin.Context, contentURL string) error {
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, contentURL, nil)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		return err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("video content body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		errDownloadStatus := fmt.Errorf("video content download failed: %s", strings.TrimSpace(string(body)))
		if strings.TrimSpace(string(body)) == "" {
			errDownloadStatus = fmt.Errorf("video content download failed: %s", resp.Status)
		}
		errMsg := &interfaces.ErrorMessage{StatusCode: resp.StatusCode, Error: errDownloadStatus}
		h.WriteErrorResponse(c, errMsg)
		return errDownloadStatus
	}

	copyVideoContentHeaders(c.Writer.Header(), resp.Header)
	if c.Writer.Header().Get("Content-Type") == "" {
		c.Writer.Header().Set("Content-Type", "application/octet-stream")
	}
	c.Status(resp.StatusCode)
	_, err = io.Copy(c.Writer, resp.Body)
	return err
}

func copyVideoContentHeaders(dst http.Header, src http.Header) {
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Disposition", "Cache-Control", "ETag", "Last-Modified"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

func (h *OpenAIAPIHandler) collectXAIVideosNative(c *gin.Context, rawJSON []byte, model string, bindCreatedVideoAuth bool) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	if bindCreatedVideoAuth {
		cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
			selectedAuthID = authID
		})
	} else {
		cliCtx = h.contextWithVideoAuthBinding(cliCtx, videoIDFromPayload(rawJSON))
	}
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, model, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	if bindCreatedVideoAuth {
		h.bindVideoAuthIDFromPayload(resp, selectedAuthID)
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) collectXAIVideosCreate(c *gin.Context, xaiReq []byte, meta xaiVideoCreateMetadata) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	selectedAuthID := ""
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	upstreamModel := strings.TrimSpace(meta.UpstreamModel)
	if upstreamModel == "" {
		upstreamModel = meta.Model
	}
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, upstreamModel, xaiReq, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	out, err := buildVideosCreateAPIResponseFromXAI(resp, meta)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	h.bindVideoAuthIDFromPayload(out, selectedAuthID)
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(out)
	cliCancel(nil)
}
