package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type audioCaptureExecutor struct {
	lastURL      string
	lastAuthID   string
	lastFields   map[string][]string
	lastFileName string
	lastFileBody []byte
	calls        int
	responseBody string
	contentType  string
}

func (e *audioCaptureExecutor) Identifier() string { return "openai-compatibility" }

func (e *audioCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *audioCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *audioCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *audioCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *audioCaptureExecutor) HttpRequest(_ context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	e.calls++
	e.lastAuthID = auth.ID
	e.lastURL = req.URL.String()
	e.lastFields = make(map[string][]string)

	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	if mediaType != "multipart/form-data" {
		return nil, errors.New("unexpected content type")
	}
	reader := multipart.NewReader(req.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		payload, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		if part.FormName() == "file" {
			e.lastFileName = part.FileName()
			e.lastFileBody = payload
			continue
		}
		e.lastFields[part.FormName()] = append(e.lastFields[part.FormName()], string(payload))
	}

	contentType := e.contentType
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	responseBody := e.responseBody
	if responseBody == "" {
		responseBody = "transcribed text"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(responseBody)),
	}, nil
}

func TestAudioTranscriptionsWrapsPlainTextResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gpt-4o-mini-transcribe"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/audio/transcriptions", h.AudioTranscriptions)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("model", "gpt-4o-mini-transcribe"); err != nil {
		t.Fatalf("WriteField(model): %v", err)
	}
	if err := writer.WriteField("prompt", "caption this"); err != nil {
		t.Fatalf("WriteField(prompt): %v", err)
	}
	if err := writer.WriteField("language", "en"); err != nil {
		t.Fatalf("WriteField(language): %v", err)
	}
	filePart, err := writer.CreateFormFile("file", "sample.webm")
	if err != nil {
		t.Fatalf("CreateFormFile(): %v", err)
	}
	if _, err := filePart.Write([]byte("fake-audio")); err != nil {
		t.Fatalf("Write(file): %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if strings.TrimSpace(resp.Body.String()) != `{"text":"transcribed text"}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.lastAuthID != auth.ID {
		t.Fatalf("last auth id = %q, want %q", executor.lastAuthID, auth.ID)
	}
	if executor.lastURL != "https://api.example.com/v1/audio/transcriptions" {
		t.Fatalf("last URL = %q, want %q", executor.lastURL, "https://api.example.com/v1/audio/transcriptions")
	}
	if got := executor.lastFields["model"]; len(got) != 1 || got[0] != "gpt-4o-mini-transcribe" {
		t.Fatalf("model field = %v", got)
	}
	if got := executor.lastFields["prompt"]; len(got) != 1 || got[0] != "caption this" {
		t.Fatalf("prompt field = %v", got)
	}
	if got := executor.lastFields["language"]; len(got) != 1 || got[0] != "en" {
		t.Fatalf("language field = %v", got)
	}
	if executor.lastFileName != "sample.webm" {
		t.Fatalf("file name = %q, want %q", executor.lastFileName, "sample.webm")
	}
	if string(executor.lastFileBody) != "fake-audio" {
		t.Fatalf("file body = %q, want %q", string(executor.lastFileBody), "fake-audio")
	}
}

func TestAudioTranscriptionsRejectsUnsupportedFileFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/audio/transcriptions", h.AudioTranscriptions)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("model", "gpt-4o-mini-transcribe"); err != nil {
		t.Fatalf("WriteField(model): %v", err)
	}
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="file"; filename="notes.txt"`)
	partHeader.Set("Content-Type", "text/plain")
	filePart, err := writer.CreatePart(partHeader)
	if err != nil {
		t.Fatalf("CreatePart(): %v", err)
	}
	if _, err := filePart.Write([]byte("not audio")); err != nil {
		t.Fatalf("Write(file): %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "unsupported audio format") {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}
