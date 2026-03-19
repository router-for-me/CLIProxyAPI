package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	audioTranscriptionUploadLimitBytes        int64 = 32 << 20
	audioTranscriptionNonFileFieldsLimitBytes int64 = 1 << 20
	audioTranscriptionContentSniffBytes             = 512
	audioTranscriptionFileFieldName                 = "file"
	audioTranscriptionModelFieldName                = "model"
	audioTranscriptionResponseFormatFieldName       = "response_format"
	audioTranscriptionStreamFieldName               = "stream"
	audioTranscriptionDefaultFilename               = "audio.webm"
	audioTranscriptionTempFilePattern               = "cliproxy-audio-transcription-*"
	audioTranscriptionResponseTempFilePattern       = "cliproxy-audio-transcription-response-*"
	openAIAudioTranscriptionsPath                   = "/audio/transcriptions"
	defaultCodexAudioTranscriptionURL               = "https://chatgpt.com/backend-api/transcribe"
)

var supportedAudioFileExtensions = map[string]struct{}{
	".flac": {},
	".m4a":  {},
	".mp3":  {},
	".mp4":  {},
	".mpeg": {},
	".mpga": {},
	".ogg":  {},
	".wav":  {},
	".webm": {},
}

var supportedAudioMediaTypes = map[string]struct{}{
	"audio/flac": {},
	"audio/m4a":  {},
	"audio/mp3":  {},
	"audio/mp4":  {},
	"audio/mpeg": {},
	"audio/mpga": {},
	"audio/ogg":  {},
	"audio/wav":  {},
	"audio/webm": {},
	"video/mp4":  {},
	"video/webm": {},
}

type audioFormField struct {
	Name  string
	Value string
}

type audioTranscriptionRequest struct {
	Model           string
	ResponseFormat  string
	Stream          bool
	Fields          []audioFormField
	FileName        string
	FileContentType string
	StagedFilePath  string
}

type audioRequestError struct {
	status int
	msg    string
}

func (e *audioRequestError) Error() string {
	if e == nil {
		return ""
	}
	return e.msg
}

func (e *audioRequestError) StatusCode() int {
	if e == nil || e.status <= 0 {
		return http.StatusInternalServerError
	}
	return e.status
}

// AudioTranscriptions handles the /v1/audio/transcriptions endpoint.
func (h *OpenAIAPIHandler) AudioTranscriptions(c *gin.Context) {
	audioReq, err := parseAudioTranscriptionRequest(c)
	if err != nil {
		c.JSON(statusCodeOrDefault(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: err.Error(),
				Type:    "invalid_request_error",
			},
		})
		return
	}
	defer func() {
		_ = audioReq.Cleanup()
	}()
	var pinnedAuthID string
	audioReq.Model, pinnedAuthID, err = resolveAudioAutoSelection(h.AuthManager, audioReq.Model)
	if err != nil {
		c.JSON(statusCodeOrDefault(err, http.StatusBadRequest), handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: err.Error(),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	if pinnedAuthID != "" {
		cliCtx = handlers.WithPinnedAuthID(cliCtx, pinnedAuthID)
	}
	upstreamResp, _, errMsg := h.ExecuteHTTPRequestWithAuthManager(cliCtx, audioReq.Model, func(ctx context.Context, auth *coreauth.Auth, upstreamModel string) (*http.Request, error) {
		return audioReq.BuildHTTPRequest(ctx, auth, upstreamModel)
	})
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	defer func() {
		_ = upstreamResp.Body.Close()
	}()

	filteredHeaders := handlers.FilterUpstreamHeaders(upstreamResp.Header)
	if audioReq.ShouldStreamResponse(filteredHeaders, upstreamResp.Header) {
		if err := h.writeAudioStreamingResponse(c, upstreamResp.Body, filteredHeaders, upstreamResp.Header); err != nil {
			h.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: statusCodeOrDefault(err, http.StatusBadGateway),
				Error:      err,
			})
			cliCancel(err)
			return
		}
		cliCancel(nil)
		return
	}

	if audioReq.PreserveRawResponse() {
		if err := h.writeAudioRawResponse(c, upstreamResp.Body, filteredHeaders, upstreamResp.Header); err != nil {
			h.WriteErrorResponse(c, &interfaces.ErrorMessage{
				StatusCode: statusCodeOrDefault(err, http.StatusBadGateway),
				Error:      err,
			})
			cliCancel(err)
			return
		}
		cliCancel(nil)
		return
	}

	c.Header("Content-Type", "application/json")
	if handlers.PassthroughHeadersEnabled(h.Cfg) {
		handlers.WriteUpstreamHeaders(c.Writer.Header(), filteredHeaders)
	}
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	normalizedPath, err := normalizeAudioTranscriptionResponseFromReader(upstreamResp.Body)
	stopKeepAlive()
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{
			StatusCode: statusCodeOrDefault(err, http.StatusBadGateway),
			Error:      err,
		})
		cliCancel(err)
		return
	}
	defer func() {
		if normalizedPath != "" {
			_ = os.Remove(normalizedPath)
		}
	}()

	c.Status(http.StatusOK)
	if err := writeStagedAudioTranscriptionResponse(c.Writer, normalizedPath); err != nil {
		cliCancel(err)
		return
	}
	cliCancel()
}

