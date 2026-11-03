package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/MoeForge/moego/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	log "github.com/sirupsen/logrus"
)

const (
	defaultImagesMainModel      = "gpt-5.4-mini"
	gptImage15Model             = "gpt-image-1.5"
	defaultImagesToolModel      = "gpt-image-2"
	gptImage25FlareModel        = "gpt-image-2.5-flare"
	gptImage25SunburstModel     = "gpt-image-2.5-sunburst"
	gptImage25Model             = "gpt-image-2.5"
	defaultXAIImagesModel       = "grok-imagine-image"
	xaiImagesQualityModel       = "grok-imagine-image-quality"
	xaiImages20Model            = "grok-imagine-image-2.0"
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
	pending         []byte
	syntheticBreaks []int
}

type xaiImageResult struct {
	B64JSON       string
	URL           string
	RevisedPrompt string
	MimeType      string
}

type imagesStreamExecutionResult struct {
	Data            <-chan []byte
	UpstreamHeaders http.Header
	Errs            <-chan *interfaces.ErrorMessage
}

func setImagesSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
}

func (h *OpenAIAPIHandler) newImagesStreamKeepAliveTicker() (func(), <-chan time.Time) {
	if h == nil || h.BaseAPIHandler == nil {
		return func() {}, nil
	}
	keepAliveC, stop := handlers.NewStreamingKeepAliveTicker(handlers.StreamingKeepAliveInterval(h.CurrentConfig()))
	return stop, keepAliveC
}

func (h *OpenAIAPIHandler) newImagesPreFirstTokenKeepAliveTicker() (func(), <-chan time.Time) {
	if h == nil || h.BaseAPIHandler == nil {
		return func() {}, nil
	}
	keepAliveC, stop := handlers.NewPreFirstTokenKeepAliveTicker(h.CurrentConfig())
	return stop, keepAliveC
}

func writeImagesStreamKeepAlive(c *gin.Context, flusher http.Flusher) {
	_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
	flusher.Flush()
}

func writeImagesStreamErrorEvent(c *gin.Context, errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	original := errMsg
	errMsg = sanitizeResponsesStreamErrorMessage(errMsg)
	if errMsg == nil {
		return nil
	}
	if original != nil {
		*original = *errMsg
		errMsg = original
	}
	status := errMsg.StatusCode
	errText := responsesStreamErrorText(errMsg, status)
	body := handlers.BuildErrorResponseBody(status, errText)
	_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", string(body))
	return errMsg
}

func (h *OpenAIAPIHandler) waitImagesStreamExecution(c *gin.Context, flusher http.Flusher, cliCancel handlers.APIHandlerCancelFunc, execute func() imagesStreamExecutionResult) (imagesStreamExecutionResult, bool, bool) {
	stopKeepAlive, keepAliveC := h.newImagesPreFirstTokenKeepAliveTicker()
	defer stopKeepAlive()
	if keepAliveC == nil {
		return execute(), false, false
	}

	resultChan := make(chan imagesStreamExecutionResult, 1)
	go func() {
		resultChan <- execute()
	}()

	streamStarted := false
	for {
		select {
		case <-c.Request.Context().Done():
			if cliCancel != nil {
				cliCancel(c.Request.Context().Err())
			}
			<-resultChan
			return imagesStreamExecutionResult{}, streamStarted, true
		case result := <-resultChan:
			return result, streamStarted, false
		case <-keepAliveC:
			setImagesSSEHeaders(c)
			writeImagesStreamKeepAlive(c, flusher)
			streamStarted = true
		}
	}
}

func (a *sseFrameAccumulator) AddChunk(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}

	var frames [][]byte
	if responsesSSEStartsNewDataFrame(a.pending, chunk) {
		frames = append(frames, bytes.Clone(responsesSSERecoverJSONFrame(a.pending, a.syntheticBreaks)))
		a.pending = a.pending[:0]
		a.syntheticBreaks = a.syntheticBreaks[:0]
	}
	a.pending = responsesSSEAppendChunk(a.pending, &a.syntheticBreaks, chunk)

	for {
		frameLen := responsesSSEFrameLen(a.pending)
		if frameLen == 0 {
			break
		}
		frames = append(frames, bytes.Clone(responsesSSERecoverJSONFrame(a.pending[:frameLen], a.syntheticBreaks)))
		copy(a.pending, a.pending[frameLen:])
		a.pending = a.pending[:len(a.pending)-frameLen]
		a.syntheticBreaks = responsesSSEConsumeBreaks(a.syntheticBreaks, frameLen)
	}

	if len(bytes.TrimSpace(a.pending)) == 0 {
		a.pending = a.pending[:0]
		a.syntheticBreaks = a.syntheticBreaks[:0]
		return frames
	}
	if len(a.pending) == 0 || !responsesSSECanEmitWithoutDelimiter(a.pending) {
		return frames
	}
	frames = append(frames, bytes.Clone(responsesSSERecoverJSONFrame(a.pending, a.syntheticBreaks)))
	a.pending = a.pending[:0]
	a.syntheticBreaks = a.syntheticBreaks[:0]
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
		frames = append(frames, bytes.Clone(responsesSSERecoverJSONFrame(a.pending[:frameLen], a.syntheticBreaks)))
		copy(a.pending, a.pending[frameLen:])
		a.pending = a.pending[:len(a.pending)-frameLen]
		a.syntheticBreaks = responsesSSEConsumeBreaks(a.syntheticBreaks, frameLen)
	}

	if len(bytes.TrimSpace(a.pending)) == 0 {
		a.pending = nil
		return frames
	}
	frame := responsesSSERecoverJSONFrame(a.pending, a.syntheticBreaks)
	if responsesSSECanFlushWithoutDelimiter(frame) {
		frames = append(frames, bytes.Clone(frame))
	}
	a.pending = nil
	a.syntheticBreaks = nil
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

func isXAIImagesBaseModel(baseModel string) bool {
	switch strings.ToLower(strings.TrimSpace(baseModel)) {
	case defaultXAIImagesModel, xaiImagesQualityModel, xaiImages20Model:
		return true
	default:
		return false
	}
}

func registeredXAIImagesModel(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	modelRegistry := registry.GetGlobalRegistry()
	for _, candidate := range []string{model, strings.ToLower(model)} {
		for _, provider := range modelRegistry.GetModelProviders(candidate) {
			if strings.EqualFold(strings.TrimSpace(provider), "xai") {
				return candidate, true
			}
		}
	}
	return "", false
}

func isXAIVendorAlias(prefix string) bool {
	switch strings.ToLower(strings.TrimSpace(prefix)) {
	case "xai", "x-ai", "grok":
		return true
	default:
		return false
	}
}

func isXAIImagesModel(model string) bool {
	prefix, baseModel := imagesModelParts(model)
	if !isXAIImagesBaseModel(baseModel) {
		return false
	}

	trimmedPrefix := strings.TrimSpace(prefix)
	if trimmedPrefix == "" || isXAIVendorAlias(trimmedPrefix) {
		return true
	}
	if _, ok := registeredXAIImagesModel(prefix + "/" + canonicalXAIImagesModel(model)); ok {
		return true
	}
	_, ok := registeredXAIImagesModel(model)
	return ok
}

