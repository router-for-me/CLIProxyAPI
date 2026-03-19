package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

const audioTestModel = "gpt-4o-mini-transcribe"

type audioExecutorResponse struct {
	status      int
	body        string
	contentType string
	headers     http.Header
}

type audioCaptureExecutor struct {
	id           string
	mu           sync.Mutex
	calls        int
	delay        time.Duration
	authIDs      []string
	lastURL      string
	lastAuthID   string
	lastFields   map[string][]string
	lastFileName string
	lastFileBody []byte
	lastLength   int64
	lastAccept   string
	responses    map[string]audioExecutorResponse
}

func (e *audioCaptureExecutor) Identifier() string {
	if strings.TrimSpace(e.id) != "" {
		return e.id
	}
	return "openai-compatibility"
}

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
	e.mu.Lock()
	e.calls++
	if auth != nil {
		e.lastAuthID = auth.ID
		e.authIDs = append(e.authIDs, auth.ID)
	}
	e.lastURL = req.URL.String()
	e.lastFields = make(map[string][]string)
	e.lastFileName = ""
	e.lastFileBody = nil
	e.lastLength = req.ContentLength
	e.lastAccept = req.Header.Get("Accept")
	e.mu.Unlock()

	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	if mediaType != "multipart/form-data" {
		return nil, errors.New("unexpected content type")
	}

	reader := multipart.NewReader(req.Body, params["boundary"])
	for {
		part, errNext := reader.NextPart()
		if errors.Is(errNext, io.EOF) {
			break
		}
		if errNext != nil {
			return nil, errNext
		}

		payload, errRead := io.ReadAll(part)
		_ = part.Close()
		if errRead != nil {
			return nil, errRead
		}

		e.mu.Lock()
		if part.FormName() == audioTranscriptionFileFieldName {
			e.lastFileName = part.FileName()
			e.lastFileBody = payload
		} else {
			e.lastFields[part.FormName()] = append(e.lastFields[part.FormName()], string(payload))
		}
		e.mu.Unlock()
	}

	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	respCfg := audioExecutorResponse{
		status:      http.StatusOK,
		body:        "transcribed text",
		contentType: "text/plain; charset=utf-8",
	}
	if e.responses != nil {
		if candidate, ok := e.responses[authID]; ok {
			respCfg = candidate
		}
	}
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	if respCfg.status == 0 {
		respCfg.status = http.StatusOK
	}
	if respCfg.contentType == "" {
		respCfg.contentType = "text/plain; charset=utf-8"
	}

	respHeaders := make(http.Header)
	if respCfg.headers != nil {
		respHeaders = respCfg.headers.Clone()
	}
	if strings.TrimSpace(respCfg.contentType) != "" {
		respHeaders.Set("Content-Type", respCfg.contentType)
	}

	return &http.Response{
		StatusCode: respCfg.status,
		Header:     respHeaders,
		Body:       io.NopCloser(strings.NewReader(respCfg.body)),
	}, nil
}

func (e *audioCaptureExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *audioCaptureExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.authIDs))
	copy(out, e.authIDs)
	return out
}