func (h *OpenAIAPIHandler) writeAudioStreamingResponse(c *gin.Context, body io.Reader, filteredHeaders, upstreamHeaders http.Header) error {
	if c == nil {
		return fmt.Errorf("missing response writer")
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return &audioRequestError{status: http.StatusInternalServerError, msg: "streaming not supported"}
	}
	if contentType := audioTranscriptionContentType(filteredHeaders, upstreamHeaders); contentType != "" {
		c.Header("Content-Type", contentType)
	} else {
		c.Header("Content-Type", "text/event-stream")
	}
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
	if handlers.PassthroughHeadersEnabled(h.Cfg) {
		handlers.WriteUpstreamHeaders(c.Writer.Header(), filteredHeaders)
	}
	c.Status(http.StatusOK)
	return copyAudioResponse(c, flusher, body)
}

func (h *OpenAIAPIHandler) writeAudioRawResponse(c *gin.Context, body io.Reader, filteredHeaders, upstreamHeaders http.Header) error {
	if c == nil {
		return fmt.Errorf("missing response writer")
	}
	if contentType := audioTranscriptionContentType(filteredHeaders, upstreamHeaders); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	if handlers.PassthroughHeadersEnabled(h.Cfg) {
		handlers.WriteUpstreamHeaders(c.Writer.Header(), filteredHeaders)
	}
	c.Status(http.StatusOK)
	_, err := io.Copy(c.Writer, body)
	return err
}

func copyAudioResponse(c *gin.Context, flusher http.Flusher, body io.Reader) error {
	if c == nil || flusher == nil {
		return fmt.Errorf("streaming not supported")
	}
	if body == nil {
		return nil
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, errWrite := c.Writer.Write(buf[:n]); errWrite != nil {
				return errWrite
			}
			flusher.Flush()
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-c.Request.Context().Done():
			return c.Request.Context().Err()
		default:
		}
	}
}

func parseAudioTranscriptionRequest(c *gin.Context) (*audioTranscriptionRequest, error) {
	if c == nil || c.Request == nil {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "missing request"}
	}

	reader, err := c.Request.MultipartReader()
	if err != nil {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("invalid multipart form: %v", err)}
	}

	audioReq := &audioTranscriptionRequest{}
	cleanupOnError := true
	var nonFileBytesRead int64
	defer func() {
		if cleanupOnError {
			_ = audioReq.Cleanup()
		}
	}()

	hasFile := false
	for {
		part, errNext := reader.NextPart()
		if errors.Is(errNext, io.EOF) {
			break
		}
		if errNext != nil {
			return nil, &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("invalid multipart form: %v", errNext)}
		}
		if part == nil {
			continue
		}

		partName := part.FormName()
		fileName := part.FileName()

		if fileName == "" {
			fieldValue, errRead := readAudioTranscriptionField(part, audioTranscriptionNonFileFieldsLimitBytes-nonFileBytesRead)
			_ = part.Close()
			if errRead != nil {
				return nil, &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("failed to read form field %q: %v", partName, errRead)}
			}
			nonFileBytesRead += int64(len(fieldValue))
			if nonFileBytesRead > audioTranscriptionNonFileFieldsLimitBytes {
				return nil, &audioRequestError{
					status: http.StatusBadRequest,
					msg:    fmt.Sprintf("non-file multipart fields exceed %d byte limit", audioTranscriptionNonFileFieldsLimitBytes),
				}
			}
			field := audioFormField{Name: partName, Value: string(fieldValue)}
			audioReq.Fields = append(audioReq.Fields, field)
			if partName == audioTranscriptionModelFieldName && audioReq.Model == "" {
				audioReq.Model = strings.TrimSpace(field.Value)
			}
			if partName == audioTranscriptionResponseFormatFieldName && audioReq.ResponseFormat == "" {
				audioReq.ResponseFormat = strings.TrimSpace(field.Value)
			}
			if partName == audioTranscriptionStreamFieldName && !audioReq.Stream {
				if parsed, errParse := strconv.ParseBool(strings.TrimSpace(field.Value)); errParse == nil {
					audioReq.Stream = parsed
				}
			}
			continue
		}

		if partName != audioTranscriptionFileFieldName {
			_ = part.Close()
			continue
		}
		if hasFile {
			_ = part.Close()
			return nil, &audioRequestError{status: http.StatusBadRequest, msg: "only one file upload is supported"}
		}

		audioReq.FileName = fileName
		audioReq.FileContentType = strings.TrimSpace(part.Header.Get("Content-Type"))
		if errStage := audioReq.stageFilePart(part); errStage != nil {
			_ = part.Close()
			return nil, errStage
		}
		_ = part.Close()
		hasFile = true
	}

	if audioReq.Model == "" {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "missing required field: model"}
	}
	if !hasFile {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "missing required field: file"}
	}

	sort.SliceStable(audioReq.Fields, func(i, j int) bool {
		return audioReq.Fields[i].Name < audioReq.Fields[j].Name
	})

	cleanupOnError = false
	return audioReq, nil
}

