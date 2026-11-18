package live

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func transcriptionUpload(t *testing.T, fields map[string]string, files int) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < files; i++ {
		part, err := writer.CreateFormFile("file", "speech.wav")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("RIFF-private-audio-data")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func transcriptionFixture(t *testing.T, status int, response string) (*Handler, *captureExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, &apiKeyFirstSelector{}, nil)
	executor := &captureExecutor{statusCode: status, responseBody: io.NopCloser(strings.NewReader(response))}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID: "api-key", Provider: "codex", Status: auth.StatusActive,
		Attributes: map[string]string{auth.AttributeAPIKey: "never-use-this-key"},
	})
	registerCredential(t, manager, &auth.Auth{
		ID: "oauth", Provider: "codex", Status: auth.StatusActive,
		Metadata: map[string]any{"access_token": "upstream-token", "account_id": "account-123"},
	})
	return NewHandler(manager, nil), executor
}

func serveTranscription(handler *Handler, body []byte, contentType string) *httptest.ResponseRecorder {
	router := gin.New()
	router.POST("/v1/audio/transcriptions", handler.HandleTranscription)
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer downstream-client-key")
	request.Header.Set("Cookie", "secret-cookie")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestTranscriptionUsesOAuthAndPreservesAudio(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			handler, executor := transcriptionFixture(t, 200, `{"text":"Hello world","extra":"not-public"}`)
			body, ct := transcriptionUpload(t, map[string]string{"model": transcriptionModel, "language": "en", "response_format": format, "stream": "false", "prompt": "private-dictionary"}, 1)
			response := serveTranscription(handler, body, ct)
			if response.Code != 200 {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if response.Header().Get("X-CLIProxy-Transcription-Prompt") != "ignored" {
				t.Fatal("ignored dictionary hint was not disclosed")
			}
			if format == "text" {
				if response.Body.String() != "Hello world" || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/plain") {
					t.Fatalf("invalid text response: %s %v", response.Body, response.Header())
				}
			} else if response.Body.String() != `{"text":"Hello world"}` {
				t.Fatalf("invalid JSON response: %s", response.Body)
			}
			if executor.selectedAuth.ID != "oauth" || executor.request.URL.String() != transcriptionURL {
				t.Fatalf("wrong credential or endpoint: %s %s", executor.selectedAuth.ID, executor.request.URL)
			}
			if executor.request.Header.Get("Authorization") != "Bearer upstream-token" || executor.request.Header.Get("Chatgpt-Account-Id") != "account-123" || executor.request.Header.Get("Cookie") != "" {
				t.Fatal("downstream credentials leaked or OAuth authentication was lost")
			}
			for _, blocked := range []string{"Set-Cookie", "Connection", "X-Connection-Secret", "Location"} {
				if response.Header().Get(blocked) != "" {
					t.Fatalf("upstream header %s leaked", blocked)
				}
			}
			_, params, err := mime.ParseMediaType(executor.request.Header.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			reader := multipart.NewReader(bytes.NewReader(executor.body), params["boundary"])
			fields := make(map[string]string)
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				value, err := io.ReadAll(part)
				if err != nil {
					t.Fatal(err)
				}
				fields[part.FormName()] = string(value)
			}
			if len(fields) != 2 || fields["file"] != "RIFF-private-audio-data" || fields["language"] != "en" {
				t.Fatalf("audio changed or compatibility fields forwarded: %v", fields)
			}
		})
	}
}

func TestTranscriptionRejectsUnsupportedRequestsBeforeUpstream(t *testing.T) {
	for name, fields := range map[string]map[string]string{
		"missing model": {},
		"chat model":    {"model": "gpt-6.1-sol"},
		"streaming":     {"model": transcriptionModel, "stream": "true"},
		"subtitles":     {"model": transcriptionModel, "response_format": "srt"},
		"diarization":   {"model": "gpt-4o-transcribe-diarize"},
		"long field":    {"model": transcriptionModel, "language": strings.Repeat("a", transcriptionFieldLimit+1)},
	} {
		t.Run(name, func(t *testing.T) {
			handler, executor := transcriptionFixture(t, 200, `{"text":"wrong"}`)
			body, ct := transcriptionUpload(t, fields, 1)
			response := serveTranscription(handler, body, ct)
			if response.Code != 400 || executor.httpCalls.Load() != 0 || !json.Valid(response.Body.Bytes()) {
				t.Fatalf("invalid request reached upstream: %d %s", response.Code, response.Body)
			}
		})
	}
	for _, count := range []int{0, 2} {
		handler, executor := transcriptionFixture(t, 200, `{"text":"wrong"}`)
		body, ct := transcriptionUpload(t, map[string]string{"model": transcriptionModel}, count)
		response := serveTranscription(handler, body, ct)
		if response.Code != 400 || executor.httpCalls.Load() != 0 {
			t.Fatalf("accepted %d audio files", count)
		}
	}
}