func isSupportedImagesModel(model string) bool {
	if isCodexImagesToolModel(model) {
		return true
	}
	return isXAIImagesModel(model) || isOpenAICompatImagesModel(model)
}

func isCodexImagesToolModel(model string) bool {
	switch imagesModelBase(model) {
	case gptImage15Model, defaultImagesToolModel, gptImage25FlareModel, gptImage25SunburstModel, gptImage25Model:
		return true
	default:
		return false
	}
}

func isOpenAICompatImagesModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	info := registry.LookupModelInfo(model)
	return info != nil && info.Type == registry.OpenAIImageModelType
}

func rejectUnsupportedImagesModel(c *gin.Context, model string) bool {
	if isSupportedImagesModel(model) {
		return false
	}

	c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: fmt.Sprintf("Model %s is not supported on %s or %s. Use %s, %s, %s, %s, %s, %s, %s, %s, or a configured openai-compatibility image model.", model, imagesGenerationsPath, imagesEditsPath, gptImage15Model, defaultImagesToolModel, gptImage25FlareModel, gptImage25SunburstModel, gptImage25Model, defaultXAIImagesModel, xaiImagesQualityModel, xaiImages20Model),
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
	switch baseModel {
	case xaiImagesQualityModel:
		return xaiImagesQualityModel
	case xaiImages20Model:
		return xaiImages20Model
	default:
		return defaultXAIImagesModel
	}
}

func (h *OpenAIAPIHandler) routingXAIImagesModel(model string) string {
	canonical := canonicalXAIImagesModel(model)
	prefix, _ := imagesModelParts(model)
	if prefix == "" {
		return canonical
	}
	prefixedModel := prefix + "/" + canonical
	if resolved, ok := registeredXAIImagesModel(prefixedModel); ok {
		return resolved
	}
	if resolved, ok := registeredXAIImagesModel(model); ok {
		return resolved
	}
	if h != nil && !isXAIVendorAlias(prefix) {
		if cfg := h.CurrentConfig(); cfg != nil && cfg.ForceModelPrefix {
			return prefixedModel
		}
	}
	if h != nil && h.BaseAPIHandler != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		return prefixedModel
	}
	if _, ok := registeredXAIImagesModel(canonical); !ok {
		return prefixedModel
	}
	return canonical
}

func xaiImagesAspectRatio(raw string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1:1", "square":
		return "1:1"
	case "16:9", "landscape":
		return "16:9"
	case "9:16", "portrait":
		return "9:16"
	case "9:20":
		return "9:20"
	case "20:9":
		return "20:9"
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
	case "9:20":
		return "9:20"
	case "20:9":
		return "20:9"
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
	ref, _ = ojson.SetBytes(ref, "url", strings.TrimSpace(imageURL))
	return ref
}

func buildXAIImagesBaseRequest(model string, prompt string, responseFormat string, aspectRatio string, resolution string, quality string, n int64) []byte {
	return buildXAIImagesOriginalBaseRequest(model, prompt, normalizeImagesResponseFormat(responseFormat), aspectRatio, resolution, quality, n)
}

func buildXAIImagesOriginalBaseRequestDoc(model string, prompt string, responseFormat string, aspectRatio string, resolution string, quality string, n int64) *ojson.Doc {
	doc := ojson.NewDoc([]byte(`{}`))
	_ = doc.Set("model", canonicalXAIImagesModel(model))
	_ = doc.Set("prompt", strings.TrimSpace(prompt))
	if rf := strings.TrimSpace(responseFormat); rf != "" {
		_ = doc.Set("response_format", normalizeImagesResponseFormat(rf))
	}
	if aspectRatio != "" {
		_ = doc.Set("aspect_ratio", aspectRatio)
	}
	if resolution != "" {
		_ = doc.Set("resolution", resolution)
	}
	if q := strings.TrimSpace(quality); q != "" {
		_ = doc.Set("quality", q)
	}
	if n > 0 {
		_ = doc.Set("n", n)
	}
	return doc
}

func buildXAIImagesOriginalBaseRequest(model string, prompt string, responseFormat string, aspectRatio string, resolution string, quality string, n int64) []byte {
	return buildXAIImagesOriginalBaseRequestDoc(model, prompt, responseFormat, aspectRatio, resolution, quality, n).Bytes()
}

func buildXAIImagesGenerationsRequest(rawJSON []byte, model string, responseFormat string) []byte {
	var fieldsBuf [6]ojson.Result
	fields := ojson.GetManyBytesNoCopy(fieldsBuf[:0], rawJSON, "prompt", "size", "aspect_ratio", "resolution", "quality", "n")
	prompt := strings.TrimSpace(fields[0].String())
	size := strings.TrimSpace(fields[1].String())
	aspectRatio := xaiImagesAspectRatio(fields[2].String(), "")
	aspectRatio = xaiImagesAspectRatioFromSize(size, aspectRatio)
	if aspectRatio == "" {
		aspectRatio = xaiImagesDefaultAspectRatio
	}
	resolution := xaiImagesResolution(fields[3].String(), size, xaiImagesDefaultResolution)
	quality := strings.TrimSpace(fields[4].String())
	n := int64(0)
	if v := fields[5]; v.Exists() && v.Type == ojson.Number {
		n = v.Int()
	}
	return buildXAIImagesBaseRequest(model, prompt, responseFormat, aspectRatio, resolution, quality, n)
}

func buildXAIImagesGenerationsOriginalRequest(rawJSON []byte, model string) []byte {
	var fieldsBuf [7]ojson.Result
	fields := ojson.GetManyBytesNoCopy(fieldsBuf[:0], rawJSON, "prompt", "response_format", "size", "aspect_ratio", "resolution", "quality", "n")
	prompt := strings.TrimSpace(fields[0].String())
	responseFormat := strings.TrimSpace(fields[1].String())
	size := strings.TrimSpace(fields[2].String())
	aspectRatio := xaiImagesAspectRatio(fields[3].String(), "")
	aspectRatio = xaiImagesAspectRatioFromSize(size, aspectRatio)
	resolution := xaiImagesResolution(fields[4].String(), size, "")
	quality := strings.TrimSpace(fields[5].String())
	n := int64(0)
	if v := fields[6]; v.Exists() && v.Type == ojson.Number {
		n = v.Int()
	}
	return buildXAIImagesOriginalBaseRequest(model, prompt, responseFormat, aspectRatio, resolution, quality, n)
}

func appendXAIImagesEditRefsDoc(doc *ojson.Doc, images []string) {
	trimmedImages := make([]string, 0, len(images))
	for _, img := range images {
		if strings.TrimSpace(img) != "" {
			trimmedImages = append(trimmedImages, strings.TrimSpace(img))
		}
	}
	if len(trimmedImages) == 1 {
		_ = doc.SetRawBytes("image", xaiImagesRef(trimmedImages[0]))
		return
	}
	for _, img := range trimmedImages {
		_ = doc.SetRawBytes("images.-1", xaiImagesRef(img))
	}
}

func appendXAIImagesEditRefs(req []byte, images []string) []byte {
	doc := ojson.NewDoc(req)
	appendXAIImagesEditRefsDoc(doc, images)
	return doc.Bytes()
}

