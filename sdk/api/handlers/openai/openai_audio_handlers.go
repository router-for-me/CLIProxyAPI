package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
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

const audioTranscriptionFormMemory = 32 << 20

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
	FileData        []byte
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
		status := http.StatusBadRequest
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		c.JSON(status, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: err.Error(),
				Type:    "invalid_request_error",
			},
		})
		return
	}

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

	body, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      fmt.Errorf("failed to read upstream transcription response: %w", err),
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
	if err := c.Request.ParseMultipartForm(audioTranscriptionFormMemory); err != nil {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("invalid multipart form: %v", err)}
	}
	if c.Request.MultipartForm == nil {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "multipart form is empty"}
	}
	defer c.Request.MultipartForm.RemoveAll()

	model := strings.TrimSpace(c.PostForm("model"))
	if model == "" {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "missing required field: model"}
	}

	files := c.Request.MultipartForm.File["file"]
	if len(files) == 0 {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "missing required field: file"}
	}
	if len(files) > 1 {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "only one file upload is supported"}
	}

	fileHeader := files[0]
	file, err := fileHeader.Open()
	if err != nil {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("failed to open uploaded file: %v", err)}
	}
	defer file.Close()

	fileData, err := io.ReadAll(file)
	if err != nil {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: fmt.Sprintf("failed to read uploaded file: %v", err)}
	}
	if len(fileData) == 0 {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "uploaded file is empty"}
	}

	fileContentType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if fileContentType == "" {
		fileContentType = http.DetectContentType(fileData)
	}
	if err := validateAudioFile(fileHeader.Filename, fileContentType); err != nil {
		return nil, err
	}

	fieldNames := make([]string, 0, len(c.Request.MultipartForm.Value))
	for name := range c.Request.MultipartForm.Value {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	fields := make([]audioFormField, 0, len(fieldNames))
	for _, name := range fieldNames {
		values := c.Request.MultipartForm.Value[name]
		for _, value := range values {
			fields = append(fields, audioFormField{Name: name, Value: value})
		}
	}

	return &audioTranscriptionRequest{
		Model:           model,
		Fields:          fields,
		FileName:        fileHeader.Filename,
		FileContentType: fileContentType,
		FileData:        append([]byte(nil), fileData...),
	}, nil
}

func validateAudioFile(fileName, contentType string) error {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(fileName)))
	if _, ok := supportedAudioFileExtensions[ext]; ok {
		return nil
	}
	if contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err == nil {
			if _, ok := supportedAudioMediaTypes[strings.ToLower(strings.TrimSpace(mediaType))]; ok {
				return nil
			}
		}
	}
	return &audioRequestError{
		status: http.StatusBadRequest,
		msg:    "unsupported audio format; supported formats are flac, m4a, mp3, mp4, mpeg, mpga, ogg, wav, and webm",
	}
}

func (r *audioTranscriptionRequest) BuildHTTPRequest(ctx context.Context, auth *coreauth.Auth) (*http.Request, error) {
	if r == nil {
		return nil, &audioRequestError{status: http.StatusBadRequest, msg: "audio transcription request is empty"}
	}
	targetURL, err := resolveAudioTranscriptionURL(auth)
	if err != nil {
		return nil, err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range r.Fields {
		if err := writer.WriteField(field.Name, field.Value); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}

	filename := strings.TrimSpace(r.FileName)
	if filename == "" {
		filename = "audio.webm"
	}
	partHeader := make(textproto.MIMEHeader)
	contentDisposition := mime.FormatMediaType("form-data", map[string]string{
		"name":     "file",
		"filename": filename,
	})
	partHeader.Set("Content-Disposition", contentDisposition)
	if contentType := strings.TrimSpace(r.FileContentType); contentType != "" {
		partHeader.Set("Content-Type", contentType)
	}
	filePart, err := writer.CreatePart(partHeader)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	if _, err = filePart.Write(r.FileData); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	return req, nil
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
		return strings.TrimSuffix(baseURL, "/") + "/audio/transcriptions", nil
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		if auth.Attributes != nil {
			if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
				return strings.TrimSuffix(baseURL, "/") + "/audio/transcriptions", nil
			}
		}
		return "https://chatgpt.com/backend-api/transcribe", nil
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

func normalizeAudioTranscriptionResponse(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []byte(`{"text":""}`)
	}
	if json.Valid(trimmed) && len(trimmed) > 0 && trimmed[0] == '{' {
		textValue := gjson.GetBytes(trimmed, "text")
		if textValue.Exists() && textValue.Type != gjson.Null {
			return trimmed
		}
		updated, err := sjson.SetBytes(trimmed, "text", "")
		if err == nil {
			return updated
		}
	}

	text := string(trimmed)
	var decoded string
	if err := json.Unmarshal(trimmed, &decoded); err == nil {
		text = decoded
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return []byte(`{"text":""}`)
	}
	return payload
}
