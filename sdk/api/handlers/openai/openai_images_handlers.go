package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	defaultImagesMainModel      = "gpt-5.4-mini"
	defaultImagesToolModel      = "gpt-image-2"
	defaultXAIImagesModel       = "grok-imagine-image"
	xaiImagesQualityModel       = "grok-imagine-image-quality"
	xaiImagesHandlerType        = "openai-image"
	xaiImagesDefaultAspectRatio = "1:1"
	xaiImagesDefaultResolution  = "1k"
	imagesGenerationsPath       = "/v1/images/generations"
	imagesEditsPath             = "/v1/images/edits"
)

type imageCallResult struct {
	Result        string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	Background    string
	Quality       string
}

type sseFrameAccumulator struct {
	pending []byte
}

type xaiImageResult struct {
	B64JSON       string
	URL           string
	RevisedPrompt string
	MimeType      string
}

func (a *sseFrameAccumulator) AddChunk(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}

	if responsesSSENeedsLineBreak(a.pending, chunk) {
		a.pending = append(a.pending, '\n')
	}
	a.pending = append(a.pending, chunk...)

	var frames [][]byte
	for {
		frameLen := responsesSSEFrameLen(a.pending)
		if frameLen == 0 {
			break
		}
		frames = append(frames, a.pending[:frameLen])
		copy(a.pending, a.pending[frameLen:])
		a.pending = a.pending[:len(a.pending)-frameLen]
	}

	if len(bytes.TrimSpace(a.pending)) == 0 {
		a.pending = a.pending[:0]
		return frames
	}
	if len(a.pending) == 0 || !responsesSSECanEmitWithoutDelimiter(a.pending) {
		return frames
	}
	frames = append(frames, a.pending)
	a.pending = a.pending[:0]
	return frames
}

func (a *sseFrameAccumulator) Flush() [][]byte {
	if len(a.pending) == 0 {
		return nil
	}

	var frames [][]byte
	for {
		frameLen := responsesSSEFrameLen(a.pending)
		if frameLen == 0 {
			break
		}
		frames = append(frames, a.pending[:frameLen])
		copy(a.pending, a.pending[frameLen:])
		a.pending = a.pending[:len(a.pending)-frameLen]
	}

	if len(bytes.TrimSpace(a.pending)) == 0 {
		a.pending = nil
		return frames
	}
	if responsesSSECanEmitWithoutDelimiter(a.pending) {
		frames = append(frames, a.pending)
	}
	a.pending = nil
	return frames
}

func imagesModelParts(model string) (prefix string, baseModel string) {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return strings.TrimSpace(model[:idx]), strings.TrimSpace(model[idx+1:])
	}
	return "", model
}

func imagesModelBase(model string) string {
	_, baseModel := imagesModelParts(model)
	return strings.ToLower(strings.TrimSpace(baseModel))
}

func isXAIImagesModel(model string) bool {
	prefix, baseModel := imagesModelParts(model)
	baseModel = strings.ToLower(strings.TrimSpace(baseModel))
	if baseModel != defaultXAIImagesModel && baseModel != xaiImagesQualityModel {
		return false
	}

	prefix = strings.ToLower(strings.TrimSpace(prefix))
	return prefix == "" || prefix == "xai" || prefix == "x-ai" || prefix == "grok"
}

func isSupportedImagesModel(model string) bool {
	baseModel := imagesModelBase(model)
	if baseModel == defaultImagesToolModel {
		return true
	}
	return isXAIImagesModel(model)
}

func rejectUnsupportedImagesModel(c *gin.Context, model string) bool {
	if isSupportedImagesModel(model) {
		return false
	}

	c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Model %s is not supported on %s or %s. Use %s, %s, or %s.", model, imagesGenerationsPath, imagesEditsPath, defaultImagesToolModel, defaultXAIImagesModel, xaiImagesQualityModel),
			Type:    "invalid_request_error",
		},
	})
	return true
}

func normalizeImagesResponseFormat(responseFormat string) string {
	if strings.EqualFold(strings.TrimSpace(responseFormat), "url") {
		return "url"
	}
	return "b64_json"
}

func canonicalXAIImagesModel(model string) string {
	baseModel := imagesModelBase(model)
	if baseModel == xaiImagesQualityModel {
		return xaiImagesQualityModel
	}
	return defaultXAIImagesModel
}

func xaiImagesAspectRatio(raw string, fallback string) string {
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

func xaiImagesAspectRatioFromSize(size string, fallback string) string {
	size = strings.ToLower(strings.TrimSpace(size))
	switch size {
	case "1024x1024", "2048x2048", "1:1":
		return "1:1"
	case "1792x1024", "16:9":
		return "16:9"
	case "1024x1792", "9:16":
		return "9:16"
	case "1536x1024", "3:2":
		return "3:2"
	case "1024x1536", "2:3":
		return "2:3"
	default:
		return fallback
	}
}

func xaiImagesResolution(raw string, size string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1k", "2k":
		return strings.ToLower(strings.TrimSpace(raw))
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(size)), "2048") {
		return "2k"
	}
	return fallback
}