func readAudioTranscriptionField(part *multipart.Part, remainingBytes int64) ([]byte, error) {
	if remainingBytes < 0 {
		return nil, fmt.Errorf("non-file multipart fields exceed %d byte limit", audioTranscriptionNonFileFieldsLimitBytes)
	}
	limitedReader := &io.LimitedReader{R: part, N: remainingBytes + 1}
	payload, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > remainingBytes {
		return nil, fmt.Errorf("non-file multipart fields exceed %d byte limit", audioTranscriptionNonFileFieldsLimitBytes)
	}
	return payload, nil
}

func (r *audioTranscriptionRequest) stageFilePart(part *multipart.Part) error {
	if r == nil {
		return &audioRequestError{status: http.StatusBadRequest, msg: "audio transcription request is empty"}
	}

	tempFile, err := os.CreateTemp("", audioTranscriptionTempFilePattern)
	if err != nil {
		return fmt.Errorf("failed to create temp file for audio upload: %w", err)
	}

	tempPath := tempFile.Name()
	keepTempFile := false
	defer func() {
		_ = tempFile.Close()
		if !keepTempFile {
			_ = os.Remove(tempPath)
		}
	}()

	limitedReader := &io.LimitedReader{R: part, N: audioTranscriptionUploadLimitBytes + 1}
	sniffBuffer := make([]byte, audioTranscriptionContentSniffBytes)
	sniffedBytes, readErr := io.ReadFull(limitedReader, sniffBuffer)
	switch {
	case errors.Is(readErr, io.EOF):
		return &audioRequestError{status: http.StatusBadRequest, msg: "uploaded file is empty"}
	case errors.Is(readErr, io.ErrUnexpectedEOF):
	case readErr != nil:
		return &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("failed to read uploaded file: %v", readErr)}
	}

	sniffedContent := sniffBuffer[:sniffedBytes]
	detectedContentType := http.DetectContentType(sniffedContent)
	resolvedContentType := strings.TrimSpace(r.FileContentType)
	if resolvedContentType == "" {
		resolvedContentType = detectedContentType
	}

	if errValidate := validateAudioFile(r.FileName, resolvedContentType, detectedContentType); errValidate != nil {
		return errValidate
	}

	if _, errWrite := tempFile.Write(sniffedContent); errWrite != nil {
		return fmt.Errorf("failed to stage uploaded file: %w", errWrite)
	}

	copiedBytes, errCopy := io.Copy(tempFile, limitedReader)
	if errCopy != nil {
		return &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("failed to read uploaded file: %v", errCopy)}
	}

	totalBytes := int64(sniffedBytes) + copiedBytes
	if totalBytes > audioTranscriptionUploadLimitBytes || limitedReader.N == 0 {
		return &audioRequestError{
			status: http.StatusBadRequest,
			msg:    fmt.Sprintf("uploaded file exceeds %d byte limit", audioTranscriptionUploadLimitBytes),
		}
	}

	if errClose := tempFile.Close(); errClose != nil {
		return fmt.Errorf("failed to finalize staged audio upload: %w", errClose)
	}

	r.StagedFilePath = tempPath
	r.FileContentType = resolvedContentType
	keepTempFile = true
	return nil
}

func validateAudioFile(fileName string, contentTypes ...string) error {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(fileName)))
	if _, ok := supportedAudioFileExtensions[ext]; ok {
		return nil
	}
	for _, contentType := range contentTypes {
		mediaType := strings.ToLower(strings.TrimSpace(contentType))
		if mediaType == "" {
			continue
		}
		if parsedMediaType, _, err := mime.ParseMediaType(mediaType); err == nil {
			mediaType = strings.ToLower(strings.TrimSpace(parsedMediaType))
		}
		if _, ok := supportedAudioMediaTypes[mediaType]; ok {
			return nil
		}
	}
	return &audioRequestError{
		status: http.StatusBadRequest,
		msg:    "unsupported audio format; supported formats are flac, m4a, mp3, mp4, mpeg, mpga, ogg, wav, and webm",
	}
}

