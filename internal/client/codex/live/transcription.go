package live

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	// TranscriptionModel is the backend-selected speech alias exposed to clients.
	TranscriptionModel       = "codex-transcribe"
	transcriptionModel       = TranscriptionModel
	transcriptionUserAgent   = "codex-tui/0.154.0 (Windows 11; x86_64)"
	transcriptionUploadLimit = 26 << 20
	transcriptionReplyLimit  = 4 << 20
	transcriptionFieldLimit  = 64 << 10
	transcriptionURL         = "https://chatgpt.com/backend-api/transcribe"
)

type transcriptionRequest struct {
	body          []byte
	contentType   string
	format        string
	ignoredPrompt bool
}

// HandleTranscription adapts file transcription to the ChatGPT OAuth backend.
// ChatGPT chooses the actual speech model; the client model is a compatibility alias.
func (h *Handler) HandleTranscription(c *gin.Context) {
	upload, errParse := parseTranscription(c)
	if errParse != nil {
		status := http.StatusBadRequest
		var oversized *http.MaxBytesError
		if errors.As(errParse, &oversized) {
			status = http.StatusRequestEntityTooLarge
		}
		writeTranscriptionError(c, status, "invalid_transcription_request", errParse.Error())
		return
	}
	if upload.ignoredPrompt {
		c.Header("X-CLIProxy-Transcription-Prompt", "ignored")
	}
	if h == nil || h.authManager == nil {
		writeTranscriptionError(c, http.StatusServiceUnavailable, "transcription_unavailable", "Codex OAuth authentication is unavailable")
		return
	}
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	opts := coreexecutor.Options{Headers: liveSelectionHeaders(c)}
	ctx = handlers.EnrichContextWithSessionHierarchy(ctx, opts.Headers, nil, nil)
	selection, selected, errSelect := h.selectOAuth(ctx, transcriptionModel, opts)
	if errSelect != nil {
		for _, value := range auth.SafeResponseHeaders(errSelect).Values("Retry-After") {
			c.Writer.Header().Add("Retry-After", value)
		}
		writeTranscriptionError(c, clienterror.HTTPStatusFromErrorOr(errSelect, http.StatusServiceUnavailable), "transcription_auth_unavailable", "No Codex OAuth credential is currently available")
		return
	}
	if selection != nil {
		defer selection.End("transcription_finished")
		attemptCtx, releaseAttempt, errAttempt := selection.AttemptContext(ctx)
		if errAttempt != nil {
			writeTranscriptionError(c, http.StatusServiceUnavailable, "transcription_unavailable", "Unable to acquire a Codex OAuth execution slot")
			return
		}
		ctx = attemptCtx
		defer releaseAttempt()
	}
	if selected == nil {
		writeTranscriptionError(c, http.StatusServiceUnavailable, "transcription_auth_unavailable", "No Codex OAuth credential is available")
		return
	}
	logging.SetGinCPATraceID(c, selected.EnsureIndex())
	headers := protocolHeaders(c.Request.Header)
	headers.Set("Content-Type", upload.contentType)
	headers.Set("Accept", "application/json")
	headers.Set("User-Agent", transcriptionUserAgent)
	setAccountHeader(headers, selected)
	request, errRequest := h.authManager.NewHttpRequest(ctx, selected, http.MethodPost, transcriptionUpstreamURL(selected.Attributes["base_url"]), upload.body, headers)
	if errRequest != nil {
		writeTranscriptionError(c, http.StatusBadGateway, "transcription_upstream_error", "Unable to prepare the transcription request")
		return
	}
	runtimeConfig := h.currentConfig()
	// Do not retain audio bytes in request diagnostics or execution metadata.
	logHeaders := headersForLogging(request.Header)
	logHeaders.Del("Authorization")
	logHeaders.Del("Cookie")
	helps.RecordAPIRequest(ctx, runtimeConfig, helps.UpstreamRequestLog{
		URL: request.URL.String(), Method: http.MethodPost, Headers: logHeaders,
		Provider: "codex", AuthID: selected.ID, AuthType: "oauth",
	})
	response, errRequest := h.authManager.HttpRequest(ctx, selected, request)
	if errRequest != nil {
		log.WithField("error", logging.SafeErrorDiagnostic(errRequest)).Warn("Codex transcription transport failed")
		writeTranscriptionError(c, http.StatusBadGateway, "transcription_upstream_error", "Unable to reach the ChatGPT transcription backend")
		return
	}
	closeResponse := sync.OnceValue(response.Body.Close)
	defer func() {
		if errClose := closeResponse(); errClose != nil {
			log.WithField("error", logging.SafeErrorDiagnostic(errClose)).Warn("Unable to close transcription response")
		}
	}()
	if selection != nil {
		if errBind := selection.Bind(closeResponse); errBind != nil {
			writeTranscriptionError(c, http.StatusServiceUnavailable, "transcription_unavailable", "Transcription execution was canceled")
			return
		}
	}
	for _, name := range []string{"Retry-After", "X-Request-Id", "OpenAI-Request-Id"} {
		for _, value := range response.Header.Values(name) {
			c.Writer.Header().Add(name, value)
		}
	}
	logResponseHeaders := make(http.Header)
	for _, name := range []string{"Content-Type", "Retry-After", "X-Request-Id", "OpenAI-Request-Id"} {
		logResponseHeaders[name] = response.Header.Values(name)
	}
	helps.RecordAPIResponseMetadata(ctx, runtimeConfig, response.StatusCode, logResponseHeaders)
	if response.StatusCode == http.StatusUnauthorized && selection != nil {
		h.authManager.ReportHomeUnauthorized(ctx, selected, "codex", transcriptionModel)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Never expose an upstream HTML challenge, cookies, or credential-bearing errors.
		writeTranscriptionError(c, response.StatusCode, "transcription_upstream_error", fmt.Sprintf("ChatGPT transcription failed (HTTP %d)", response.StatusCode))
		return
	}
	payload, errRead := io.ReadAll(io.LimitReader(response.Body, transcriptionReplyLimit+1))
	if errRead != nil || len(payload) > transcriptionReplyLimit {
		writeTranscriptionError(c, http.StatusBadGateway, "invalid_transcription_response", "Unable to read the transcription response")
		return
	}
	var transcript struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(payload, &transcript) != nil || transcript.Text == nil {
		writeTranscriptionError(c, http.StatusBadGateway, "invalid_transcription_response", "ChatGPT returned an invalid transcription response")
		return
	}
	if upload.format == "text" {
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(*transcript.Text))
	} else {
		c.JSON(http.StatusOK, transcript)
	}
}

func transcriptionUpstreamURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return transcriptionURL
	}
	if strings.HasSuffix(base, "/transcribe") {
		return base
	}
	return strings.TrimSuffix(base, "/codex") + "/transcribe"
}

func parseTranscription(c *gin.Context) (transcriptionRequest, error) {
	result := transcriptionRequest{format: "json"}
	if c.Request.ContentLength > transcriptionUploadLimit {
		return result, &http.MaxBytesError{Limit: transcriptionUploadLimit}
	}
	contentType, params, errMedia := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if errMedia != nil || contentType != "multipart/form-data" || params["boundary"] == "" {
		return result, errors.New("Expected multipart/form-data with an audio file")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, transcriptionUploadLimit)
	reader := multipart.NewReader(c.Request.Body, params["boundary"])
	fields := make(map[string]string)
	var audio []byte
	filename := ""
	for count := 0; ; count++ {
		part, errPart := reader.NextPart()
		if errPart == io.EOF {
			break
		}
		if errPart != nil {
			return result, errPart
		}
		if count >= 16 {
			return result, errors.New("Too many multipart fields")
		}
		name := part.FormName()
		if name == "" || len(name) > 64 {
			return result, errors.New("Invalid multipart field name")
		}
		if name == "file" {
			if audio != nil || part.FileName() == "" {
				return result, errors.New("Exactly one audio file is required")
			}
			filename = part.FileName()
			audio, errPart = io.ReadAll(part)
		} else {
			if _, exists := fields[name]; exists || part.FileName() != "" {
				return result, errors.New("Duplicate or invalid multipart field")
			}
			var value []byte
			value, errPart = io.ReadAll(io.LimitReader(part, transcriptionFieldLimit+1))
			if len(value) > transcriptionFieldLimit {
				return result, errors.New("Transcription field exceeds 64 KiB")
			}
			fields[name] = string(value)
		}
		if errPart != nil {
			return result, errPart
		}
	}
	if len(audio) == 0 {
		return result, errors.New("An audio file is required")
	}
	for name, value := range fields {
		switch name {
		case "model":
			switch strings.TrimSpace(value) {
			case transcriptionModel, "whisper-1", "gpt-transcribe", "gpt-4o-transcribe", "gpt-4o-mini-transcribe":
			default:
				return result, errors.New("Use model codex-transcribe; ChatGPT chooses the underlying speech model")
			}
		case "response_format":
			if value != "json" && value != "text" {
				return result, errors.New("ChatGPT OAuth transcription supports only json and text output")
			}
			result.format = value
		case "language":
			if len(value) > 128 {
				return result, errors.New("Invalid transcription language")
			}
		case "prompt":
			// OpenWhispr automatically sends dictionary hints, including its agent
			// name. This backend has no supported prompt parameter, so accept the
			// optional hint without forwarding or logging its contents.
			result.ignoredPrompt = strings.TrimSpace(value) != ""
		case "stream":
			if value != "false" {
				return result, errors.New("Streaming is not supported by ChatGPT OAuth file transcription")
			}
		default:
			return result, fmt.Errorf("Field %q is not supported by ChatGPT OAuth transcription", name)
		}
	}
	if strings.TrimSpace(fields["model"]) == "" {
		return result, errors.New("The model field is required; use codex-transcribe")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filePart, errFile := writer.CreateFormFile("file", filename)
	if errFile != nil {
		return result, errFile
	}
	if _, errWrite := filePart.Write(audio); errWrite != nil {
		return result, errWrite
	}
	if language := strings.TrimSpace(fields["language"]); language != "" {
		if errLanguage := writer.WriteField("language", language); errLanguage != nil {
			return result, errLanguage
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return result, errClose
	}
	result.body, result.contentType = body.Bytes(), writer.FormDataContentType()
	return result, nil
}

func writeTranscriptionError(c *gin.Context, status int, code, message string) {
	errorType := "api_error"
	if status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge {
		errorType = "invalid_request_error"
	}
	c.JSON(status, gin.H{"error": gin.H{"message": message, "type": errorType, "code": code}})
}