func xaiImagesRef(imageURL string) []byte {
	ref := []byte(`{"type":"image_url","url":""}`)
	ref, _ = sjson.SetBytes(ref, "url", strings.TrimSpace(imageURL))
	return ref
}

func buildXAIImagesBaseRequest(model string, prompt string, responseFormat string, aspectRatio string, resolution string, n int64) []byte {
	req := []byte(`{}`)
	req, _ = sjson.SetBytes(req, "model", canonicalXAIImagesModel(model))
	req, _ = sjson.SetBytes(req, "prompt", strings.TrimSpace(prompt))
	req, _ = sjson.SetBytes(req, "response_format", normalizeImagesResponseFormat(responseFormat))
	if aspectRatio != "" {
		req, _ = sjson.SetBytes(req, "aspect_ratio", aspectRatio)
	}
	if resolution != "" {
		req, _ = sjson.SetBytes(req, "resolution", resolution)
	}
	if n > 0 {
		req, _ = sjson.SetBytes(req, "n", n)
	}
	return req
}

func buildXAIImagesGenerationsRequest(rawJSON []byte, model string, responseFormat string) []byte {
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	size := strings.TrimSpace(gjson.GetBytes(rawJSON, "size").String())
	aspectRatio := xaiImagesAspectRatio(gjson.GetBytes(rawJSON, "aspect_ratio").String(), "")
	aspectRatio = xaiImagesAspectRatioFromSize(size, aspectRatio)
	if aspectRatio == "" {
		aspectRatio = xaiImagesDefaultAspectRatio
	}
	resolution := xaiImagesResolution(gjson.GetBytes(rawJSON, "resolution").String(), size, xaiImagesDefaultResolution)
	n := int64(0)
	if v := gjson.GetBytes(rawJSON, "n"); v.Exists() && v.Type == gjson.Number {
		n = v.Int()
	}
	return buildXAIImagesBaseRequest(model, prompt, responseFormat, aspectRatio, resolution, n)
}

func buildXAIImagesEditRequest(model string, prompt string, images []string, responseFormat string, aspectRatio string, resolution string, n int64) []byte {
	req := buildXAIImagesBaseRequest(model, prompt, responseFormat, aspectRatio, resolution, n)
	trimmedImages := make([]string, 0, len(images))
	for _, img := range images {
		if strings.TrimSpace(img) != "" {
			trimmedImages = append(trimmedImages, strings.TrimSpace(img))
		}
	}
	if len(trimmedImages) == 1 {
		req, _ = sjson.SetRawBytes(req, "image", xaiImagesRef(trimmedImages[0]))
		return req
	}
	for _, img := range trimmedImages {
		req, _ = sjson.SetRawBytes(req, "images.-1", xaiImagesRef(img))
	}
	return req
}

func collectXAIImagesFromJSON(rawJSON []byte) []string {
	var images []string
	appendImage := func(url string) {
		url = strings.TrimSpace(url)
		if url != "" {
			images = append(images, url)
		}
	}

	if image := gjson.GetBytes(rawJSON, "image"); image.Exists() {
		if image.Type == gjson.String {
			appendImage(image.String())
		} else if image.Type == gjson.JSON {
			appendImage(image.Get("image_url.url").String())
			if imageURL := image.Get("image_url"); imageURL.Type == gjson.String {
				appendImage(imageURL.String())
			}
			appendImage(image.Get("url").String())
		}
	}
	if imagesResult := gjson.GetBytes(rawJSON, "images"); imagesResult.IsArray() {
		for _, img := range imagesResult.Array() {
			if img.Type == gjson.String {
				appendImage(img.String())
				continue
			}
			appendImage(img.Get("image_url.url").String())
			if imageURL := img.Get("image_url"); imageURL.Type == gjson.String {
				appendImage(imageURL.String())
			}
			appendImage(img.Get("url").String())
		}
	}
	return images
}

func xaiImagesEditOptionsFromJSON(rawJSON []byte) (aspectRatio string, resolution string, n int64) {
	size := strings.TrimSpace(gjson.GetBytes(rawJSON, "size").String())
	aspectRatio = xaiImagesAspectRatio(gjson.GetBytes(rawJSON, "aspect_ratio").String(), "")
	aspectRatio = xaiImagesAspectRatioFromSize(size, aspectRatio)
	resolution = xaiImagesResolution(gjson.GetBytes(rawJSON, "resolution").String(), size, "")
	if v := gjson.GetBytes(rawJSON, "n"); v.Exists() && v.Type == gjson.Number {
		n = v.Int()
	}
	return aspectRatio, resolution, n
}

func mimeTypeFromOutputFormat(outputFormat string) string {
	if outputFormat == "" {
		return "image/png"
	}
	if strings.Contains(outputFormat, "/") {
		return outputFormat
	}
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func multipartFileToDataURL(fileHeader *multipart.FileHeader) (string, error) {
	if fileHeader == nil {
		return "", fmt.Errorf("upload file is nil")
	}
	f, err := fileHeader.Open()
	if err != nil {
		return "", fmt.Errorf("open upload file failed: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("openai images: close upload file error: %v", errClose)
		}
	}()

	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read upload file failed: %w", err)
	}

	mediaType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}

	b64 := base64.StdEncoding.EncodeToString(data)
	return "data:" + mediaType + ";base64," + b64, nil
}