func (r *audioTranscriptionRequest) Cleanup() error {
	if r == nil || r.StagedFilePath == "" {
		return nil
	}
	err := os.Remove(r.StagedFilePath)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		r.StagedFilePath = ""
		return nil
	}
	return err
}

func (r *audioTranscriptionRequest) BuildHTTPRequest(ctx context.Context, auth *coreauth.Auth, upstreamModel string) (*http.Request, error) {
	if r == nil {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "audio transcription request is empty"}
	}
	if strings.TrimSpace(r.StagedFilePath) == "" {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "audio transcription file is not staged"}
	}

	targetURL, err := resolveAudioTranscriptionURL(auth)
	if err != nil {
		return nil, err
	}

	bodyReader, bodyWriter := io.Pipe()
	multipartWriter := multipart.NewWriter(bodyWriter)
	contentLength, err := r.multipartContentLength(multipartWriter.Boundary(), upstreamModel)
	if err != nil {
		_ = bodyWriter.Close()
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bodyReader)
	if err != nil {
		_ = bodyWriter.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	if accept := audioTranscriptionAcceptHeader(r.Stream, r.ResponseFormat); accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.ContentLength = contentLength

	go r.writeMultipartBody(bodyWriter, multipartWriter, upstreamModel)
	return req, nil
}

func (r *audioTranscriptionRequest) writeMultipartBody(bodyWriter *io.PipeWriter, multipartWriter *multipart.Writer, upstreamModel string) {
	if err := r.writeMultipartFields(multipartWriter, upstreamModel); err != nil {
		_ = bodyWriter.CloseWithError(err)
		return
	}
	if err := multipartWriter.Close(); err != nil {
		_ = bodyWriter.CloseWithError(err)
		return
	}
	_ = bodyWriter.Close()
}

func (r *audioTranscriptionRequest) writeMultipartFields(multipartWriter *multipart.Writer, upstreamModel string) error {
	modelValue := normalizeAudioUpstreamModel(upstreamModel, r.Model)
	for _, field := range r.Fields {
		fieldValue := field.Value
		if field.Name == audioTranscriptionModelFieldName {
			fieldValue = modelValue
		}
		if err := multipartWriter.WriteField(field.Name, fieldValue); err != nil {
			return err
		}
	}

	filename := strings.TrimSpace(r.FileName)
	if filename == "" {
		filename = audioTranscriptionDefaultFilename
	}

	filePartHeader := make(textproto.MIMEHeader)
	filePartHeader.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name":     audioTranscriptionFileFieldName,
		"filename": filename,
	}))
	if contentType := strings.TrimSpace(r.FileContentType); contentType != "" {
		filePartHeader.Set("Content-Type", contentType)
	}

	filePart, err := multipartWriter.CreatePart(filePartHeader)
	if err != nil {
		return err
	}

	stagedFile, err := os.Open(r.StagedFilePath)
	if err != nil {
		return fmt.Errorf("failed to open staged audio file: %w", err)
	}
	defer func() {
		_ = stagedFile.Close()
	}()

	_, err = io.Copy(filePart, stagedFile)
	return err
}

func (r *audioTranscriptionRequest) multipartContentLength(boundary, upstreamModel string) (int64, error) {
	if r == nil {
		return 0, &audioRequestError{status: http.StatusBadRequest, msg: "audio transcription request is empty"}
	}
	fileInfo, err := os.Stat(r.StagedFilePath)
	if err != nil {
		return 0, fmt.Errorf("failed to stat staged audio file: %w", err)
	}
	counter := &countingWriter{}
	multipartWriter := multipart.NewWriter(counter)
	if err := multipartWriter.SetBoundary(boundary); err != nil {
		return 0, err
	}
	modelValue := normalizeAudioUpstreamModel(upstreamModel, r.Model)
	for _, field := range r.Fields {
		fieldValue := field.Value
		if field.Name == audioTranscriptionModelFieldName {
			fieldValue = modelValue
		}
		if err := multipartWriter.WriteField(field.Name, fieldValue); err != nil {
			return 0, err
		}
	}

	filename := strings.TrimSpace(r.FileName)
	if filename == "" {
		filename = audioTranscriptionDefaultFilename
	}
	filePartHeader := make(textproto.MIMEHeader)
	filePartHeader.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name":     audioTranscriptionFileFieldName,
		"filename": filename,
	}))
	if contentType := strings.TrimSpace(r.FileContentType); contentType != "" {
		filePartHeader.Set("Content-Type", contentType)
	}
	if _, err := multipartWriter.CreatePart(filePartHeader); err != nil {
		return 0, err
	}
	counter.n += fileInfo.Size()
	if err := multipartWriter.Close(); err != nil {
		return 0, err
	}
	return counter.n, nil
}