func buildXAIImagesEditRequest(model string, prompt string, images []string, responseFormat string, aspectRatio string, resolution string, quality string, n int64) []byte {
	doc := buildXAIImagesOriginalBaseRequestDoc(model, prompt, normalizeImagesResponseFormat(responseFormat), aspectRatio, resolution, quality, n)
	appendXAIImagesEditRefsDoc(doc, images)
	return doc.Bytes()
}

func buildXAIImagesEditOriginalRequest(model string, prompt string, images []string, responseFormat string, aspectRatio string, resolution string, quality string, n int64) []byte {
	doc := buildXAIImagesOriginalBaseRequestDoc(model, prompt, responseFormat, aspectRatio, resolution, quality, n)
	appendXAIImagesEditRefsDoc(doc, images)
	return doc.Bytes()
}

func collectXAIImagesFromJSON(rawJSON []byte) []string {
	var images []string
	appendImage := func(url string) {
		url = strings.TrimSpace(url)
		if url != "" {
			images = append(images, url)
		}
	}

	if image := ojson.GetBytesNoCopy(rawJSON, "image"); image.Exists() {
		if image.Type == ojson.String {
			appendImage(image.String())
		} else if image.Type == ojson.JSON {
			appendImage(image.Get("image_url.url").String())
			if imageURL := image.Get("image_url"); imageURL.Type == ojson.String {
				appendImage(imageURL.String())
			}
			appendImage(image.Get("url").String())
		}
	}
	if imagesResult := ojson.GetBytesNoCopy(rawJSON, "images"); imagesResult.IsArray() {
		for _, img := range imagesResult.Array() {
			if img.Type == ojson.String {
				appendImage(img.String())
				continue
			}
			appendImage(img.Get("image_url.url").String())
			if imageURL := img.Get("image_url"); imageURL.Type == ojson.String {
				appendImage(imageURL.String())
			}
			appendImage(img.Get("url").String())
		}
	}
	return images
}

func xaiImagesEditOptionsFromJSON(rawJSON []byte) (aspectRatio string, resolution string, quality string, n int64) {
	var fieldsBuf [5]ojson.Result
	fields := ojson.GetManyBytesNoCopy(fieldsBuf[:0], rawJSON, "size", "aspect_ratio", "resolution", "quality", "n")
	size := strings.TrimSpace(fields[0].String())
	aspectRatio = xaiImagesAspectRatio(fields[1].String(), "")
	aspectRatio = xaiImagesAspectRatioFromSize(size, aspectRatio)
	resolution = xaiImagesResolution(fields[2].String(), size, "")
	quality = strings.TrimSpace(fields[3].String())
	if v := fields[4]; v.Exists() && v.Type == ojson.Number {
		n = v.Int()
	}
	return aspectRatio, resolution, quality, n
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

	return base64.EncodeDataURL(mediaType, data), nil
}

func buildOpenAICompatImagesJSONRequest(rawJSON []byte, imageModel string, stream bool) []byte {
	doc := ojson.NewDoc(rawJSON)
	if model := strings.TrimSpace(imageModel); model != "" {
		_ = doc.Set("model", model)
	}
	if stream {
		_ = doc.Set("stream", true)
	} else {
		_ = doc.Delete("stream")
	}
	return doc.Bytes()
}

func cloneMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func buildOpenAICompatImagesMultipartRequest(form *multipart.Form, imageModel string, stream bool) ([]byte, string, error) {
	if form == nil {
		return nil, "", fmt.Errorf("multipart form is nil")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if errWrite := writer.WriteField("model", imageModel); errWrite != nil {
		return nil, "", fmt.Errorf("write model field failed: %w", errWrite)
	}
	if stream {
		if errWrite := writer.WriteField("stream", "true"); errWrite != nil {
			return nil, "", fmt.Errorf("write stream field failed: %w", errWrite)
		}
	}
	for key, values := range form.Value {
		if key == "model" || key == "stream" {
			continue
		}
		for _, value := range values {
			if errWrite := writer.WriteField(key, value); errWrite != nil {
				return nil, "", fmt.Errorf("write form field %s failed: %w", key, errWrite)
			}
		}
	}

	for key, files := range form.File {
		for _, fileHeader := range files {
			if fileHeader == nil {
				continue
			}
			header := cloneMIMEHeader(fileHeader.Header)
			header.Set("Content-Disposition", multipart.FileContentDisposition(key, fileHeader.Filename))
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/octet-stream")
			}
			part, errCreate := writer.CreatePart(header)
			if errCreate != nil {
				return nil, "", fmt.Errorf("create file field %s failed: %w", key, errCreate)
			}
			src, errOpen := fileHeader.Open()
			if errOpen != nil {
				return nil, "", fmt.Errorf("open upload file failed: %w", errOpen)
			}
			_, errCopy := io.Copy(part, src)
			if errClose := src.Close(); errClose != nil {
				log.Errorf("openai images: close upload file error: %v", errClose)
				if errCopy == nil {
					errCopy = errClose
				}
			}
			if errCopy != nil {
				return nil, "", fmt.Errorf("copy upload file failed: %w", errCopy)
			}
		}
	}

	if errClose := writer.Close(); errClose != nil {
		return nil, "", fmt.Errorf("close multipart writer failed: %w", errClose)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
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
	if h != nil {
		if cfg := h.CurrentConfig(); cfg != nil && cfg.DisableImageGeneration == internalconfig.DisableImageGenerationAll {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
	}

	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
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

	imageModel := strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "model").String()))
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	if rejectUnsupportedImagesModel(c, imageModel) {
		return
	}

	prompt := strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "prompt").String())
	if prompt == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: prompt is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	responseFormat := strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "response_format").String()))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	stream := ojson.GetBytesNoCopy(rawJSON, "stream").Bool()

	if isCodexImagesToolModel(imageModel) {
		imageReq := buildOpenAICompatImagesJSONRequest(rawJSON, imageModel, stream)
		h.handleRoutedImages(c, imageReq, imageModel, stream)
		return
	}
	if isXAIImagesModel(imageModel) {
		xaiReq := buildXAIImagesGenerationsRequest(rawJSON, imageModel, responseFormat)
		originalReq := buildXAIImagesGenerationsOriginalRequest(rawJSON, imageModel)
		h.handleXAIImages(c, xaiReq, originalReq, imageModel, responseFormat, "image_generation", stream)
		return
	}
	if isOpenAICompatImagesModel(imageModel) {
		compatReq := buildOpenAICompatImagesJSONRequest(rawJSON, imageModel, stream)
		h.handleOpenAICompatImages(c, compatReq, imageModel, responseFormat, "image_generation", stream)
		return
	}

	toolDoc := ojson.NewDoc([]byte(`{"type":"image_generation","action":"generate"}`))
	_ = toolDoc.Set("model", imageModel)

	if v := strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "size").String()); v != "" {
		_ = toolDoc.Set("size", v)
	}
	if v := strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "quality").String()); v != "" {
		_ = toolDoc.Set("quality", v)
	}
	if v := strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "background").String()); v != "" {
		_ = toolDoc.Set("background", v)
	}
	if v := strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "output_format").String()); v != "" {
		_ = toolDoc.Set("output_format", v)
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "output_compression"); v.Exists() {
		if v.Type == ojson.Number {
			_ = toolDoc.Set("output_compression", v.Int())
		}
	}
	if v := ojson.GetBytesNoCopy(rawJSON, "partial_images"); v.Exists() {
		if v.Type == ojson.Number {
			_ = toolDoc.Set("partial_images", v.Int())
		}
	}
	if v := strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "moderation").String()); v != "" {
		_ = toolDoc.Set("moderation", v)
	}

	responsesReq := buildImagesResponsesRequest(prompt, nil, toolDoc.Bytes())
	if stream {
		h.streamImagesFromResponses(c, responsesReq, responseFormat, "image_generation")
		return
	}
	h.collectImagesFromResponses(c, responsesReq, responseFormat)
}