func parseIntField(raw string, fallback int64) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return v
}

func parseBoolField(raw string, fallback bool) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func (h *OpenAIAPIHandler) ImagesGenerations(c *gin.Context) {
	if h != nil && h.BaseAPIHandler != nil && h.BaseAPIHandler.Cfg != nil && h.BaseAPIHandler.Cfg.DisableImageGeneration == internalconfig.DisableImageGenerationAll {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if !json.Valid(rawJSON) {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: body must be valid JSON",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	imageModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	if rejectUnsupportedImagesModel(c, imageModel) {
		return
	}

	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	if prompt == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: prompt is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	responseFormat := strings.TrimSpace(gjson.GetBytes(rawJSON, "response_format").String())
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	stream := gjson.GetBytes(rawJSON, "stream").Bool()

	if isXAIImagesModel(imageModel) {
		xaiReq := buildXAIImagesGenerationsRequest(rawJSON, imageModel, responseFormat)
		h.handleXAIImages(c, xaiReq, responseFormat, "image_generation", stream)
		return
	}
	if shouldPassthroughOpenAICompatImages(imageModel) {
		h.handleOpenAICompatImages(c, rawJSON, responseFormat, "image_generation", stream)
		return
	}

	tool := []byte(`{"type":"image_generation","action":"generate"}`)
	tool, _ = sjson.SetBytes(tool, "model", imageModel)

	if v := strings.TrimSpace(gjson.GetBytes(rawJSON, "size").String()); v != "" {
		tool, _ = sjson.SetBytes(tool, "size", v)
	}
	if v := strings.TrimSpace(gjson.GetBytes(rawJSON, "quality").String()); v != "" {
		tool, _ = sjson.SetBytes(tool, "quality", v)
	}
	if v := strings.TrimSpace(gjson.GetBytes(rawJSON, "background").String()); v != "" {
		tool, _ = sjson.SetBytes(tool, "background", v)
	}
	if v := strings.TrimSpace(gjson.GetBytes(rawJSON, "output_format").String()); v != "" {
		tool, _ = sjson.SetBytes(tool, "output_format", v)
	}
	if v := gjson.GetBytes(rawJSON, "output_compression"); v.Exists() {
		if v.Type == gjson.Number {
			tool, _ = sjson.SetBytes(tool, "output_compression", v.Int())
		}
	}
	if v := gjson.GetBytes(rawJSON, "partial_images"); v.Exists() {
		if v.Type == gjson.Number {
			tool, _ = sjson.SetBytes(tool, "partial_images", v.Int())
		}
	}
	if v := strings.TrimSpace(gjson.GetBytes(rawJSON, "moderation").String()); v != "" {
		tool, _ = sjson.SetBytes(tool, "moderation", v)
	}

	responsesReq := buildImagesResponsesRequest(prompt, nil, tool)
	if stream {
		h.streamImagesFromResponses(c, responsesReq, responseFormat, "image_generation")
		return
	}
	h.collectImagesFromResponses(c, responsesReq, responseFormat)
}

func (h *OpenAIAPIHandler) ImagesEdits(c *gin.Context) {
	if h != nil && h.BaseAPIHandler != nil && h.BaseAPIHandler.Cfg != nil && h.BaseAPIHandler.Cfg.DisableImageGeneration == internalconfig.DisableImageGenerationAll {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
	if strings.HasPrefix(contentType, "application/json") {
		h.imagesEditsFromJSON(c)
		return
	}
	if strings.HasPrefix(contentType, "multipart/form-data") || contentType == "" {
		h.imagesEditsFromMultipart(c)
		return
	}

	c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Invalid request: unsupported Content-Type %q", contentType),
			Type:    "invalid_request_error",
		},
	})
}