func normalizeAudioUpstreamModel(upstreamModel, fallbackModel string) string {
	modelValue := strings.TrimSpace(upstreamModel)
	if modelValue == "" {
		modelValue = strings.TrimSpace(fallbackModel)
	}
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelValue).ModelName)
	if baseModel != "" {
		return baseModel
	}
	return modelValue
}

func audioTranscriptionAcceptHeader(stream bool, responseFormat string) string {
	if stream {
		return "text/event-stream"
	}
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "", "json", "verbose_json":
	default:
		return ""
	}
	return "application/json"
}

func resolveAudioTranscriptionURL(auth *coreauth.Auth) (string, error) {
	if auth == nil {
		return "", &audioRequestError{status: http.StatusBadGateway, msg: "no auth selected for audio transcription"}
	}
	if isOpenAICompatibleAuth(auth) {
		baseURL := ""
		if auth.Attributes != nil {
			baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		}
		if baseURL == "" {
			return "", &audioRequestError{status: http.StatusBadGateway, msg: "selected OpenAI-compatible auth is missing base_url"}
		}
		return strings.TrimSuffix(baseURL, "/") + openAIAudioTranscriptionsPath, nil
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		if auth.Attributes != nil {
			if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
				return resolveCodexAudioTranscriptionURL(baseURL), nil
			}
		}
		return defaultCodexAudioTranscriptionURL, nil
	}
	return "", &audioRequestError{
		status: http.StatusNotImplemented,
		msg:    fmt.Sprintf("audio transcription is not supported for provider %q", strings.TrimSpace(auth.Provider)),
	}
}

func isOpenAICompatibleAuth(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes != nil {
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			return true
		}
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility")
}

func resolveAudioAutoModel(manager *coreauth.Manager, modelName string) (string, error) {
	model, _, err := resolveAudioAutoSelection(manager, modelName)
	return model, err
}

func resolveAudioAutoSelection(manager *coreauth.Manager, modelName string) (string, string, error) {
	parsed := thinking.ParseSuffix(strings.TrimSpace(modelName))
	if parsed.ModelName != "auto" {
		return modelName, "", nil
	}
	selection, err := resolveAutoAudioModelBase(manager)
	if err != nil {
		return "", "", &audioRequestError{status: http.StatusBadRequest, msg: err.Error()}
	}
	if parsed.HasSuffix {
		return fmt.Sprintf("%s(%s)", selection.RouteModel, parsed.RawSuffix), selection.AuthID, nil
	}
	return selection.RouteModel, selection.AuthID, nil
}

type audioAutoModelCandidate struct {
	RouteModel string
	CreatedAt  int64
	AuthID     string
}

func resolveAutoAudioModelBase(manager *coreauth.Manager) (audioAutoModelCandidate, error) {
	if manager == nil {
		return audioAutoModelCandidate{}, fmt.Errorf("model auto is not supported for audio transcription because the auth manager is unavailable")
	}

	candidates := make([]audioAutoModelCandidate, 0)
	now := time.Now()

	for _, preview := range manager.PreviewSelectableRouteModels(now, supportsAudioTranscriptionAuth) {
		if !audioPreviewSupportsTranscription(preview) {
			continue
		}
		authID := ""
		if preview.Auth != nil {
			authID = strings.TrimSpace(preview.Auth.ID)
		}
		candidates = append(candidates, audioAutoModelCandidate{
			RouteModel: preview.RouteModel,
			CreatedAt:  preview.CreatedAt,
			AuthID:     authID,
		})
	}

	if len(candidates) == 0 {
		return audioAutoModelCandidate{}, fmt.Errorf("model auto is not supported for audio transcription because no transcription-capable model is available")
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt == candidates[j].CreatedAt {
			if candidates[i].RouteModel == candidates[j].RouteModel {
				return candidates[i].AuthID < candidates[j].AuthID
			}
			return candidates[i].RouteModel < candidates[j].RouteModel
		}
		return candidates[i].CreatedAt > candidates[j].CreatedAt
	})
	return candidates[0], nil
}

func supportsAudioTranscriptionAuth(auth *coreauth.Auth) bool {
	_, err := resolveAudioTranscriptionURL(auth)
	return err == nil
}