func (h *OpenAIAPIHandler) ImagesEdits(c *gin.Context) {
	if h != nil {
		if cfg := h.CurrentConfig(); cfg != nil && cfg.DisableImageGeneration == internalconfig.DisableImageGenerationAll {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
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
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
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

	rawResponseFormat := strings.TrimSpace(c.PostForm("response_format"))
	responseFormat := rawResponseFormat
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	stream := parseBoolField(c.PostForm("stream"), false)

	if isCodexImagesToolModel(imageModel) {
		imageReq, contentType, errBuild := buildOpenAICompatImagesMultipartRequest(form, imageModel, stream)
		if errBuild != nil {
			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: fmt.Sprintf("Invalid request: %v", errBuild),
					Type:    "invalid_request_error",
				},
			})
			return
		}
		c.Request.Header.Set("Content-Type", contentType)
		h.handleRoutedImages(c, imageReq, imageModel, stream)
		return
	}
	if isXAIImagesModel(imageModel) {
		aspectRatio := xaiImagesAspectRatio(c.PostForm("aspect_ratio"), "")
		aspectRatio = xaiImagesAspectRatioFromSize(c.PostForm("size"), aspectRatio)
		resolution := xaiImagesResolution(c.PostForm("resolution"), c.PostForm("size"), "")
		quality := strings.TrimSpace(c.PostForm("quality"))
		n := parseIntField(c.PostForm("n"), 0)
		xaiReq := buildXAIImagesEditRequest(imageModel, prompt, images, responseFormat, aspectRatio, resolution, quality, n)
		originalReq := buildXAIImagesEditOriginalRequest(imageModel, prompt, images, rawResponseFormat, aspectRatio, resolution, quality, n)
		h.handleXAIImages(c, xaiReq, originalReq, imageModel, responseFormat, "image_edit", stream)
		return
	}
	if isOpenAICompatImagesModel(imageModel) {
		compatReq, contentType, errBuild := buildOpenAICompatImagesMultipartRequest(form, imageModel, stream)
		if errBuild != nil {
			c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{
					Message: fmt.Sprintf("Invalid request: %v", errBuild),
					Type:    "invalid_request_error",
				},
			})
			return
		}
		c.Request.Header.Set("Content-Type", contentType)
		h.handleOpenAICompatImages(c, compatReq, imageModel, responseFormat, "image_edit", stream)
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

	toolDoc := ojson.NewDoc([]byte(`{"type":"image_generation","action":"edit"}`))
	_ = toolDoc.Set("model", imageModel)

	if v := strings.TrimSpace(c.PostForm("size")); v != "" {
		_ = toolDoc.Set("size", v)
	}
	if v := strings.TrimSpace(c.PostForm("quality")); v != "" {
		_ = toolDoc.Set("quality", v)
	}
	if v := strings.TrimSpace(c.PostForm("background")); v != "" {
		_ = toolDoc.Set("background", v)
	}
	if v := strings.TrimSpace(c.PostForm("output_format")); v != "" {
		_ = toolDoc.Set("output_format", v)
	}
	if v := strings.TrimSpace(c.PostForm("input_fidelity")); v != "" {
		_ = toolDoc.Set("input_fidelity", v)
	}
	if v := strings.TrimSpace(c.PostForm("moderation")); v != "" {
		_ = toolDoc.Set("moderation", v)
	}

	if v := strings.TrimSpace(c.PostForm("output_compression")); v != "" {
		_ = toolDoc.Set("output_compression", parseIntField(v, 0))
	}
	if v := strings.TrimSpace(c.PostForm("partial_images")); v != "" {
		_ = toolDoc.Set("partial_images", parseIntField(v, 0))
	}

	if maskDataURL != nil && strings.TrimSpace(*maskDataURL) != "" {
		_ = toolDoc.Set("input_image_mask.image_url", strings.TrimSpace(*maskDataURL))
	}

	responsesReq := buildImagesResponsesRequest(prompt, images, toolDoc.Bytes())
	if stream {
		h.streamImagesFromResponses(c, responsesReq, responseFormat, "image_edit")
		return
	}
	h.collectImagesFromResponses(c, responsesReq, responseFormat)
}

func (h *OpenAIAPIHandler) imagesEditsFromJSON(c *gin.Context) {
	rawJSON, err := handlers.ReadRequestBody(c)
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), handlers.ErrorResponse{
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

	imageModel := strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "model").String()))
	if imageModel == "" {
		imageModel = defaultImagesToolModel
	}
	if rejectUnsupportedImagesModel(c, imageModel) {
		return
	}

	prompt := strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "prompt").String())
	if prompt == "" {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: prompt is required",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	rawResponseFormat := strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, "response_format").String()))
	responseFormat := rawResponseFormat
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	stream := ojson.GetBytesNoCopy(rawJSON, "stream").Bool()

	if isCodexImagesToolModel(imageModel) {
		imageReq := buildOpenAICompatImagesJSONRequest(rawJSON, imageModel, stream)
		h.handleRoutedImages(c, imageReq, imageModel, stream)
		return
	}
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
		aspectRatio, resolution, quality, n := xaiImagesEditOptionsFromJSON(rawJSON)
		xaiReq := buildXAIImagesEditRequest(imageModel, prompt, images, responseFormat, aspectRatio, resolution, quality, n)
		originalReq := buildXAIImagesEditOriginalRequest(imageModel, prompt, images, rawResponseFormat, aspectRatio, resolution, quality, n)
		h.handleXAIImages(c, xaiReq, originalReq, imageModel, responseFormat, "image_edit", stream)
		return
	}
	if isOpenAICompatImagesModel(imageModel) {
		compatReq := buildOpenAICompatImagesJSONRequest(rawJSON, imageModel, stream)
		h.handleOpenAICompatImages(c, compatReq, imageModel, responseFormat, "image_edit", stream)
		return
	}

	var images []string
	imagesResult := ojson.GetBytesNoCopy(rawJSON, "images")
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
	if mask := ojson.GetBytesNoCopy(rawJSON, "mask.image_url"); mask.Exists() {
		url := strings.TrimSpace(mask.String())
		if url != "" {
			maskDataURL = &url
		}
	} else if mask := ojson.GetBytesNoCopy(rawJSON, "mask.file_id"); mask.Exists() {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Invalid request: mask.file_id is not supported (use mask.image_url instead)",
				Type:    "invalid_request_error",
			},
		})
		return
	}

	toolDoc := ojson.NewDoc([]byte(`{"type":"image_generation","action":"edit"}`))
	_ = toolDoc.Set("model", imageModel)

	for _, field := range []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation"} {
		if v := strings.TrimSpace(ojson.GetBytesNoCopy(rawJSON, field).String()); v != "" {
			_ = toolDoc.Set(field, v)
		}
	}

	for _, field := range []string{"output_compression", "partial_images"} {
		if v := ojson.GetBytesNoCopy(rawJSON, field); v.Exists() && v.Type == ojson.Number {
			_ = toolDoc.Set(field, v.Int())
		}
	}

	if maskDataURL != nil && strings.TrimSpace(*maskDataURL) != "" {
		_ = toolDoc.Set("input_image_mask.image_url", strings.TrimSpace(*maskDataURL))
	}

	responsesReq := buildImagesResponsesRequest(prompt, images, toolDoc.Bytes())
	if stream {
		h.streamImagesFromResponses(c, responsesReq, responseFormat, "image_edit")
		return
	}
	h.collectImagesFromResponses(c, responsesReq, responseFormat)
}