func (h *OpenAIAPIHandler) imagesEditsFromMultipart(c *gin.Context) {
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	imageModel := strings.TrimSpace(c.PostForm("model"))
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	if rejectUnsupportedImagesModel(c, imageModel) {
		return
	}

	prompt := strings.TrimSpace(c.PostForm("prompt"))
	if prompt == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: prompt is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	var imageFiles []*multipart.FileHeader
	if files := form.File["image[]"]; len(files) > 0 {
		imageFiles = files
	} else if files := form.File["image"]; len(files) > 0 {
		imageFiles = files
	}
	if len(imageFiles) == 0 {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: image is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	images := make([]string, 0, len(imageFiles))
	for _, fh := range imageFiles {
		dataURL, err := multipartFileToDataURL(fh)
		if err != nil {
			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: fmt.Sprintf("Invalid request: %v", err),
					Type:    "invalid_request_error",
				},
			})
			return
		}
		images = append(images, dataURL)
	}

	responseFormat := strings.TrimSpace(c.PostForm("response_format"))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	stream := parseBoolField(c.PostForm("stream"), false)

	if isXAIImagesModel(imageModel) {
		aspectRatio := xaiImagesAspectRatio(c.PostForm("aspect_ratio"), "")
		aspectRatio = xaiImagesAspectRatioFromSize(c.PostForm("size"), aspectRatio)
		resolution := xaiImagesResolution(c.PostForm("resolution"), c.PostForm("size"), "")
		n := parseIntField(c.PostForm("n"), 0)
		xaiReq := buildXAIImagesEditRequest(imageModel, prompt, images, responseFormat, aspectRatio, resolution, n)
		h.handleXAIImages(c, xaiReq, responseFormat, "image_edit", stream)
		return
	}

	var maskDataURL *string
	if maskFiles := form.File["mask"]; len(maskFiles) > 0 && maskFiles[0] != nil {
		dataURL, err := multipartFileToDataURL(maskFiles[0])
		if err != nil {
			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: fmt.Sprintf("Invalid request: %v", err),
					Type:    "invalid_request_error",
				},
			})
			return
		}
		maskDataURL = &dataURL
	}

	tool := []byte(`{"type":"image_generation","action":"edit"}`)
	tool, _ = sjson.SetBytes(tool, "model", imageModel)

	if v := strings.TrimSpace(c.PostForm("size")); v != "" {
		tool, _ = sjson.SetBytes(tool, "size", v)
	}
	if v := strings.TrimSpace(c.PostForm("quality")); v != "" {
		tool, _ = sjson.SetBytes(tool, "quality", v)
	}
	if v := strings.TrimSpace(c.PostForm("background")); v != "" {
		tool, _ = sjson.SetBytes(tool, "background", v)
	}
	if v := strings.TrimSpace(c.PostForm("output_format")); v != "" {
		tool, _ = sjson.SetBytes(tool, "output_format", v)
	}
	if v := strings.TrimSpace(c.PostForm("input_fidelity")); v != "" {
		tool, _ = sjson.SetBytes(tool, "input_fidelity", v)
	}
	if v := strings.TrimSpace(c.PostForm("moderation")); v != "" {
		tool, _ = sjson.SetBytes(tool, "moderation", v)
	}

	if v := strings.TrimSpace(c.PostForm("output_compression")); v != "" {
		tool, _ = sjson.SetBytes(tool, "output_compression", parseIntField(v, 0))
	}
	if v := strings.TrimSpace(c.PostForm("partial_images")); v != "" {
		tool, _ = sjson.SetBytes(tool, "partial_images", parseIntField(v, 0))
	}

	if maskDataURL != nil && strings.TrimSpace(*maskDataURL) != "" {
		tool, _ = sjson.SetBytes(tool, "input_image_mask.image_url", strings.TrimSpace(*maskDataURL))
	}

	responsesReq := buildImagesResponsesRequest(prompt, images, tool)
	if stream {
		h.streamImagesFromResponses(c, responsesReq, responseFormat, "image_edit")
		return
	}
	h.collectImagesFromResponses(c, responsesReq, responseFormat)
}

func (h *OpenAIAPIHandler) imagesEditsFromJSON(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if !json.Valid(rawJSON) {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: body must be valid JSON",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	imageModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	if rejectUnsupportedImagesModel(c, imageModel) {
		return
	}

	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	if prompt == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: prompt is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	responseFormat := strings.TrimSpace(gjson.GetBytes(rawJSON, "response_format").String())
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	stream := gjson.GetBytes(rawJSON, "stream").Bool()

	if isXAIImagesModel(imageModel) {
		images := collectXAIImagesFromJSON(rawJSON)
		if len(images) == 0 {
			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: "Invalid request: image is required",
					Type:    "invalid_request_error",
				},
			})
			return
		}
		aspectRatio, resolution, n := xaiImagesEditOptionsFromJSON(rawJSON)
		xaiReq := buildXAIImagesEditRequest(imageModel, prompt, images, responseFormat, aspectRatio, resolution, n)
		h.handleXAIImages(c, xaiReq, responseFormat, "image_edit", stream)
		return
	}

	var images []string
	imagesResult := gjson.GetBytes(rawJSON, "images")
	if imagesResult.IsArray() {
		for _, img := range imagesResult.Array() {
			url := strings.TrimSpace(img.Get("image_url").String())
			if url == "" {
				continue
			}
			images = append(images, url)
		}
	}
	if len(images) == 0 {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: images[].image_url is required (file_id is not supported)",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	var maskDataURL *string
	if mask := gjson.GetBytes(rawJSON, "mask.image_url"); mask.Exists() {
		url := strings.TrimSpace(mask.String())
		if url != "" {
			maskDataURL = &url
		}
	} else if mask := gjson.GetBytes(rawJSON, "mask.file_id"); mask.Exists() {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: mask.file_id is not supported (use mask.image_url instead)",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	tool := []byte(`{"type":"image_generation","action":"edit"}`)
	tool, _ = sjson.SetBytes(tool, "model", imageModel)

	for _, field := range []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation"} {
		if v := strings.TrimSpace(gjson.GetBytes(rawJSON, field).String()); v != "" {
			tool, _ = sjson.SetBytes(tool, field, v)
		}
	}

	for _, field := range []string{"output_compression", "partial_images"} {
		if v := gjson.GetBytes(rawJSON, field); v.Exists() && v.Type == gjson.Number {
			tool, _ = sjson.SetBytes(tool, field, v.Int())
		}
	}

	if maskDataURL != nil && strings.TrimSpace(*maskDataURL) != "" {
		tool, _ = sjson.SetBytes(tool, "input_image_mask.image_url", strings.TrimSpace(*maskDataURL))
	}

	responsesReq := buildImagesResponsesRequest(prompt, images, tool)
	if stream {
		h.streamImagesFromResponses(c, responsesReq, responseFormat, "image_edit")
		return
	}
	h.collectImagesFromResponses(c, responsesReq, responseFormat)
}