func TestAudioTranscriptionsWrapsPlainTextResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model":    audioTestModel,
		"prompt":   "caption this",
		"language": "en",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if strings.TrimSpace(resp.Body.String()) != `{"text":"transcribed text"}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if executor.Calls() != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.Calls())
	}
	if executor.lastAuthID != "audio-auth" {
		t.Fatalf("last auth id = %q, want %q", executor.lastAuthID, "audio-auth")
	}
	if executor.lastURL != "https://api.example.com/v1/audio/transcriptions" {
		t.Fatalf("last URL = %q, want %q", executor.lastURL, "https://api.example.com/v1/audio/transcriptions")
	}
	if got := executor.lastFields["model"]; len(got) != 1 || got[0] != audioTestModel {
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
	if executor.lastLength <= 0 {
		t.Fatalf("content length = %d, want > 0", executor.lastLength)
	}
}

func TestAudioTranscriptionsRejectsUnsupportedFileFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	router := newAudioTestRouter(t, executor)

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": audioTestModel,
	}, audioTranscriptionFileFieldName, "notes.txt", "text/plain", []byte("not audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "unsupported audio format") {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if executor.Calls() != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.Calls())
	}
}

func TestAudioTranscriptionsRejectUnsupportedResponseFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	router := newAudioTestRouter(t, executor)

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model":           audioTestModel,
		"response_format": "markdown",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "unsupported response_format") || !strings.Contains(resp.Body.String(), "markdown") {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if executor.Calls() != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.Calls())
	}
}

func TestAudioTranscriptionsPreserveExplicitTextResponseFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{
		responses: map[string]audioExecutorResponse{
			"audio-auth": {
				status:      http.StatusOK,
				body:        "raw transcript",
				contentType: "text/plain; charset=utf-8",
			},
		},
	}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model":           audioTestModel,
		"response_format": "text",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if strings.TrimSpace(resp.Body.String()) != "raw transcript" {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q, want %q", got, "text/plain; charset=utf-8")
	}
	if got := executor.lastAccept; got != "" {
		t.Fatalf("accept = %q, want empty for raw response format", got)
	}
}

func TestAudioTranscriptionsPreserveExplicitVTTResponseFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{
		responses: map[string]audioExecutorResponse{
			"audio-auth": {
				status:      http.StatusOK,
				body:        "WEBVTT\n\n00:00.000 --> 00:01.000\nHello",
				contentType: "text/vtt; charset=utf-8",
			},
		},
	}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model":           audioTestModel,
		"response_format": "vtt",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "text/vtt; charset=utf-8" {
		t.Fatalf("content type = %q, want %q", got, "text/vtt; charset=utf-8")
	}
	if !strings.Contains(resp.Body.String(), "WEBVTT") {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestAudioTranscriptionsRawResponseDoesNotEmitKeepAlive(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{
		delay: 2200 * time.Millisecond,
		responses: map[string]audioExecutorResponse{
			"audio-auth": {
				status:      http.StatusOK,
				body:        "raw transcript",
				contentType: "text/plain; charset=utf-8",
			},
		},
	}
	router := newAudioTestRouterWithConfig(t, &sdkconfig.SDKConfig{
		NonStreamKeepAliveInterval: 1,
	}, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model":           audioTestModel,
		"response_format": "text",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", resp.Code, http.StatusOK, resp.Body.String())
	}
	if resp.Body.String() != "raw transcript" {
		t.Fatalf("body = %q, want %q", resp.Body.String(), "raw transcript")
	}
	if got := resp.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q, want %q", got, "text/plain; charset=utf-8")
	}
}

func TestNormalizeAudioTranscriptionResponse(t *testing.T) {
	testCases := []struct {
		name string
		body []byte
		want string
	}{
		{
			name: "empty body",
			body: nil,
			want: `{"text":""}`,
		},
		{
			name: "existing text field preserved",
			body: []byte(`{"text":"hello","segments":[{"id":1}]}`),
			want: `{"text":"hello","segments":[{"id":1}]}`,
		},
		{
			name: "bare json string wrapped",
			body: []byte(`"hello"`),
			want: `{"text":"hello"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(normalizeAudioTranscriptionResponse(tc.body))
			if got != tc.want {
				t.Fatalf("normalizeAudioTranscriptionResponse() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestResolveAudioTranscriptionURL_DefaultCodexOAuth(t *testing.T) {
	url, err := resolveAudioTranscriptionURL(&coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"email": "user@example.com",
		},
	})
	if err != nil {
		t.Fatalf("resolveAudioTranscriptionURL() error = %v", err)
	}
	if url != defaultCodexAudioTranscriptionURL {
		t.Fatalf("resolveAudioTranscriptionURL() = %q, want %q", url, defaultCodexAudioTranscriptionURL)
	}
}

func TestResolveAudioTranscriptionURL_ConfiguredCodexBaseURL(t *testing.T) {
	url, err := resolveAudioTranscriptionURL(&coreauth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://chatgpt.com/backend-api/codex",
		},
	})
	if err != nil {
		t.Fatalf("resolveAudioTranscriptionURL() error = %v", err)
	}
	if url != "https://chatgpt.com/backend-api/transcribe" {
		t.Fatalf("resolveAudioTranscriptionURL() = %q, want %q", url, "https://chatgpt.com/backend-api/transcribe")
	}
}

