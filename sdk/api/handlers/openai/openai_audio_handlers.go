package openai

import (
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
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	audioTranscriptionUploadLimitBytes           int64 = 32 << 20
	audioTranscriptionUpstreamResponseLimitBytes int64 = 8 << 20
	audioTranscriptionContentSniffBytes                = 512
	audioTranscriptionFileFieldName                    = "file"
	audioTranscriptionModelFieldName                   = "model"
	audioTranscriptionDefaultFilename                  = "audio.webm"
	audioTranscriptionTempFilePattern                  = "cliproxy-audio-transcription-*"
	openAIAudioTranscriptionsPath                      = "/audio/transcriptions"
	defaultCodexAudioTranscriptionURL                  = "https://chatgpt.com/backend-api/transcribe"
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

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	upstreamResp, _, errMsg := h.ExecuteHTTPRequestWithAuthManager(cliCtx, audioReq.Model, func(ctx context.Context, auth *coreauth.Auth) (*http.Request, error) {
		return audioReq.BuildHTTPRequest(ctx, auth)
	})
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	defer func() {
		_ = upstreamResp.Body.Close()
	}()

	body, err := readAudioTranscriptionUpstreamResponse(upstreamResp.Body)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{
			StatusCode: statusCodeOrDefault(err, http.StatusBadGateway),
			Error:      err,
		})
		cliCancel(err)
		return
	}

	normalizedBody := normalizeAudioTranscriptionResponse(body)
	if handlers.PassthroughHeadersEnabled(h.Cfg) {
		handlers.WriteUpstreamHeaders(c.Writer.Header(), handlers.FilterUpstreamHeaders(upstreamResp.Header))
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(normalizedBody)
	cliCancel(normalizedBody)
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
			fieldValue, errRead := io.ReadAll(part)
			_ = part.Close()
			if errRead != nil {
				return nil, &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("failed to read form field %q: %v", partName, errRead)}
			}
			field := audioFormField{Name: partName, Value: string(fieldValue)}
			audioReq.Fields = append(audioReq.Fields, field)
			if partName == audioTranscriptionModelFieldName && audioReq.Model == "" {
				audioReq.Model = strings.TrimSpace(field.Value)
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

func (r *audioTranscriptionRequest) BuildHTTPRequest(ctx context.Context, auth *coreauth.Auth) (*http.Request, error) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bodyReader)
	if err != nil {
		_ = bodyWriter.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	go r.writeMultipartBody(bodyWriter, multipartWriter)
	return req, nil
}

func (r *audioTranscriptionRequest) writeMultipartBody(bodyWriter *io.PipeWriter, multipartWriter *multipart.Writer) {
	if err := r.writeMultipartFields(multipartWriter); err != nil {
		_ = bodyWriter.CloseWithError(err)
		return
	}
	if err := multipartWriter.Close(); err != nil {
		_ = bodyWriter.CloseWithError(err)
		return
	}
	_ = bodyWriter.Close()
}

func (r *audioTranscriptionRequest) writeMultipartFields(multipartWriter *multipart.Writer) error {
	for _, field := range r.Fields {
		if err := multipartWriter.WriteField(field.Name, field.Value); err != nil {
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
				return strings.TrimSuffix(baseURL, "/") + openAIAudioTranscriptionsPath, nil
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

func readAudioTranscriptionUpstreamResponse(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	limitedReader := &io.LimitedReader{R: body, N: audioTranscriptionUpstreamResponseLimitBytes + 1}
	payload, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read upstream transcription response: %w", err)
	}
	if int64(len(payload)) > audioTranscriptionUpstreamResponseLimitBytes {
		return nil, &audioRequestError{
			status: http.StatusBadGateway,
			msg:    fmt.Sprintf("upstream transcription response exceeded %d byte limit", audioTranscriptionUpstreamResponseLimitBytes),
		}
	}
	return payload, nil
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