func audioPreviewSupportsTranscription(preview coreauth.RouteModelPreview) bool {
	if len(preview.UpstreamModels) == 0 {
		return false
	}
	for _, upstreamModel := range preview.UpstreamModels {
		if !isAudioTranscriptionModel(preview.Auth, upstreamModel) {
			return false
		}
	}
	return true
}

func isAudioTranscriptionModel(auth *coreauth.Auth, modelName string) bool {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		return false
	}
	provider := ""
	if auth != nil {
		provider = strings.TrimSpace(auth.Provider)
	}
	if info := registry.LookupModelInfo(baseModel, provider); info != nil {
		return isAudioTranscriptionModelInfo(info)
	}
	if info := registry.LookupModelInfo(baseModel); info != nil {
		return isAudioTranscriptionModelInfo(info)
	}
	return isAudioTranscriptionModelName(baseModel)
}

func isAudioTranscriptionModelInfo(info *registry.ModelInfo) bool {
	if info == nil {
		return false
	}
	if len(info.SupportedInputModalities) > 0 || len(info.SupportedOutputModalities) > 0 {
		supportsAudioInput := len(info.SupportedInputModalities) == 0 || containsFolded(info.SupportedInputModalities, "audio")
		supportsTextOutput := len(info.SupportedOutputModalities) == 0 || containsFolded(info.SupportedOutputModalities, "text")
		return supportsAudioInput && supportsTextOutput
	}
	return isAudioTranscriptionModelName(info.ID) ||
		isAudioTranscriptionModelName(info.Version) ||
		isAudioTranscriptionModelName(info.DisplayName) ||
		isAudioTranscriptionModelName(info.Description)
}

func isAudioTranscriptionModelName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	return strings.Contains(value, "transcribe") ||
		strings.Contains(value, "transcription") ||
		strings.Contains(value, "speech-to-text") ||
		strings.Contains(value, "whisper")
}

func containsFolded(values []string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), needle) {
			return true
		}
	}
	return false
}

func resolveCodexAudioTranscriptionURL(baseURL string) string {
	trimmedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	// Configured Codex base URLs are expected to follow the same backend-api root
	// convention as the existing Codex HTTP endpoints, where /codex and /transcribe
	// are sibling paths under the same root.
	if strings.HasSuffix(trimmedBaseURL, "/codex") {
		return strings.TrimSuffix(trimmedBaseURL, "/codex") + "/transcribe"
	}
	return trimmedBaseURL + "/transcribe"
}

func (r *audioTranscriptionRequest) PreserveRawResponse() bool {
	if r == nil {
		return false
	}
	responseFormat := strings.ToLower(strings.TrimSpace(r.ResponseFormat))
	switch responseFormat {
	case "", "json", "verbose_json":
		return false
	default:
		return true
	}
}

func (r *audioTranscriptionRequest) ShouldStreamResponse(headers ...http.Header) bool {
	if r != nil && r.Stream {
		return true
	}
	return audioTranscriptionIsEventStream(headers...)
}

func audioTranscriptionContentType(headers ...http.Header) string {
	for _, hdr := range headers {
		if hdr == nil {
			continue
		}
		if contentType := strings.TrimSpace(hdr.Get("Content-Type")); contentType != "" {
			return contentType
		}
	}
	return ""
}

func audioTranscriptionIsEventStream(headers ...http.Header) bool {
	mediaType := audioTranscriptionContentType(headers...)
	if mediaType == "" {
		return false
	}
	parsedMediaType, _, err := mime.ParseMediaType(mediaType)
	if err == nil {
		mediaType = parsedMediaType
	}
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}