func TestTranscriptionBoundsUnknownLengthUploads(t *testing.T) {
	handler, executor := transcriptionFixture(t, 200, `{"text":"wrong"}`)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "speech.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(part, io.LimitReader(repeatedByteReader{}, transcriptionUploadLimit+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
	request.ContentLength = -1
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = request
	handler.HandleTranscription(ctx)
	if response.Code != 413 || executor.httpCalls.Load() != 0 {
		t.Fatalf("oversized upload accepted: %d %s", response.Code, response.Body)
	}
}

type repeatedByteReader struct{}

func (repeatedByteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestTranscriptionSanitizesUpstreamFailures(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		handler, executor := transcriptionFixture(t, status, "<html>secret-cookie upstream-token</html>")
		body, ct := transcriptionUpload(t, map[string]string{"model": transcriptionModel}, 1)
		response := serveTranscription(handler, body, ct)
		if response.Code != status || !json.Valid(response.Body.Bytes()) || strings.Contains(response.Body.String(), "secret-cookie") || strings.Contains(response.Body.String(), "upstream-token") || executor.httpCalls.Load() != 1 {
			t.Fatalf("unsafe upstream failure: %d %s", response.Code, response.Body)
		}
	}
	for _, payload := range []string{`{}`, `{"text":null}`, `<html>challenge</html>`, strings.Repeat("x", transcriptionReplyLimit+1)} {
		handler, _ := transcriptionFixture(t, 200, payload)
		body, ct := transcriptionUpload(t, map[string]string{"model": transcriptionModel}, 1)
		response := serveTranscription(handler, body, ct)
		if response.Code != 502 {
			t.Fatalf("accepted malformed or oversized response: %d", response.Code)
		}
	}
}

func TestTranscriptionDiagnosticsExcludeCredentialsAndAudio(t *testing.T) {
	for _, fullLogging := range []bool{false, true} {
		handler, _ := transcriptionFixture(t, 403, "secret-cookie upstream-token")
		if err := handler.UpdateConfig(&config.Config{SDKConfig: config.SDKConfig{RequestLog: fullLogging}}); err != nil {
			t.Fatal(err)
		}
		body, ct := transcriptionUpload(t, map[string]string{"model": transcriptionModel, "prompt": "private-dictionary"}, 1)
		request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
		request.Header.Set("Content-Type", ct)
		request.Header.Set("Authorization", "Bearer downstream-client-key")
		request.Header.Set("Cookie", "secret-cookie")
		response := httptest.NewRecorder()
		ginContext, _ := gin.CreateTestContext(response)
		ginContext.Request = request
		handler.HandleTranscription(ginContext)
		var diagnostics strings.Builder
		if fullLogging {
			value, exists := ginContext.Get("API_REQUEST")
			if !exists {
				t.Fatal("request diagnostics were not recorded")
			}
			diagnostics.Write(value.([]byte))
		} else {
			value, exists := ginContext.Get(logging.DeferredAPIRequestContextKey)
			if !exists {
				t.Fatal("deferred request diagnostics were not recorded")
			}
			for _, deferred := range value.([]logging.DeferredAPIRequest) {
				diagnostics.Write(deferred())
			}
		}
		diagnostics.Write(response.Body.Bytes())
		for _, secret := range []string{"downstream-client-key", "upstream-token", "secret-cookie", "RIFF-private-audio-data", "never-use-this-key", "private-dictionary"} {
			if strings.Contains(diagnostics.String(), secret) {
				t.Fatalf("private data present in diagnostics with full logging=%v", fullLogging)
			}
		}
	}
}

func TestTranscriptionRefusesAPIKeyOnlyCredentials(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	executor := &captureExecutor{}
	manager.RegisterExecutor(executor)
	_, err := manager.Register(context.Background(), &auth.Auth{ID: "key", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "key"}})
	if err != nil {
		t.Fatal(err)
	}
	body, ct := transcriptionUpload(t, map[string]string{"model": transcriptionModel}, 1)
	response := serveTranscription(NewHandler(manager, nil), body, ct)
	if response.Code != 503 || executor.httpCalls.Load() != 0 {
		t.Fatalf("API key selected for OAuth endpoint: %d %s", response.Code, response.Body)
	}
}

type canceledTranscriptionExecutor struct {
	captureExecutor
	started chan struct{}
}

func (e *canceledTranscriptionExecutor) HttpRequest(ctx context.Context, _ *auth.Auth, _ *http.Request) (*http.Response, error) {
	close(e.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTranscriptionClientCancellationStopsUpstream(t *testing.T) {
	handler, _ := transcriptionFixture(t, 200, `{"text":"unused"}`)
	executor := &canceledTranscriptionExecutor{started: make(chan struct{})}
	handler.authManager.RegisterExecutor(executor)
	body, ct := transcriptionUpload(t, map[string]string{"model": transcriptionModel}, 1)
	request := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	request.Header.Set("Content-Type", ct)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(response)
	ginContext.Request = request
	done := make(chan struct{})
	go func() { defer close(done); handler.HandleTranscription(ginContext) }()
	select {
	case <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream transcription ignored client cancellation")
	}
}