func buildImagesResponsesRequest(prompt string, images []string, toolJSON []byte) []byte {
	req := []byte(`{"instructions":"","stream":true,"reasoning":{"effort":"medium","summary":"auto"},"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"model":"","store":false,"tool_choice":{"type":"image_generation"}}`)
	mainModel := defaultImagesMainModel
	if len(toolJSON) > 0 && json.Valid(toolJSON) {
		toolModel := strings.TrimSpace(gjson.GetBytes(toolJSON, "model").String())
		if idx := strings.LastIndex(toolModel, "/"); idx > 0 && idx < len(toolModel)-1 {
			prefix := strings.TrimSpace(toolModel[:idx])
			if prefix != "" {
				mainModel = prefix + "/" + defaultImagesMainModel
			}
		}
	}
	req, _ = sjson.SetBytes(req, "model", mainModel)

	input := []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`)
	input, _ = sjson.SetBytes(input, "0.content.0.text", prompt)
	contentIndex := 1
	for _, img := range images {
		if strings.TrimSpace(img) == "" {
			continue
		}
		part := []byte(`{"type":"input_image","image_url":""}`)
		part, _ = sjson.SetBytes(part, "image_url", img)
		path := fmt.Sprintf("0.content.%d", contentIndex)
		input, _ = sjson.SetRawBytes(input, path, part)
		contentIndex++
	}
	req, _ = sjson.SetRawBytes(req, "input", input)

	req, _ = sjson.SetRawBytes(req, "tools", []byte(`[]`))
	if len(toolJSON) > 0 && json.Valid(toolJSON) {
		req, _ = sjson.SetRawBytes(req, "tools.-1", toolJSON)
	}
	return req
}

func extractXAIImagesResponse(payload []byte) (results []xaiImageResult, createdAt int64, usageRaw []byte, err error) {
	if !json.Valid(payload) {
		return nil, 0, nil, fmt.Errorf("upstream returned invalid image response JSON")
	}

	createdAt = gjson.GetBytes(payload, "created").Int()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}

	data := gjson.GetBytes(payload, "data")
	if data.IsArray() {
		for _, item := range data.Array() {
			result := xaiImageResult{
				B64JSON:       strings.TrimSpace(item.Get("b64_json").String()),
				URL:           strings.TrimSpace(item.Get("url").String()),
				RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
				MimeType:      strings.TrimSpace(item.Get("mime_type").String()),
			}
			if result.MimeType == "" {
				result.MimeType = mimeTypeFromOutputFormat(strings.TrimSpace(item.Get("output_format").String()))
			}
			if result.MimeType == "" {
				result.MimeType = "image/png"
			}
			if result.B64JSON == "" && result.URL == "" {
				continue
			}
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		return nil, 0, nil, fmt.Errorf("upstream did not return image output")
	}

	if usage := gjson.GetBytes(payload, "usage"); usage.Exists() && usage.IsObject() {
		usageRaw = []byte(usage.Raw)
	}

	return results, createdAt, usageRaw, nil
}

func buildImagesAPIResponseFromXAI(payload []byte, responseFormat string) ([]byte, error) {
	results, createdAt, usageRaw, err := extractXAIImagesResponse(payload)
	if err != nil {
		return nil, err
	}

	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", createdAt)
	responseFormat = normalizeImagesResponseFormat(responseFormat)

	for _, img := range results {
		item := []byte(`{}`)
		if responseFormat == "url" {
			if img.URL != "" {
				item, _ = sjson.SetBytes(item, "url", img.URL)
			} else {
				item, _ = sjson.SetBytes(item, "url", "data:"+mimeTypeFromOutputFormat(img.MimeType)+";base64,"+img.B64JSON)
			}
		} else if img.B64JSON != "" {
			item, _ = sjson.SetBytes(item, "b64_json", img.B64JSON)
		} else {
			item, _ = sjson.SetBytes(item, "url", img.URL)
		}
		if img.RevisedPrompt != "" {
			item, _ = sjson.SetBytes(item, "revised_prompt", img.RevisedPrompt)
		}
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}

	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		out, _ = sjson.SetRawBytes(out, "usage", usageRaw)
	}

	return out, nil
}

func (h *OpenAIAPIHandler) handleXAIImages(c *gin.Context, xaiReq []byte, responseFormat string, streamPrefix string, stream bool) {
	if stream {
		h.streamXAIImages(c, xaiReq, responseFormat, streamPrefix)
		return
	}
	h.collectXAIImages(c, xaiReq, responseFormat)
}

func shouldPassthroughOpenAICompatImages(model string) bool {
	if isXAIImagesModel(model) || imagesModelBase(model) != defaultImagesToolModel {
		return false
	}
	for _, candidate := range []string{strings.TrimSpace(model), imagesModelBase(model)} {
		if candidate == "" {
			continue
		}
		for _, provider := range util.GetProviderName(candidate) {
			provider = strings.TrimSpace(provider)
			if provider != "" && !strings.EqualFold(provider, "codex") {
				return true
			}
		}
	}
	return false
}

func (h *OpenAIAPIHandler) handleOpenAICompatImages(c *gin.Context, rawJSON []byte, responseFormat string, streamPrefix string, stream bool) {
	if stream {
		h.streamOpenAICompatImages(c, rawJSON, responseFormat, streamPrefix)
		return
	}
	h.collectOpenAICompatImages(c, rawJSON)
}

func (h *OpenAIAPIHandler) collectOpenAICompatImages(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	imageModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiImagesHandlerType, imageModel, rawJSON, "")
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

func (h *OpenAIAPIHandler) streamOpenAICompatImages(c *gin.Context, rawJSON []byte, responseFormat string, streamPrefix string) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	imageModel := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiImagesHandlerType, imageModel, rawJSON, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

	eventName := streamPrefix + ".completed"
	responseFormat = normalizeImagesResponseFormat(responseFormat)
	for _, item := range gjson.GetBytes(resp, "data").Array() {
		data := []byte(`{"type":""}`)
		data, _ = sjson.SetBytes(data, "type", eventName)
		if responseFormat == "url" {
			if url := strings.TrimSpace(item.Get("url").String()); url != "" {
				data, _ = sjson.SetBytes(data, "url", url)
			} else {
				data, _ = sjson.SetBytes(data, "url", "data:image/png;base64,"+strings.TrimSpace(item.Get("b64_json").String()))
			}
		} else if b64 := strings.TrimSpace(item.Get("b64_json").String()); b64 != "" {
			data, _ = sjson.SetBytes(data, "b64_json", b64)
		} else if url := strings.TrimSpace(item.Get("url").String()); url != "" {
			data, _ = sjson.SetBytes(data, "url", url)
		}
		if revised := strings.TrimSpace(item.Get("revised_prompt").String()); revised != "" {
			data, _ = sjson.SetBytes(data, "revised_prompt", revised)
		}
		if strings.TrimSpace(eventName) != "" {
			_, _ = fmt.Fprintf(c.Writer, "event: %s\n", eventName)
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(data))
		flusher.Flush()
	}
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) collectXAIImages(c *gin.Context, xaiReq []byte, responseFormat string) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	model := strings.TrimSpace(gjson.GetBytes(xaiReq, "model").String())
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiImagesHandlerType, model, xaiReq, "")
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

	out, err := buildImagesAPIResponseFromXAI(resp, responseFormat)
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

func (h *OpenAIAPIHandler) streamXAIImages(c *gin.Context, xaiReq []byte, responseFormat string, streamPrefix string) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	model := strings.TrimSpace(gjson.GetBytes(xaiReq, "model").String())
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, xaiImagesHandlerType, model, xaiReq, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		if errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
		return
	}

	results, _, usageRaw, err := extractXAIImagesResponse(resp)
	if err != nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
		h.WriteErrorResponse(c, errMsg)
		cliCancel(err)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

	eventName := streamPrefix + ".completed"
	responseFormat = normalizeImagesResponseFormat(responseFormat)
	for _, img := range results {
		data := []byte(`{"type":""}`)
		data, _ = sjson.SetBytes(data, "type", eventName)
		if responseFormat == "url" {
			if img.URL != "" {
				data, _ = sjson.SetBytes(data, "url", img.URL)
			} else {
				data, _ = sjson.SetBytes(data, "url", "data:"+mimeTypeFromOutputFormat(img.MimeType)+";base64,"+img.B64JSON)
			}
		} else if img.B64JSON != "" {
			data, _ = sjson.SetBytes(data, "b64_json", img.B64JSON)
		} else {
			data, _ = sjson.SetBytes(data, "url", img.URL)
		}
		if len(usageRaw) > 0 && json.Valid(usageRaw) {
			data, _ = sjson.SetRawBytes(data, "usage", usageRaw)
		}
		if strings.TrimSpace(eventName) != "" {
			_, _ = fmt.Fprintf(c.Writer, "event: %s\n", eventName)
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(data))
		flusher.Flush()
	}
	cliCancel(nil)
}

func (h *OpenAIAPIHandler) collectImagesFromResponses(c *gin.Context, responsesReq []byte, responseFormat string) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	cliCtx = handlers.WithDisallowFreeAuth(cliCtx)
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	mainModel := strings.TrimSpace(gjson.GetBytes(responsesReq, "model").String())
	if mainModel == "" {
		mainModel = defaultImagesMainModel
	}
	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, "openai-response", mainModel, responsesReq, "")

	out, errMsg := collectImagesFromResponsesStream(cliCtx, dataChan, errChan, responseFormat)
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
	_, _ = c.Writer.Write(out)
	cliCancel()
}

func collectImagesFromResponsesStream(ctx context.Context, data <-chan []byte, errs <-chan *interfaces.ErrorMessage, responseFormat string) ([]byte, *interfaces.ErrorMessage) {
	acc := &sseFrameAccumulator{}

	processFrame := func(frame []byte) ([]byte, bool, *interfaces.ErrorMessage) {
		for _, line := range bytes.Split(frame, []byte("\n")) {
			trimmed := bytes.TrimSpace(bytes.TrimRight(line, "\r"))
			if len(trimmed) == 0 {
				continue
			}
			if !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(trimmed[len("data:"):])
			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			if !json.Valid(payload) {
				return nil, false, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("invalid SSE data JSON")}
			}

			if gjson.GetBytes(payload, "type").String() != "response.completed" {
				continue
			}

			results, createdAt, usageRaw, firstMeta, err := extractImagesFromResponsesCompleted(payload)
			if err != nil {
				return nil, false, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
			}
			if len(results) == 0 {
				return nil, false, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("upstream did not return image output")}
			}
			out, err := buildImagesAPIResponse(results, createdAt, usageRaw, firstMeta, responseFormat)
			if err != nil {
				return nil, false, &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: err}
			}
			return out, true, nil
		}
		return nil, false, nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, &interfaces.ErrorMessage{StatusCode: http.StatusRequestTimeout, Error: ctx.Err()}
		case errMsg, ok := <-errs:
			if ok && errMsg != nil {
				return nil, errMsg
			}
			errs = nil
		case chunk, ok := <-data:
			if !ok {
				for _, frame := range acc.Flush() {
					if out, done, errMsg := processFrame(frame); errMsg != nil {
						return nil, errMsg
					} else if done {
						return out, nil
					}
				}
				return nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("stream disconnected before completion")}
			}
			for _, frame := range acc.AddChunk(chunk) {
				if out, done, errMsg := processFrame(frame); errMsg != nil {
					return nil, errMsg
				} else if done {
					return out, nil
				}
			}
		}
	}
}

func extractImagesFromResponsesCompleted(payload []byte) (results []imageCallResult, createdAt int64, usageRaw []byte, firstMeta imageCallResult, err error) {
	if gjson.GetBytes(payload, "type").String() != "response.completed" {
		return nil, 0, nil, imageCallResult{}, fmt.Errorf("unexpected event type")
	}

	createdAt = gjson.GetBytes(payload, "response.created_at").Int()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}

	output := gjson.GetBytes(payload, "response.output")
	if output.IsArray() {
		for _, item := range output.Array() {
			if item.Get("type").String() != "image_generation_call" {
				continue
			}
			res := strings.TrimSpace(item.Get("result").String())
			if res == "" {
				continue
			}
			entry := imageCallResult{
				Result:        res,
				RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
				OutputFormat:  strings.TrimSpace(item.Get("output_format").String()),
				Size:          strings.TrimSpace(item.Get("size").String()),
				Background:    strings.TrimSpace(item.Get("background").String()),
				Quality:       strings.TrimSpace(item.Get("quality").String()),
			}
			if len(results) == 0 {
				firstMeta = entry
			}
			results = append(results, entry)
		}
	}

	if usage := gjson.GetBytes(payload, "response.tool_usage.image_gen"); usage.Exists() && usage.IsObject() {
		usageRaw = []byte(usage.Raw)
	}

	return results, createdAt, usageRaw, firstMeta, nil
}

func buildImagesAPIResponse(results []imageCallResult, createdAt int64, usageRaw []byte, firstMeta imageCallResult, responseFormat string) ([]byte, error) {
	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", createdAt)

	responseFormat = strings.ToLower(strings.TrimSpace(responseFormat))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}

	for _, img := range results {
		item := []byte(`{}`)
		if responseFormat == "url" {
			mt := mimeTypeFromOutputFormat(img.OutputFormat)
			item, _ = sjson.SetBytes(item, "url", "data:"+mt+";base64,"+img.Result)
		} else {
			item, _ = sjson.SetBytes(item, "b64_json", img.Result)
		}
		if img.RevisedPrompt != "" {
			item, _ = sjson.SetBytes(item, "revised_prompt", img.RevisedPrompt)
		}
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}

	if firstMeta.Background != "" {
		out, _ = sjson.SetBytes(out, "background", firstMeta.Background)
	}
	if firstMeta.OutputFormat != "" {
		out, _ = sjson.SetBytes(out, "output_format", firstMeta.OutputFormat)
	}
	if firstMeta.Quality != "" {
		out, _ = sjson.SetBytes(out, "quality", firstMeta.Quality)
	}
	if firstMeta.Size != "" {
		out, _ = sjson.SetBytes(out, "size", firstMeta.Size)
	}

	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		out, _ = sjson.SetRawBytes(out, "usage", usageRaw)
	}

	return out, nil
}

func (h *OpenAIAPIHandler) streamImagesFromResponses(c *gin.Context, responsesReq []byte, responseFormat string, streamPrefix string) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	cliCtx = handlers.WithDisallowFreeAuth(cliCtx)
	mainModel := strings.TrimSpace(gjson.GetBytes(responsesReq, "model").String())
	if mainModel == "" {
		mainModel = defaultImagesMainModel
	}
	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, "openai-response", mainModel, responsesReq, "")

	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}

	writeEvent := func(eventName string, dataJSON []byte) {
		if strings.TrimSpace(eventName) != "" {
			_, _ = fmt.Fprintf(c.Writer, "event: %s\n", eventName)
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(dataJSON))
		flusher.Flush()
	}

	// Peek for first chunk/error so we can still return a JSON error body.
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				setSSEHeaders()
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cliCancel(nil)
				return
			}

			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

			h.forwardImagesStream(cliCtx, c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, chunk, responseFormat, streamPrefix, writeEvent)
			return
		}
	}
}

func (h *OpenAIAPIHandler) forwardImagesStream(ctx context.Context, c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, firstChunk []byte, responseFormat string, streamPrefix string, writeEvent func(string, []byte)) {
	acc := &sseFrameAccumulator{}

	responseFormat = strings.ToLower(strings.TrimSpace(responseFormat))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}

	emitError := func(errMsg *interfaces.ErrorMessage) {
		if errMsg == nil {
			return
		}
		status := http.StatusInternalServerError
		if errMsg.StatusCode > 0 {
			status = errMsg.StatusCode
		}
		errText := http.StatusText(status)
		if errMsg.Error != nil && strings.TrimSpace(errMsg.Error.Error()) != "" {
			errText = errMsg.Error.Error()
		}
		body := handlers.BuildErrorResponseBody(status, errText)
		writeEvent("error", body)
	}

	processFrame := func(frame []byte) (done bool) {
		for _, line := range bytes.Split(frame, []byte("\n")) {
			trimmed := bytes.TrimSpace(bytes.TrimRight(line, "\r"))
			if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(trimmed[len("data:"):])
			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
				continue
			}

			switch gjson.GetBytes(payload, "type").String() {
			case "response.image_generation_call.partial_image":
				b64 := strings.TrimSpace(gjson.GetBytes(payload, "partial_image_b64").String())
				if b64 == "" {
					continue
				}
				outputFormat := strings.TrimSpace(gjson.GetBytes(payload, "output_format").String())
				index := gjson.GetBytes(payload, "partial_image_index").Int()
				eventName := streamPrefix + ".partial_image"
				data := []byte(`{"type":"","partial_image_index":0}`)
				data, _ = sjson.SetBytes(data, "type", eventName)
				data, _ = sjson.SetBytes(data, "partial_image_index", index)
				if responseFormat == "url" {
					mt := mimeTypeFromOutputFormat(outputFormat)
					data, _ = sjson.SetBytes(data, "url", "data:"+mt+";base64,"+b64)
				} else {
					data, _ = sjson.SetBytes(data, "b64_json", b64)
				}
				writeEvent(eventName, data)
			case "response.completed":
				results, _, usageRaw, _, err := extractImagesFromResponsesCompleted(payload)
				if err != nil {
					emitError(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
					return true
				}
				if len(results) == 0 {
					emitError(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("upstream did not return image output")})
					return true
				}
				eventName := streamPrefix + ".completed"
				for _, img := range results {
					data := []byte(`{"type":""}`)
					data, _ = sjson.SetBytes(data, "type", eventName)
					if responseFormat == "url" {
						mt := mimeTypeFromOutputFormat(img.OutputFormat)
						data, _ = sjson.SetBytes(data, "url", "data:"+mt+";base64,"+img.Result)
					} else {
						data, _ = sjson.SetBytes(data, "b64_json", img.Result)
					}
					if len(usageRaw) > 0 && json.Valid(usageRaw) {
						data, _ = sjson.SetRawBytes(data, "usage", usageRaw)
					}
					writeEvent(eventName, data)
				}
				return true
			}
		}
		return false
	}

	for _, frame := range acc.AddChunk(firstChunk) {
		if processFrame(frame) {
			cancel(nil)
			return
		}
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errs:
			if ok && errMsg != nil {
				emitError(errMsg)
				cancel(errMsg.Error)
				return
			}
			errs = nil
		case chunk, ok := <-data:
			if !ok {
				for _, frame := range acc.Flush() {
					if processFrame(frame) {
						cancel(nil)
						return
					}
				}
				cancel(nil)
				return
			}
			for _, frame := range acc.AddChunk(chunk) {
				if processFrame(frame) {
					cancel(nil)
					return
				}
			}
		}
	}
}