func normalizeAudioTranscriptionResponseFromReader(body io.Reader) (string, error) {
	stagedPath, err := stageAudioTranscriptionResponse(body)
	if err != nil {
		return "", err
	}
	if stagedPath != "" {
		defer func() {
			_ = os.Remove(stagedPath)
		}()
	}

	normalizedFile, err := os.CreateTemp("", audioTranscriptionResponseTempFilePattern)
	if err != nil {
		return "", fmt.Errorf("failed to create normalized transcription response temp file: %w", err)
	}
	normalizedPath := normalizedFile.Name()
	keepNormalizedFile := false
	defer func() {
		_ = normalizedFile.Close()
		if !keepNormalizedFile {
			_ = os.Remove(normalizedPath)
		}
	}()

	switch kind, errKind := detectAudioTranscriptionResponseKind(stagedPath); {
	case errKind != nil:
		return "", errKind
	case kind == audioTranscriptionResponseEmpty:
		if _, err := normalizedFile.Write([]byte(`{"text":""}`)); err != nil {
			return "", fmt.Errorf("failed to write normalized empty transcription response: %w", err)
		}
	case kind == audioTranscriptionResponseJSONObject:
		hasText, err := audioTranscriptionObjectHasText(stagedPath)
		if err != nil {
			return "", err
		}
		sourceFile, err := os.Open(stagedPath)
		if err != nil {
			return "", fmt.Errorf("failed to open staged upstream transcription response: %w", err)
		}
		if err := writeNormalizedAudioTranscriptionObject(normalizedFile, sourceFile, !hasText); err != nil {
			_ = sourceFile.Close()
			return "", err
		}
		_ = sourceFile.Close()
	case kind == audioTranscriptionResponseJSONString:
		sourceFile, err := os.Open(stagedPath)
		if err != nil {
			return "", fmt.Errorf("failed to open staged upstream transcription response: %w", err)
		}
		text, err := readAudioTranscriptionJSONString(sourceFile)
		_ = sourceFile.Close()
		if err != nil {
			return "", err
		}
		payload, err := json.Marshal(map[string]string{"text": text})
		if err != nil {
			return "", fmt.Errorf("failed to normalize upstream transcription string response: %w", err)
		}
		if _, err := normalizedFile.Write(payload); err != nil {
			return "", fmt.Errorf("failed to write normalized transcription string response: %w", err)
		}
	default:
		sourceFile, err := os.Open(stagedPath)
		if err != nil {
			return "", fmt.Errorf("failed to open staged upstream transcription response: %w", err)
		}
		if err := writeWrappedAudioTranscriptionText(normalizedFile, sourceFile); err != nil {
			_ = sourceFile.Close()
			return "", err
		}
		_ = sourceFile.Close()
	}

	if err := normalizedFile.Close(); err != nil {
		return "", fmt.Errorf("failed to finalize normalized transcription response: %w", err)
	}
	keepNormalizedFile = true
	return normalizedPath, nil
}

func stageAudioTranscriptionResponse(body io.Reader) (string, error) {
	if body == nil {
		return "", nil
	}
	tempFile, err := os.CreateTemp("", audioTranscriptionResponseTempFilePattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for transcription response: %w", err)
	}
	tempPath := tempFile.Name()
	keepTempFile := false
	defer func() {
		_ = tempFile.Close()
		if !keepTempFile {
			_ = os.Remove(tempPath)
		}
	}()
	if _, errCopy := io.Copy(tempFile, body); errCopy != nil {
		return "", fmt.Errorf("failed to stage upstream transcription response: %w", errCopy)
	}
	if errClose := tempFile.Close(); errClose != nil {
		return "", fmt.Errorf("failed to finalize staged transcription response: %w", errClose)
	}
	keepTempFile = true
	return tempPath, nil
}

type audioTranscriptionResponseKind int

const (
	audioTranscriptionResponseEmpty audioTranscriptionResponseKind = iota
	audioTranscriptionResponseJSONObject
	audioTranscriptionResponseJSONString
	audioTranscriptionResponseText
)

func detectAudioTranscriptionResponseKind(path string) (audioTranscriptionResponseKind, error) {
	if strings.TrimSpace(path) == "" {
		return audioTranscriptionResponseEmpty, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return audioTranscriptionResponseEmpty, fmt.Errorf("failed to open staged upstream transcription response: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	reader := bufio.NewReader(file)
	for {
		r, _, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			return audioTranscriptionResponseEmpty, nil
		}
		if err != nil {
			return audioTranscriptionResponseEmpty, fmt.Errorf("failed to inspect staged upstream transcription response: %w", err)
		}
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '{':
			return audioTranscriptionResponseJSONObject, nil
		case '"':
			return audioTranscriptionResponseJSONString, nil
		default:
			return audioTranscriptionResponseText, nil
		}
	}
}