func buildImagesResponsesRequest(prompt string, images []string, toolJSON []byte) []byte {
	reqDoc := ojson.NewDoc([]byte(`{"instructions":"","stream":true,"reasoning":{"effort":"medium","summary":"auto"},"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"model":"","store":false,"tool_choice":{"type":"image_generation"}}`))
	mainModel := defaultImagesMainModel
	if len(toolJSON) > 0 && json.Valid(toolJSON) {
		toolModel := strings.TrimSpace(ojson.GetBytesNoCopy(toolJSON, "model").String())
		if idx := strings.LastIndex(toolModel, "/"); idx > 0 && idx < len(toolModel)-1 {
			prefix := strings.TrimSpace(toolModel[:idx])
			if prefix != "" {
				mainModel = prefix + "/" + defaultImagesMainModel
			}
		}
	}
	_ = reqDoc.Set("model", mainModel)

	inputDoc := ojson.NewDoc([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`))
	_ = inputDoc.Set("0.content.0.text", prompt)
	contentIndex := 1
	for _, img := range images {
		if strings.TrimSpace(img) == "" {
			continue
		}
		part, _ := ojson.SetBytes([]byte(`{"type":"input_image","image_url":""}`), "image_url", img)
		path := fmt.Sprintf("0.content.%d", contentIndex)
		_ = inputDoc.SetRawBytes(path, part)
		contentIndex++
	}
	_ = reqDoc.SetRawBytes("input", inputDoc.Bytes())

	_ = reqDoc.SetRawBytes("tools", []byte(`[]`))
	if len(toolJSON) > 0 && json.Valid(toolJSON) {
		_ = reqDoc.SetRawBytes("tools.-1", toolJSON)
	}
	return reqDoc.Bytes()
}

func extractXAIImagesResponse(payload []byte) (results []xaiImageResult, createdAt int64, usageRaw []byte, err error) {
	if !json.Valid(payload) {
		return nil, 0, nil, fmt.Errorf("upstream returned invalid image response JSON")
	}

	createdAt = ojson.GetBytesNoCopy(payload, "created").Int()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}

	data := ojson.GetBytesNoCopy(payload, "data")
	if data.IsArray() {
		for _, item := range data.Array() {
			result := xaiImageResult{
				B64JSON:       strings.Clone(strings.TrimSpace(item.Get("b64_json").String())),
				URL:           strings.Clone(strings.TrimSpace(item.Get("url").String())),
				RevisedPrompt: strings.Clone(strings.TrimSpace(item.Get("revised_prompt").String())),
				MimeType:      strings.Clone(strings.TrimSpace(item.Get("mime_type").String())),
			}
			if result.MimeType == "" {
				result.MimeType = strings.Clone(mimeTypeFromOutputFormat(strings.TrimSpace(item.Get("output_format").String())))
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

	if usage := ojson.GetBytesNoCopy(payload, "usage"); usage.Exists() && usage.IsObject() {
		usageRaw = bytes.Clone([]byte(usage.Raw))
	}

	return results, createdAt, usageRaw, nil
}

func buildImagesAPIResponseFromXAI(payload []byte, responseFormat string) ([]byte, error) {
	results, createdAt, usageRaw, err := extractXAIImagesResponse(payload)
	if err != nil {
		return nil, err
	}

	outDoc := ojson.NewDoc([]byte(`{"created":0,"data":[]}`))
	_ = outDoc.Set("created", createdAt)
	responseFormat = normalizeImagesResponseFormat(responseFormat)

	for _, img := range results {
		itemDoc := ojson.NewDoc([]byte(`{}`))
		if responseFormat == "url" {
			if img.URL != "" {
				_ = itemDoc.Set("url", img.URL)
			} else {
				_ = itemDoc.Set("url", "data:"+mimeTypeFromOutputFormat(img.MimeType)+";base64,"+img.B64JSON)
			}
		} else if img.B64JSON != "" {
			_ = itemDoc.Set("b64_json", img.B64JSON)
		} else {
			_ = itemDoc.Set("url", img.URL)
		}
		if img.RevisedPrompt != "" {
			_ = itemDoc.Set("revised_prompt", img.RevisedPrompt)
		}
		_ = outDoc.SetRawBytes("data.-1", itemDoc.Bytes())
	}

	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		_ = outDoc.SetRawBytes("usage", usageRaw)
	}

	return outDoc.Bytes(), nil
}

func (h *OpenAIAPIHandler) handleXAIImages(c *gin.Context, xaiReq []byte, originalReq []byte, imageModel string, responseFormat string, streamPrefix string, stream bool) {
	if stream {
		h.streamXAIImages(c, xaiReq, originalReq, imageModel, responseFormat, streamPrefix)
		return
	}
	h.collectXAIImages(c, xaiReq, originalReq, imageModel, responseFormat)
}

func (h *OpenAIAPIHandler) handleOpenAICompatImages(c *gin.Context, compatReq []byte, imageModel string, responseFormat string, streamPrefix string, stream bool) {
	if stream {
		h.streamOpenAICompatImages(c, compatReq, imageModel)
		return
	}
	h.collectImagesWithModel(c, compatReq, imageModel, responseFormat)
}

func (h *OpenAIAPIHandler) handleRoutedImages(c *gin.Context, imageReq []byte, imageModel string, stream bool) {
	if stream {
		h.streamRoutedImages(c, imageReq, imageModel)
		return
	}
	h.collectRoutedImages(c, imageReq, imageModel)
}

func (h *OpenAIAPIHandler) collectRoutedImages(c *gin.Context, imageReq []byte, imageModel string) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	cliCtx = handlers.WithDisallowFreeAuth(cliCtx)
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	model := strings.TrimSpace(imageModel)
	resp, upstreamHeaders, errMsg := h.ExecuteImageWithAuthManager(cliCtx, xaiImagesHandlerType, model, imageReq, "")
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

func (h *OpenAIAPIHandler) streamRoutedImages(c *gin.Context, imageReq []byte, imageModel string) {
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
	model := strings.TrimSpace(imageModel)
	execution, streamStarted, canceled := h.waitImagesStreamExecution(c, flusher, cliCancel, func() imagesStreamExecutionResult {
		dataChan, upstreamHeaders, errChan := h.ExecuteImageStreamWithAuthManager(cliCtx, xaiImagesHandlerType, model, imageReq, "")
		return imagesStreamExecutionResult{Data: dataChan, UpstreamHeaders: upstreamHeaders, Errs: errChan}
	})
	if canceled {
		return
	}
	dataChan := execution.Data
	upstreamHeaders := execution.UpstreamHeaders
	errChan := execution.Errs
	rawStopKeepAlive, keepAliveC := h.newImagesPreFirstTokenKeepAliveTicker()
	stopKeepAlive := func() {
		if rawStopKeepAlive != nil {
			rawStopKeepAlive()
			rawStopKeepAlive = nil
			keepAliveC = nil
		}
	}
	defer stopKeepAlive()

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
			if streamStarted {
				writeImagesStreamErrorEvent(c, errMsg)
				flusher.Flush()
			} else {
				h.WriteErrorResponse(c, errMsg)
			}
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				stopKeepAlive()
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					if streamStarted {
						writeImagesStreamErrorEvent(c, errMsg)
						flusher.Flush()
					} else {
						h.WriteErrorResponse(c, errMsg)
					}
					cliCancel(errMsg.Error)
					return
				}
				setImagesSSEHeaders(c)
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cliCancel(nil)
				return
			}

			stopKeepAlive()
			setImagesSSEHeaders(c)
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			_, _ = c.Writer.Write(chunk)
			flusher.Flush()
			streamStarted = true
			h.forwardRawImageStream(cliCtx, c, func(err error) { cliCancel(err) }, dataChan, errChan)
			return
		case <-keepAliveC:
			setImagesSSEHeaders(c)
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			writeImagesStreamKeepAlive(c, flusher)
			streamStarted = true
		}
	}
}

func (h *OpenAIAPIHandler) forwardRawImageStream(ctx context.Context, c *gin.Context, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	stopKeepAlive, keepAliveC := h.newImagesStreamKeepAliveTicker()
	defer stopKeepAlive()

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return
		case <-ctx.Done():
			cancel(ctx.Err())
			return
		case errMsg, ok := <-errs:
			if ok && errMsg != nil {
				writeImagesStreamErrorEvent(c, errMsg)
				if flusher, ok := c.Writer.(http.Flusher); ok {
					flusher.Flush()
				}
				cancel(errMsg.Error)
				return
			}
			errs = nil
		case chunk, ok := <-data:
			if !ok {
				if errMsg, hasPendingError := handlers.PendingStreamError(errs); hasPendingError {
					writeImagesStreamErrorEvent(c, errMsg)
					if flusher, ok := c.Writer.(http.Flusher); ok {
						flusher.Flush()
					}
					cancel(errMsg.Error)
					return
				}
				cancel(nil)
				return
			}
			_, _ = c.Writer.Write(chunk)
			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
		case <-keepAliveC:
			if flusher, ok := c.Writer.(http.Flusher); ok {
				writeImagesStreamKeepAlive(c, flusher)
			}
		}
	}
}

func (h *OpenAIAPIHandler) streamOpenAICompatImages(c *gin.Context, compatReq []byte, imageModel string) {
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
	model := strings.TrimSpace(imageModel)
	execution, streamStarted, canceled := h.waitImagesStreamExecution(c, flusher, cliCancel, func() imagesStreamExecutionResult {
		dataChan, upstreamHeaders, errChan := h.ExecuteImageStreamWithAuthManager(cliCtx, xaiImagesHandlerType, model, compatReq, "")
		return imagesStreamExecutionResult{Data: dataChan, UpstreamHeaders: upstreamHeaders, Errs: errChan}
	})
	if canceled {
		return
	}
	dataChan := execution.Data
	upstreamHeaders := execution.UpstreamHeaders
	errChan := execution.Errs
	rawStopKeepAlive, keepAliveC := h.newImagesPreFirstTokenKeepAliveTicker()
	stopKeepAlive := func() {
		if rawStopKeepAlive != nil {
			rawStopKeepAlive()
			rawStopKeepAlive = nil
			keepAliveC = nil
		}
	}
	defer stopKeepAlive()

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
			if streamStarted {
				writeImagesStreamErrorEvent(c, errMsg)
				flusher.Flush()
			} else {
				h.WriteErrorResponse(c, errMsg)
			}
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				stopKeepAlive()
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					if streamStarted {
						writeImagesStreamErrorEvent(c, errMsg)
						flusher.Flush()
					} else {
						h.WriteErrorResponse(c, errMsg)
					}
					cliCancel(errMsg.Error)
					return
				}
				setImagesSSEHeaders(c)
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				flusher.Flush()
				cliCancel(nil)
				return
			}

			stopKeepAlive()
			setImagesSSEHeaders(c)
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			_, _ = c.Writer.Write(chunk)
			flusher.Flush()
			streamStarted = true
			h.ForwardStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, handlers.StreamForwardOptions{
				NormalizeTerminalError: sanitizeResponsesStreamErrorMessage,
				WriteChunk: func(next []byte) {
					_, _ = c.Writer.Write(next)
				},
				WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
					writeImagesStreamErrorEvent(c, errMsg)
				},
			})
			return
		case <-keepAliveC:
			setImagesSSEHeaders(c)
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			writeImagesStreamKeepAlive(c, flusher)
			streamStarted = true
		}
	}
}

func (h *OpenAIAPIHandler) collectXAIImages(c *gin.Context, xaiReq []byte, originalReq []byte, imageModel string, responseFormat string) {
	model := strings.TrimSpace(imageModel)
	if model == "" {
		model = strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(xaiReq, "model").String()))
	}
	h.collectImagesWithModelAndOriginal(c, xaiReq, originalReq, h.routingXAIImagesModel(model), responseFormat)
}

func (h *OpenAIAPIHandler) collectImagesWithModel(c *gin.Context, imageReq []byte, model string, responseFormat string) {
	h.collectImagesWithModelAndOriginal(c, imageReq, nil, model, responseFormat)
}

func (h *OpenAIAPIHandler) collectImagesWithModelAndOriginal(c *gin.Context, imageReq []byte, originalReq []byte, model string, responseFormat string) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	if len(originalReq) > 0 {
		cliCtx = handlers.WithOriginalRequest(cliCtx, originalReq)
	}
	cliCtx = handlers.WithFinalizedResponseFormatCallback(cliCtx, func(finalizedFormat string) {
		responseFormat = finalizedFormat
	})
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	model = strings.TrimSpace(model)
	resp, upstreamHeaders, errMsg := h.ExecuteImageWithAuthManager(cliCtx, xaiImagesHandlerType, model, imageReq, "")
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

func (h *OpenAIAPIHandler) streamXAIImages(c *gin.Context, xaiReq []byte, originalReq []byte, imageModel string, responseFormat string, streamPrefix string) {
	model := strings.TrimSpace(imageModel)
	if model == "" {
		model = strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(xaiReq, "model").String()))
	}
	h.streamImagesWithModel(c, xaiReq, originalReq, h.routingXAIImagesModel(model), responseFormat, streamPrefix)
}

func (h *OpenAIAPIHandler) streamImagesWithModel(c *gin.Context, imageReq []byte, originalReq []byte, model string, responseFormat string, streamPrefix string) {
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
	if len(originalReq) > 0 {
		cliCtx = handlers.WithOriginalRequest(cliCtx, originalReq)
	}
	model = strings.TrimSpace(model)
	type imageStreamResult struct {
		resp            []byte
		upstreamHeaders http.Header
		errMsg          *interfaces.ErrorMessage
		responseFormat  string
	}
	resultChan := make(chan imageStreamResult, 1)
	go func() {
		effectiveFormat := responseFormat
		execCtx := handlers.WithFinalizedResponseFormatCallback(cliCtx, func(finalizedFormat string) {
			effectiveFormat = finalizedFormat
		})
		resp, upstreamHeaders, errMsg := h.ExecuteImageWithAuthManager(execCtx, xaiImagesHandlerType, model, imageReq, "")
		resultChan <- imageStreamResult{resp: resp, upstreamHeaders: upstreamHeaders, errMsg: errMsg, responseFormat: effectiveFormat}
	}()

	rawStopKeepAlive, keepAliveC := h.newImagesPreFirstTokenKeepAliveTicker()
	stopKeepAlive := func() {
		if rawStopKeepAlive != nil {
			rawStopKeepAlive()
			rawStopKeepAlive = nil
			keepAliveC = nil
		}
	}
	defer stopKeepAlive()
	streamStarted := false
	writeError := func(errMsg *interfaces.ErrorMessage) {
		if streamStarted {
			writeImagesStreamErrorEvent(c, errMsg)
			flusher.Flush()
		} else {
			h.WriteErrorResponse(c, errMsg)
		}
		if errMsg != nil && errMsg.Error != nil {
			cliCancel(errMsg.Error)
		} else {
			cliCancel(nil)
		}
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			<-resultChan
			return
		case <-keepAliveC:
			setImagesSSEHeaders(c)
			writeImagesStreamKeepAlive(c, flusher)
			streamStarted = true
		case result := <-resultChan:
			stopKeepAlive()
			if result.errMsg != nil {
				writeError(result.errMsg)
				return
			}

			results, _, usageRaw, err := extractXAIImagesResponse(result.resp)
			if err != nil {
				writeError(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
				return
			}

			setImagesSSEHeaders(c)
			handlers.WriteUpstreamHeaders(c.Writer.Header(), result.upstreamHeaders)

			eventName := streamPrefix + ".completed"
			responseFormat = normalizeImagesResponseFormat(result.responseFormat)
			for _, img := range results {
				dataDoc := ojson.NewDoc([]byte(`{"type":""}`))
				_ = dataDoc.Set("type", eventName)
				if responseFormat == "url" {
					if img.URL != "" {
						_ = dataDoc.Set("url", img.URL)
					} else {
						_ = dataDoc.Set("url", "data:"+mimeTypeFromOutputFormat(img.MimeType)+";base64,"+img.B64JSON)
					}
				} else if img.B64JSON != "" {
					_ = dataDoc.Set("b64_json", img.B64JSON)
				} else {
					_ = dataDoc.Set("url", img.URL)
				}
				if len(usageRaw) > 0 && json.Valid(usageRaw) {
					_ = dataDoc.SetRawBytes("usage", usageRaw)
				}
				if strings.TrimSpace(eventName) != "" {
					_, _ = fmt.Fprintf(c.Writer, "event: %s\n", eventName)
				}
				_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(dataDoc.Bytes()))
				flusher.Flush()
				streamStarted = true
			}
			cliCancel(nil)
			return
		}
	}
}

func (h *OpenAIAPIHandler) collectImagesFromResponses(c *gin.Context, responsesReq []byte, responseFormat string) {
	c.Header("Content-Type", "application/json")

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	cliCtx = handlers.WithDisallowFreeAuth(cliCtx)
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	mainModel := strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(responsesReq, "model").String()))
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
		payload, ok := responsesSSEDataPayload(frame)
		if !ok || len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return nil, false, nil
		}
		if !json.Valid(payload) {
			return nil, false, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("invalid SSE data JSON")}
		}
		payloadType := ojson.GetBytesNoCopy(payload, "type").String()
		if responsesSSEErrorEvent(payloadType) || responsesSSEErrorEvent(responsesSSEEventName(frame)) || responsesSSEPayloadHasError(payload) {
			return nil, false, responsesSSEPayloadErrorMessage(payload)
		}
		if payloadType != "response.completed" {
			return nil, false, nil
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

	for {
		select {
		case <-ctx.Done():
			errCtx := ctx.Err()
			return nil, &interfaces.ErrorMessage{
				StatusCode: clienterror.HTTPStatusFromErrorOr(errCtx, http.StatusRequestTimeout),
				Error:      errCtx,
			}
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
				if errMsg, hasPendingError := handlers.PendingStreamError(errs); hasPendingError {
					return nil, errMsg
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
	if ojson.GetBytesNoCopy(payload, "type").String() != "response.completed" {
		return nil, 0, nil, imageCallResult{}, fmt.Errorf("unexpected event type")
	}

	createdAt = ojson.GetBytesNoCopy(payload, "response.created_at").Int()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}

	output := ojson.GetBytesNoCopy(payload, "response.output")
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
				Result:        strings.Clone(res),
				RevisedPrompt: strings.Clone(strings.TrimSpace(item.Get("revised_prompt").String())),
				OutputFormat:  strings.Clone(strings.TrimSpace(item.Get("output_format").String())),
				Size:          strings.Clone(strings.TrimSpace(item.Get("size").String())),
				Background:    strings.Clone(strings.TrimSpace(item.Get("background").String())),
				Quality:       strings.Clone(strings.TrimSpace(item.Get("quality").String())),
			}
			if len(results) == 0 {
				firstMeta = entry
			}
			results = append(results, entry)
		}
	}

	if usage := ojson.GetBytesNoCopy(payload, "response.tool_usage.image_gen"); usage.Exists() && usage.IsObject() {
		usageRaw = bytes.Clone([]byte(usage.Raw))
	}

	return results, createdAt, usageRaw, firstMeta, nil
}

func buildImagesAPIResponse(results []imageCallResult, createdAt int64, usageRaw []byte, firstMeta imageCallResult, responseFormat string) ([]byte, error) {
	outDoc := ojson.NewDoc([]byte(`{"created":0,"data":[]}`))
	_ = outDoc.Set("created", createdAt)

	responseFormat = strings.ToLower(strings.TrimSpace(responseFormat))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}

	for _, img := range results {
		itemDoc := ojson.NewDoc([]byte(`{}`))
		if responseFormat == "url" {
			mt := mimeTypeFromOutputFormat(img.OutputFormat)
			_ = itemDoc.Set("url", "data:"+mt+";base64,"+img.Result)
		} else {
			_ = itemDoc.Set("b64_json", img.Result)
		}
		if img.RevisedPrompt != "" {
			_ = itemDoc.Set("revised_prompt", img.RevisedPrompt)
		}
		_ = outDoc.SetRawBytes("data.-1", itemDoc.Bytes())
	}

	if firstMeta.Background != "" {
		_ = outDoc.Set("background", firstMeta.Background)
	}
	if firstMeta.OutputFormat != "" {
		_ = outDoc.Set("output_format", firstMeta.OutputFormat)
	}
	if firstMeta.Quality != "" {
		_ = outDoc.Set("quality", firstMeta.Quality)
	}
	if firstMeta.Size != "" {
		_ = outDoc.Set("size", firstMeta.Size)
	}

	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		_ = outDoc.SetRawBytes("usage", usageRaw)
	}

	return outDoc.Bytes(), nil
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
	mainModel := strings.Clone(strings.TrimSpace(ojson.GetBytesNoCopy(responsesReq, "model").String()))
	if mainModel == "" {
		mainModel = defaultImagesMainModel
	}
	execution, streamStarted, canceled := h.waitImagesStreamExecution(c, flusher, cliCancel, func() imagesStreamExecutionResult {
		dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, "openai-response", mainModel, responsesReq, "")
		return imagesStreamExecutionResult{Data: dataChan, UpstreamHeaders: upstreamHeaders, Errs: errChan}
	})
	if canceled {
		return
	}
	dataChan := execution.Data
	upstreamHeaders := execution.UpstreamHeaders
	errChan := execution.Errs
	rawStopKeepAlive, keepAliveC := h.newImagesPreFirstTokenKeepAliveTicker()
	stopKeepAlive := func() {
		if rawStopKeepAlive != nil {
			rawStopKeepAlive()
			rawStopKeepAlive = nil
			keepAliveC = nil
		}
	}
	defer stopKeepAlive()

	writeEvent := func(eventName string, dataJSON []byte) {
		if strings.TrimSpace(eventName) != "" {
			_, _ = fmt.Fprintf(c.Writer, "event: %s\n", eventName)
		}
		_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", string(dataJSON))
		flusher.Flush()
	}

	// Peek for the first chunk/error while still allowing configured SSE heartbeats.
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
			if streamStarted {
				writeImagesStreamErrorEvent(c, errMsg)
				flusher.Flush()
			} else {
				h.WriteErrorResponse(c, errMsg)
			}
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				stopKeepAlive()
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					if streamStarted {
						writeImagesStreamErrorEvent(c, errMsg)
						flusher.Flush()
					} else {
						h.WriteErrorResponse(c, errMsg)
					}
					cliCancel(errMsg.Error)
					return
				}
				setImagesSSEHeaders(c)
				handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				_, _ = c.Writer.Write([]byte("\n"))
				flusher.Flush()
				cliCancel(nil)
				return
			}

			stopKeepAlive()
			setImagesSSEHeaders(c)
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

			h.forwardImagesStream(cliCtx, c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, chunk, responseFormat, streamPrefix, writeEvent)
			return
		case <-keepAliveC:
			setImagesSSEHeaders(c)
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			writeImagesStreamKeepAlive(c, flusher)
			streamStarted = true
		}
	}
}

func (h *OpenAIAPIHandler) forwardImagesStream(ctx context.Context, c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, firstChunk []byte, responseFormat string, streamPrefix string, writeEvent func(string, []byte)) {
	acc := &sseFrameAccumulator{}

	responseFormat = strings.ToLower(strings.TrimSpace(responseFormat))
	if responseFormat == "" {
		responseFormat = "b64_json"
	}
	stopKeepAlive, keepAliveC := h.newImagesStreamKeepAliveTicker()
	defer stopKeepAlive()

	emitError := func(errMsg *interfaces.ErrorMessage) *interfaces.ErrorMessage {
		errMsg = writeImagesStreamErrorEvent(c, errMsg)
		flusher.Flush()
		return errMsg
	}

	processFrame := func(frame []byte) (bool, *interfaces.ErrorMessage) {
		payload, ok := responsesSSEDataPayload(frame)
		if !ok || len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return false, nil
		}
		if !json.Valid(payload) {
			return true, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("invalid SSE data JSON")}
		}
		payloadType := ojson.GetBytesNoCopy(payload, "type").String()
		if responsesSSEErrorEvent(payloadType) || responsesSSEErrorEvent(responsesSSEEventName(frame)) || responsesSSEPayloadHasError(payload) {
			return true, responsesSSEPayloadErrorMessage(payload)
		}

		switch payloadType {
		case "response.image_generation_call.partial_image":
			b64 := strings.TrimSpace(ojson.GetBytesNoCopy(payload, "partial_image_b64").String())
			if b64 == "" {
				return false, nil
			}
			outputFormat := strings.TrimSpace(ojson.GetBytesNoCopy(payload, "output_format").String())
			index := ojson.GetBytesNoCopy(payload, "partial_image_index").Int()
			eventName := streamPrefix + ".partial_image"
			dataDoc := ojson.NewDoc([]byte(`{"type":"","partial_image_index":0}`))
			_ = dataDoc.Set("type", eventName)
			_ = dataDoc.Set("partial_image_index", index)
			if responseFormat == "url" {
				mt := mimeTypeFromOutputFormat(outputFormat)
				_ = dataDoc.Set("url", "data:"+mt+";base64,"+b64)
			} else {
				_ = dataDoc.Set("b64_json", b64)
			}
			writeEvent(eventName, dataDoc.Bytes())
		case "response.completed":
			results, _, usageRaw, _, err := extractImagesFromResponsesCompleted(payload)
			if err != nil {
				return true, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err}
			}
			if len(results) == 0 {
				return true, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("upstream did not return image output")}
			}
			eventName := streamPrefix + ".completed"
			for _, img := range results {
				dataDoc := ojson.NewDoc([]byte(`{"type":""}`))
				_ = dataDoc.Set("type", eventName)
				if responseFormat == "url" {
					mt := mimeTypeFromOutputFormat(img.OutputFormat)
					_ = dataDoc.Set("url", "data:"+mt+";base64,"+img.Result)
				} else {
					_ = dataDoc.Set("b64_json", img.Result)
				}
				if len(usageRaw) > 0 && json.Valid(usageRaw) {
					_ = dataDoc.SetRawBytes("usage", usageRaw)
				}
				writeEvent(eventName, dataDoc.Bytes())
			}
			return true, nil
		}
		return false, nil
	}

	handleFrame := func(frame []byte) bool {
		done, errMsg := processFrame(frame)
		if !done {
			return false
		}
		if errMsg != nil {
			errMsg = emitError(errMsg)
			cancel(errMsg.Error)
		} else {
			cancel(nil)
		}
		return true
	}

	for _, frame := range acc.AddChunk(firstChunk) {
		if handleFrame(frame) {
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
				errMsg = emitError(errMsg)
				cancel(errMsg.Error)
				return
			}
			errs = nil
		case chunk, ok := <-data:
			if !ok {
				for _, frame := range acc.Flush() {
					if handleFrame(frame) {
						return
					}
				}
				if errMsg, hasPendingError := handlers.PendingStreamError(errs); hasPendingError {
					errMsg = emitError(errMsg)
					cancel(errMsg.Error)
					return
				}
				cancel(nil)
				return
			}
			for _, frame := range acc.AddChunk(chunk) {
				if handleFrame(frame) {
					return
				}
			}
		case <-keepAliveC:
			writeImagesStreamKeepAlive(c, flusher)
		}
	}
}