func TestAudioTranscriptionsStreamTruePassthroughSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{
		responses: map[string]audioExecutorResponse{
			"audio-auth": {
				status:      http.StatusOK,
				body:        "event: transcript.text.delta\ndata: {\"delta\":\"hi\"}\n\n",
				contentType: "text/event-stream",
			},
		},
	}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model":  audioTestModel,
		"stream": "true",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if strings.TrimSpace(resp.Body.String()) != strings.TrimSpace("event: transcript.text.delta\ndata: {\"delta\":\"hi\"}") {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q, want %q", got, "text/event-stream")
	}
	if got := executor.lastAccept; got != "text/event-stream" {
		t.Fatalf("accept = %q, want %q", got, "text/event-stream")
	}
}

func TestAudioTranscriptionsRejectOversizedNonFileFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model":  audioTestModel,
		"prompt": strings.Repeat("a", int(audioTranscriptionNonFileFieldsLimitBytes+1)),
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusBadRequest, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "non-file multipart fields exceed") {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if executor.Calls() != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.Calls())
	}
}

func TestAudioTranscriptionsTreatEventStreamAsStreamingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{
		responses: map[string]audioExecutorResponse{
			"audio-auth": {
				status:      http.StatusOK,
				body:        "event: transcript.text.done\ndata: {\"text\":\"hello\"}\n\n",
				contentType: "text/event-stream; charset=utf-8",
			},
		},
	}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model":  audioTestModel,
		"stream": "definitely",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q, want %q", got, "text/event-stream; charset=utf-8")
	}
	if !strings.Contains(resp.Body.String(), "event: transcript.text.done") {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestAudioTranscriptionsNormalizeLargeUpstreamResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	largeTranscript := strings.Repeat("x", 8<<20+1)
	executor := &audioCaptureExecutor{
		responses: map[string]audioExecutorResponse{
			"audio-auth": {
				status:      http.StatusOK,
				body:        largeTranscript,
				contentType: "text/plain; charset=utf-8",
			},
		},
	}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": audioTestModel,
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body prefix=%s", resp.Code, http.StatusOK, resp.Body.String()[:min(len(resp.Body.String()), 64)])
	}
	var payload map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal(response): %v", err)
	}
	if got := payload["text"]; len(got) != len(largeTranscript) {
		t.Fatalf("text len = %d, want %d", len(got), len(largeTranscript))
	}
}