func audioTranscriptionObjectHasText(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("failed to open staged upstream transcription response: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	decoder := json.NewDecoder(file)
	tok, err := decoder.Token()
	if err != nil {
		return false, fmt.Errorf("failed to parse upstream transcription object: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return false, fmt.Errorf("upstream transcription response is not a JSON object")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return false, fmt.Errorf("failed to parse upstream transcription object key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return false, fmt.Errorf("upstream transcription object contains a non-string key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return false, fmt.Errorf("failed to parse upstream transcription object field %q: %w", key, err)
		}
		if key == "text" {
			return true, nil
		}
	}
	if _, err := decoder.Token(); err != nil {
		return false, fmt.Errorf("failed to parse upstream transcription object end: %w", err)
	}
	return false, nil
}

func writeNormalizedAudioTranscriptionObject(dst io.Writer, src io.Reader, injectText bool) error {
	if dst == nil {
		return fmt.Errorf("missing normalized transcription writer")
	}
	if src == nil {
		_, err := io.WriteString(dst, `{"text":""}`)
		return err
	}
	decoder := json.NewDecoder(src)
	tok, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("failed to parse upstream transcription object: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("upstream transcription response is not a JSON object")
	}
	if _, err := io.WriteString(dst, "{"); err != nil {
		return err
	}
	wroteField := false
	if injectText {
		if _, err := io.WriteString(dst, `"text":""`); err != nil {
			return err
		}
		wroteField = true
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("failed to parse upstream transcription object key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("upstream transcription object contains a non-string key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("failed to parse upstream transcription object field %q: %w", key, err)
		}
		if wroteField {
			if _, err := io.WriteString(dst, ","); err != nil {
				return err
			}
		}
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return fmt.Errorf("failed to encode transcription object key %q: %w", key, err)
		}
		if _, err := dst.Write(keyJSON); err != nil {
			return err
		}
		if _, err := io.WriteString(dst, ":"); err != nil {
			return err
		}
		if _, err := dst.Write(raw); err != nil {
			return err
		}
		wroteField = true
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("failed to parse upstream transcription object end: %w", err)
	}
	_, err = io.WriteString(dst, "}")
	return err
}

func readAudioTranscriptionJSONString(src io.Reader) (string, error) {
	if src == nil {
		return "", nil
	}
	var text string
	if err := json.NewDecoder(src).Decode(&text); err != nil {
		return "", fmt.Errorf("failed to decode upstream transcription string response: %w", err)
	}
	return text, nil
}

func writeWrappedAudioTranscriptionText(dst io.Writer, src io.Reader) error {
	if dst == nil {
		return fmt.Errorf("missing normalized transcription writer")
	}
	if _, err := io.WriteString(dst, `{"text":"`); err != nil {
		return err
	}
	if src != nil {
		reader := bufio.NewReader(src)
		for {
			r, size, err := reader.ReadRune()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to read staged transcription text response: %w", err)
			}
			if r == utf8.RuneError && size == 1 {
				if _, err := io.WriteString(dst, `\ufffd`); err != nil {
					return err
				}
				continue
			}
			switch r {
			case '\\', '"':
				if _, err := io.WriteString(dst, `\`+string(r)); err != nil {
					return err
				}
			case '\b':
				if _, err := io.WriteString(dst, `\b`); err != nil {
					return err
				}
			case '\f':
				if _, err := io.WriteString(dst, `\f`); err != nil {
					return err
				}
			case '\n':
				if _, err := io.WriteString(dst, `\n`); err != nil {
					return err
				}
			case '\r':
				if _, err := io.WriteString(dst, `\r`); err != nil {
					return err
				}
			case '\t':
				if _, err := io.WriteString(dst, `\t`); err != nil {
					return err
				}
			default:
				if r < 0x20 {
					if _, err := fmt.Fprintf(dst, "\\u%04x", r); err != nil {
						return err
					}
					continue
				}
				var encoded [utf8.UTFMax]byte
				n := utf8.EncodeRune(encoded[:], r)
				if _, err := dst.Write(encoded[:n]); err != nil {
					return err
				}
			}
		}
	}
	_, err := io.WriteString(dst, `"}`)
	return err
}

func writeStagedAudioTranscriptionResponse(dst io.Writer, path string) error {
	if dst == nil {
		return fmt.Errorf("missing transcription response writer")
	}
	if strings.TrimSpace(path) == "" {
		_, err := io.WriteString(dst, `{"text":""}`)
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open normalized transcription response: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	_, err = io.Copy(dst, file)
	return err
}

func normalizeAudioTranscriptionResponse(body []byte) []byte {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return []byte(`{"text":""}`)
	}
	trimmedBytes := []byte(trimmed)
	if json.Valid(trimmedBytes) && trimmedBytes[0] == '{' {
		textValue := gjson.GetBytes(trimmedBytes, "text")
		if textValue.Exists() && textValue.Type != gjson.Null {
			return trimmedBytes
		}
		updated, err := sjson.SetBytes(trimmedBytes, "text", "")
		if err == nil {
			return updated
		}
	}

	text := trimmed
	var decoded string
	if err := json.Unmarshal(trimmedBytes, &decoded); err == nil {
		text = decoded
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return []byte(`{"text":""}`)
	}
	return payload
}

func statusCodeOrDefault(err error, fallback int) int {
	if err == nil {
		return fallback
	}
	if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
		if code := se.StatusCode(); code > 0 {
			return code
		}
	}
	return fallback
}

type countingWriter struct {
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}
