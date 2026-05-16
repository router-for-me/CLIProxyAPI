package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	videosPath              = "/v1/videos"
	xaiVideosGenerationsAPI = "/v1/videos/generations"
	xaiVideosEditsAPI       = "/v1/videos/edits"
	xaiVideosExtensionsAPI  = "/v1/videos/extensions"
	defaultXAIVideosModel   = "grok-imagine-video"
	xaiVideosHandlerType    = "openai-video"
	defaultVideosSeconds    = "4"
	defaultVideosSize       = "720x1280"
	defaultVideosResolution = "720p"
	maxXAIVideoReferences   = 7
)

type xaiVideoCreateMetadata struct {
	Model     string
	Prompt    string
	Seconds   string
	Size      string
	CreatedAt int64
}

func videosModelBase(model string) string {
	_, baseModel := imagesModelParts(model)
	return strings.ToLower(strings.TrimSpace(baseModel))
}

func isXAIVideosModel(model string) bool {
	prefix, baseModel := imagesModelParts(model)
	baseModel = strings.ToLower(strings.TrimSpace(baseModel))
	if baseModel != defaultXAIVideosModel {
		return false
	}

	prefix = strings.ToLower(strings.TrimSpace(prefix))
	return prefix == "" || prefix == "xai" || prefix == "x-ai" || prefix == "grok"
}

func isSupportedVideosModel(model string) bool {
	return isXAIVideosModel(model)
}

func rejectUnsupportedVideosModel(c *gin.Context, model string) bool {
	if isSupportedVideosModel(model) {
		return false
	}

	c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Model %s is not supported on %s. Use %s.", model, videosPath, defaultXAIVideosModel),
			Type:    "invalid_request_error",
		},
	})
	return true
}

func rejectUnsupportedNativeVideosModel(c *gin.Context, model string) bool {
	if isSupportedVideosModel(model) {
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
	if videosModelBase(model) == defaultXAIVideosModel {
		return defaultXAIVideosModel
	}
	return defaultXAIVideosModel
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

	req := []byte(`{}`)
	req, _ = sjson.SetBytes(req, "model", canonicalXAIVideosModel(model))
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
		Model:     defaultXAIVideosModel,
		Prompt:    prompt,
		Seconds:   seconds,
		Size:      size,
		CreatedAt: time.Now().Unix(),
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

func buildVideosRetrieveAPIResponseFromXAI(videoID string, payload []byte, fallbackModel string) ([]byte, error) {
	out := []byte(`{"object":"video"}`)
	out, _ = sjson.SetBytes(out, "id", videoID)

	model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
	if model == "" {
		model = fallbackModel
	}
	out, _ = sjson.SetBytes(out, "model", model)

	if status := openAIVideoStatus(gjson.GetBytes(payload, "status").String()); status != "" {
		out, _ = sjson.SetBytes(out, "status", status)
	}
	if progress := gjson.GetBytes(payload, "progress"); progress.Exists() {
		out, _ = sjson.SetRawBytes(out, "progress", []byte(progress.Raw))
	}
	if duration := gjson.GetBytes(payload, "video.duration"); duration.Exists() {
		out, _ = sjson.SetBytes(out, "seconds", duration.String())
	}
	if video := gjson.GetBytes(payload, "video"); video.Exists() && json.Valid([]byte(video.Raw)) {
		out, _ = sjson.SetRawBytes(out, "video", []byte(video.Raw))
	}
	if usage := gjson.GetBytes(payload, "usage"); usage.Exists() && json.Valid([]byte(usage.Raw)) {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usage.Raw))
	}
	if errPayload := gjson.GetBytes(payload, "error"); errPayload.Exists() && json.Valid([]byte(errPayload.Raw)) {
		out, _ = sjson.SetRawBytes(out, "error", []byte(errPayload.Raw))
	}
	return out, nil
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
	if rejectUnsupportedVideosModel(c, videoModel) {
		return
	}

	xaiReq, meta, err := buildXAIVideosCreateRequest(rawJSON, videoModel)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
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

	h.collectXAIVideosNative(c, rawJSON, videoModel)
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
	h.collectXAIVideosNative(c, payload, defaultXAIVideosModel)
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

	out, err := buildVideosRetrieveAPIResponseFromXAI(videoID, resp, defaultXAIVideosModel)
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

func (h *OpenAIAPIHandler) collectXAIVideosNative(c *gin.Context, rawJSON []byte, model string) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
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

	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) collectXAIVideosCreate(c *gin.Context, xaiReq []byte, meta xaiVideoCreateMetadata) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiVideosHandlerType, meta.Model, xaiReq, "")
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

	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(out)
	cliCancel(nil)
}