func TestAudioTranscriptionsRetriesAcrossAuthsOnRetriableHTTPFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{
		responses: map[string]audioExecutorResponse{
			"auth1": {
				status:      http.StatusInternalServerError,
				body:        `{"error":"temporary failure"}`,
				contentType: "application/json",
			},
			"auth2": {
				status:      http.StatusOK,
				body:        "retried transcription",
				contentType: "text/plain; charset=utf-8",
			},
		},
	}
	router := newAudioTestRouter(t, executor,
		&coreauth.Auth{
			ID:       "auth1",
			Provider: "openai-compatibility",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"base_url": "https://api.example.com/v1",
			},
		},
		&coreauth.Auth{
			ID:       "auth2",
			Provider: "openai-compatibility",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"base_url": "https://api.example.com/v1",
			},
		},
	)

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": audioTestModel,
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if strings.TrimSpace(resp.Body.String()) != `{"text":"retried transcription"}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if executor.Calls() != 2 {
		t.Fatalf("executor calls = %d, want 2", executor.Calls())
	}
	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth1" || got[1] != "auth2" {
		t.Fatalf("auth IDs = %v, want [auth1 auth2]", got)
	}
}

func TestAudioTranscriptionsRetriesAcrossAuthsOnRequestBuildFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	router := newAudioTestRouter(t, executor,
		&coreauth.Auth{
			ID:       "auth1",
			Provider: "openai-compatibility",
			Status:   coreauth.StatusActive,
		},
		&coreauth.Auth{
			ID:       "auth2",
			Provider: "openai-compatibility",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"base_url": "https://api.example.com/v1",
			},
		},
	)

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": audioTestModel,
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.Calls() != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.Calls())
	}
	if got := executor.AuthIDs(); len(got) != 1 || got[0] != "auth2" {
		t.Fatalf("auth IDs = %v, want [auth2]", got)
	}
}

func TestAudioTranscriptionsCleansUpStagedTempFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	t.Setenv("TMP", tempDir)
	t.Setenv("TEMP", tempDir)

	executor := &audioCaptureExecutor{}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": audioTestModel,
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", tempDir, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("expected temp dir to be empty, got %v", names)
	}
}

func TestAudioTranscriptionsResolveAutoToTranscriptionModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("chat-auto-client", "openai", []*registry.ModelInfo{{
		ID:      "gpt-5.2",
		Created: time.Now().Unix() + 1000,
	}})
	t.Cleanup(func() {
		reg.UnregisterClient("chat-auto-client")
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": "auto",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := executor.lastFields["model"]; len(got) != 1 || got[0] != audioTestModel {
		t.Fatalf("model field = %v, want [%s]", got, audioTestModel)
	}
}

func TestAudioTranscriptionsStripThinkingSuffixFromMultipartModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	router := newAudioTestRouter(t, executor, &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": audioTestModel + "(high)",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := executor.lastFields["model"]; len(got) != 1 || got[0] != audioTestModel {
		t.Fatalf("model field = %v, want [%s]", got, audioTestModel)
	}
}

func TestAudioTranscriptionsPreserveExplicitWhisperModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{}
	auth := &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": "https://api.example.com/v1",
		},
	}
	router := newAudioTestRouter(t, executor, auth)

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{
		{ID: audioTestModel},
		{ID: "whisper-1"},
	})

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": "whisper-1",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.lastAuthID != auth.ID {
		t.Fatalf("last auth id = %q, want %q", executor.lastAuthID, auth.ID)
	}
	if got := executor.lastFields["model"]; len(got) != 1 || got[0] != "whisper-1" {
		t.Fatalf("model field = %v, want [whisper-1]", got)
	}
}

func TestAudioTranscriptionsResolveAutoToCompatibleAliasModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{id: "pool"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "pool",
			Models: []internalconfig.OpenAICompatibilityModel{
				{Name: audioTestModel, Alias: "voice-default"},
			},
		}},
	})
	manager.RegisterExecutor(executor)

	poolAuth := &coreauth.Auth{
		ID:       "pool-auth",
		Provider: "pool",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://api.example.com/v1",
			"compat_name":  "pool",
			"provider_key": "pool",
		},
	}
	if _, err := manager.Register(context.Background(), poolAuth); err != nil {
		t.Fatalf("register pool auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(poolAuth.ID, "pool", []*registry.ModelInfo{{
		ID:      "voice-default",
		Created: time.Now().Unix(),
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(poolAuth.ID)
	})

	geminiAuth := &coreauth.Auth{
		ID:       "gemini-auth",
		Provider: "gemini",
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), geminiAuth); err != nil {
		t.Fatalf("register gemini auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(geminiAuth.ID, "gemini", []*registry.ModelInfo{{
		ID:      "gemini-speech-to-text",
		Created: time.Now().Unix() + 1000,
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(geminiAuth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/audio/transcriptions", h.AudioTranscriptions)

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": "auto",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.lastAuthID != poolAuth.ID {
		t.Fatalf("last auth id = %q, want %q", executor.lastAuthID, poolAuth.ID)
	}
	if got := executor.lastFields["model"]; len(got) != 1 || got[0] != audioTestModel {
		t.Fatalf("model field = %v, want [%s]", got, audioTestModel)
	}
}

func TestAudioTranscriptionsResolveAutoToWhisperAliasModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{id: "pool"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "pool",
			Models: []internalconfig.OpenAICompatibilityModel{
				{Name: "whisper-1", Alias: "voice-whisper"},
			},
		}},
	})
	manager.RegisterExecutor(executor)

	poolAuth := &coreauth.Auth{
		ID:       "pool-auth",
		Provider: "pool",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://api.example.com/v1",
			"compat_name":  "pool",
			"provider_key": "pool",
		},
	}
	if _, err := manager.Register(context.Background(), poolAuth); err != nil {
		t.Fatalf("register pool auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(poolAuth.ID, "pool", []*registry.ModelInfo{{
		ID:      "voice-whisper",
		Created: time.Now().Unix(),
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(poolAuth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/audio/transcriptions", h.AudioTranscriptions)

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": "auto",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.lastAuthID != poolAuth.ID {
		t.Fatalf("last auth id = %q, want %q", executor.lastAuthID, poolAuth.ID)
	}
	if got := executor.lastFields["model"]; len(got) != 1 || got[0] != "whisper-1" {
		t.Fatalf("model field = %v, want [whisper-1]", got)
	}
}

func TestAudioTranscriptionsResolveAutoSkipsUnavailableAuths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &audioCaptureExecutor{id: "pool"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "pool",
			Models: []internalconfig.OpenAICompatibilityModel{
				{Name: audioTestModel, Alias: "voice-new"},
			},
		}},
	})
	manager.RegisterExecutor(executor)

	blockedAuth := &coreauth.Auth{
		ID:       "blocked-auth",
		Provider: "pool",
		Status:   coreauth.StatusDisabled,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://api.example.com/v1",
			"compat_name":  "pool",
			"provider_key": "pool",
		},
	}
	if _, err := manager.Register(context.Background(), blockedAuth); err != nil {
		t.Fatalf("register blocked auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(blockedAuth.ID, "pool", []*registry.ModelInfo{{
		ID:      "voice-new",
		Created: time.Now().Unix() + 1000,
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(blockedAuth.ID)
	})

	activeAuth := &coreauth.Auth{
		ID:       "active-auth",
		Provider: "pool",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://api.example.com/v1",
			"compat_name":  "pool",
			"provider_key": "pool",
		},
	}
	if _, err := manager.Register(context.Background(), activeAuth); err != nil {
		t.Fatalf("register active auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(activeAuth.ID, "pool", []*registry.ModelInfo{{
		ID:      audioTestModel,
		Created: time.Now().Unix(),
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(activeAuth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/audio/transcriptions", h.AudioTranscriptions)

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": "auto",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.lastAuthID != activeAuth.ID {
		t.Fatalf("last auth id = %q, want %q", executor.lastAuthID, activeAuth.ID)
	}
	if got := executor.lastFields["model"]; len(got) != 1 || got[0] != audioTestModel {
		t.Fatalf("model field = %v, want [%s]", got, audioTestModel)
	}
}

func TestResolveAudioAutoModelUsesProviderSpecificMetadata(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)

	audioAuth := &coreauth.Auth{
		ID:       "provider-aware-audio-auth",
		Provider: "compat-audio",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://api.example.com/v1",
			"compat_name":  "compat-audio",
			"provider_key": "compat-audio",
		},
	}
	if _, err := manager.Register(context.Background(), audioAuth); err != nil {
		t.Fatalf("register audio auth: %v", err)
	}

	nonAudioAuth := &coreauth.Auth{
		ID:       "provider-aware-text-auth",
		Provider: "compat-text",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://api.example.com/v1",
			"compat_name":  "compat-text",
			"provider_key": "compat-text",
		},
	}
	if _, err := manager.Register(context.Background(), nonAudioAuth); err != nil {
		t.Fatalf("register text auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(audioAuth.ID, audioAuth.Provider, []*registry.ModelInfo{{
		ID:                       "shared-transcription-model",
		Created:                  time.Now().Unix(),
		SupportedInputModalities: []string{"audio"},
		SupportedOutputModalities: []string{
			"text",
		},
	}})
	reg.RegisterClient(nonAudioAuth.ID, nonAudioAuth.Provider, []*registry.ModelInfo{{
		ID:                       "shared-transcription-model",
		Created:                  time.Now().Unix() + 1,
		SupportedInputModalities: []string{"text"},
		SupportedOutputModalities: []string{
			"text",
		},
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(audioAuth.ID)
		reg.UnregisterClient(nonAudioAuth.ID)
	})

	got, err := resolveAudioAutoModel(manager, "auto")
	if err != nil {
		t.Fatalf("resolveAudioAutoModel(auto) error = %v", err)
	}
	if got != "shared-transcription-model" {
		t.Fatalf("resolveAudioAutoModel(auto) = %q, want %q", got, "shared-transcription-model")
	}
}

func TestAudioTranscriptionsResolveAutoPinsSelectedAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	audioExecutor := &audioCaptureExecutor{id: "zz-audio"}
	textExecutor := &audioCaptureExecutor{
		id: "aa-text",
		responses: map[string]audioExecutorResponse{
			"text-auth": {
				status:      http.StatusBadRequest,
				body:        `{"error":"model not found"}`,
				contentType: "application/json",
			},
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(textExecutor)
	manager.RegisterExecutor(audioExecutor)

	textAuth := &coreauth.Auth{
		ID:       "text-auth",
		Provider: "aa-text",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://text.example.com/v1",
			"compat_name":  "aa-text",
			"provider_key": "aa-text",
		},
	}
	if _, err := manager.Register(context.Background(), textAuth); err != nil {
		t.Fatalf("register text auth: %v", err)
	}

	audioAuth := &coreauth.Auth{
		ID:       "audio-auth",
		Provider: "zz-audio",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://audio.example.com/v1",
			"compat_name":  "zz-audio",
			"provider_key": "zz-audio",
		},
	}
	if _, err := manager.Register(context.Background(), audioAuth); err != nil {
		t.Fatalf("register audio auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(textAuth.ID, textAuth.Provider, []*registry.ModelInfo{{
		ID:                       "shared-transcription-model",
		Created:                  time.Now().Unix() + 1,
		SupportedInputModalities: []string{"text"},
		SupportedOutputModalities: []string{
			"text",
		},
	}})
	reg.RegisterClient(audioAuth.ID, audioAuth.Provider, []*registry.ModelInfo{{
		ID:                       "shared-transcription-model",
		Created:                  time.Now().Unix(),
		SupportedInputModalities: []string{"audio"},
		SupportedOutputModalities: []string{
			"text",
		},
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(textAuth.ID)
		reg.UnregisterClient(audioAuth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/audio/transcriptions", h.AudioTranscriptions)

	req := newAudioMultipartRequest(t, "/v1/audio/transcriptions", map[string]string{
		"model": "auto",
	}, audioTranscriptionFileFieldName, "sample.webm", "", []byte("fake-audio"))
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if audioExecutor.lastAuthID != audioAuth.ID {
		t.Fatalf("audio executor auth id = %q, want %q", audioExecutor.lastAuthID, audioAuth.ID)
	}
	if textExecutor.Calls() != 0 {
		t.Fatalf("text executor calls = %d, want 0", textExecutor.Calls())
	}
	if got := audioExecutor.lastFields["model"]; len(got) != 1 || got[0] != "shared-transcription-model" {
		t.Fatalf("model field = %v, want [shared-transcription-model]", got)
	}
}

func newAudioTestRouter(t *testing.T, executor coreauth.ProviderExecutor, auths ...*coreauth.Auth) *gin.Engine {
	return newAudioTestRouterWithConfig(t, &sdkconfig.SDKConfig{}, executor, auths...)
}

func newAudioTestRouterWithConfig(t *testing.T, cfg *sdkconfig.SDKConfig, executor coreauth.ProviderExecutor, auths ...*coreauth.Auth) *gin.Engine {
	t.Helper()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	for _, auth := range auths {
		current := cloneAudioAuth(auth)
		if current.Status == "" {
			current.Status = coreauth.StatusActive
		}
		if current.Provider == "" {
			current.Provider = executor.Identifier()
		}
		if _, err := manager.Register(context.Background(), current); err != nil {
			t.Fatalf("Register auth %s: %v", current.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(current.ID, current.Provider, []*registry.ModelInfo{{ID: audioTestModel}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(current.ID)
		})
	}

	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/audio/transcriptions", h.AudioTranscriptions)
	return router
}

func newAudioMultipartRequest(t *testing.T, target string, fields map[string]string, fileField, fileName, fileContentType string, fileContent []byte) *http.Request {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	fieldNames := make([]string, 0, len(fields))
	for fieldName := range fields {
		fieldNames = append(fieldNames, fieldName)
	}
	sort.Strings(fieldNames)
	for _, fieldName := range fieldNames {
		if err := writer.WriteField(fieldName, fields[fieldName]); err != nil {
			t.Fatalf("WriteField(%s): %v", fieldName, err)
		}
	}

	var filePart io.Writer
	var err error
	if fileContentType == "" {
		filePart, err = writer.CreateFormFile(fileField, fileName)
	} else {
		partHeader := make(textproto.MIMEHeader)
		partHeader.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
			"name":     fileField,
			"filename": fileName,
		}))
		partHeader.Set("Content-Type", fileContentType)
		filePart, err = writer.CreatePart(partHeader)
	}
	if err != nil {
		t.Fatalf("Create file part: %v", err)
	}
	if _, err := filePart.Write(fileContent); err != nil {
		t.Fatalf("Write(file): %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, target, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func cloneAudioAuth(auth *coreauth.Auth) *coreauth.Auth {
	if auth == nil {
		return nil
	}
	clone := *auth
	if auth.Attributes != nil {
		clone.Attributes = make(map[string]string, len(auth.Attributes))
		for key, value := range auth.Attributes {
			clone.Attributes[key] = value
		}
	}
	if auth.Metadata != nil {
		clone.Metadata = make(map[string]any, len(auth.Metadata))
		for key, value := range auth.Metadata {
			clone.Metadata[key] = value
		}
	}
	return &clone
}
