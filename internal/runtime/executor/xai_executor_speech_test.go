package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestXAISpeechRequestURLStaysOnOfficialAPI(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  xaiauth.CLIChatProxyBaseURL,
		},
	}
	got := xaiSpeechRequestURL(auth)
	want := strings.TrimSuffix(xaiauth.DefaultAPIBaseURL, "/") + xaiTTSPath
	if got != want {
		t.Fatalf("xaiSpeechRequestURL() = %q, want %q", got, want)
	}
	if xaiIsCLIChatProxyBaseURL(got) {
		t.Fatalf("speech URL pinned to CLI chat proxy: %s", got)
	}

	custom := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://gateway.example/v1"}}
	if got := xaiSpeechRequestURL(custom); got != "https://gateway.example/v1/tts" {
		t.Fatalf("custom speech URL = %q", got)
	}
}

func TestXAIExecutorExecuteSpeechPostsAudioRequest(t *testing.T) {
	const body = `{"text":"hello","voice_id":"eve","language":"auto"}`
	var gotPath string
	var gotAuth string
	var gotAccept string
	var gotContentType string
	var gotClientVersion string
	var gotTokenAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotContentType = r.Header.Get("Content-Type")
		gotClientVersion = r.Header.Get(xaiClientVersionHeader)
		gotTokenAuth = r.Header.Get(xaiTokenAuthHeader)
		raw, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
		}
		gotBody = string(raw)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3audio"))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL + "/v1",
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-tts",
		Payload: []byte(body),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-speech"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/v1/tts" {
		t.Fatalf("path = %q, want /v1/tts", gotPath)
	}
	if gotAuth != "Bearer xai-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccept != "*/*" {
		t.Fatalf("Accept = %q, want */*", gotAccept)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if gotClientVersion != "" || gotTokenAuth != "" {
		t.Fatalf("chat-proxy headers leaked: version=%q token-auth=%q", gotClientVersion, gotTokenAuth)
	}
	if gotBody != body {
		t.Fatalf("body = %s", gotBody)
	}
	if string(resp.Payload) != "ID3audio" {
		t.Fatalf("payload = %q", resp.Payload)
	}
	if resp.Headers.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("response Content-Type = %q", resp.Headers.Get("Content-Type"))
	}
}

func TestXAIExecutorExecuteStreamRejectsSpeech(t *testing.T) {
	exec := NewXAIExecutor(&config.Config{})
	_, err := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(xaiSpeechHandlerType),
	})
	if err == nil || !strings.Contains(err.Error(), "streaming not supported") {
		t.Fatalf("error = %v", err)
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status error = %v", err)
	}
}

func TestXAIExecutorExecuteSpeechPayloadRulesMatchOpenAIProtocol(t *testing.T) {
	const body = `{"text":"hello","voice_id":"eve","language":"auto"}`
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read body: %v", errRead)
		}
		gotBody = string(raw)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3audio"))
	}))
	defer server.Close()

	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "grok-tts", Protocol: "openai"}},
		Params: map[string]any{"voice_id": "ara", "language": "en"},
	}}}}
	exec := NewXAIExecutor(cfg)
	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  server.URL + "/v1",
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-tts",
		Payload: []byte(body),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(xaiSpeechHandlerType),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.Get(gotBody, "voice_id").String(); got != "ara" {
		t.Fatalf("voice_id = %q, want payload rule override ara; body=%s", got, gotBody)
	}
	if got := gjson.Get(gotBody, "language").String(); got != "en" {
		t.Fatalf("language = %q, want payload rule override en; body=%s", got, gotBody)
	}
	if got := gjson.Get(gotBody, "text").String(); got != "hello" {
		t.Fatalf("text = %q, want hello; body=%s", got, gotBody)
	}
}

func TestXAIExecutorExecuteSpeechUpstreamErrorScope(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantScoped bool
	}{
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":"invalid language"}`, wantStatus: http.StatusBadRequest},
		{name: "bad request model unsupported", status: http.StatusBadRequest, body: `{"error":"requested model is not supported"}`, wantStatus: http.StatusBadRequest},
		{name: "unknown voice not found", status: http.StatusNotFound, body: `{"error":"voice not found"}`, wantStatus: http.StatusNotFound, wantScoped: true},
		{name: "model not found nested code", status: http.StatusNotFound, body: `{"error":{"code":"model_not_found","message":"The model grok-tts does not exist"}}`, wantStatus: http.StatusNotFound},
		{name: "model not found flat code", status: http.StatusNotFound, body: `{"code":"model_not_found","error":"model unavailable"}`, wantStatus: http.StatusNotFound},
		{name: "model not available", status: http.StatusNotFound, body: `{"error":"The model grok-tts is not available for your account"}`, wantStatus: http.StatusNotFound},
		{name: "model not available plain text", status: http.StatusNotFound, body: `model is not available`, wantStatus: http.StatusNotFound},
		{name: "model unsupported", status: http.StatusNotFound, body: `{"error":{"message":"Unsupported model: grok-tts"}}`, wantStatus: http.StatusNotFound},
		{name: "unprocessable", status: http.StatusUnprocessableEntity, body: `{"error":"text too long"}`, wantStatus: http.StatusUnprocessableEntity},
		{name: "unprocessable model unsupported", status: http.StatusUnprocessableEntity, body: `{"code":"model_not_supported","error":"model is not supported"}`, wantStatus: http.StatusUnprocessableEntity},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`, wantStatus: http.StatusUnauthorized},
		{name: "bad credentials remapped", status: http.StatusForbidden, body: `{"code":"bad-credentials","error":"access token could not be validated"}`, wantStatus: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":"forbidden"}`, wantStatus: http.StatusForbidden},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`, wantStatus: http.StatusTooManyRequests},
		{name: "upstream failure", status: http.StatusInternalServerError, body: `{"error":"boom"}`, wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			exec := NewXAIExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				Provider: "xai",
				Attributes: map[string]string{
					"base_url":  server.URL + "/v1",
					"auth_kind": "oauth",
				},
				Metadata: map[string]any{"access_token": "xai-token"},
			}
			_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "grok-tts",
				Payload: []byte(`{"text":"hello","voice_id":"nope","language":"auto"}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(xaiSpeechHandlerType),
			})
			if err == nil {
				t.Fatal("Execute() error = nil, want upstream error")
			}
			status, ok := err.(interface{ StatusCode() int })
			if !ok || status.StatusCode() != tt.wantStatus {
				t.Fatalf("status error = %v (%T), want status %d", err, err, tt.wantStatus)
			}
			if err.Error() != tt.body {
				t.Fatalf("error message = %q, want upstream body %q", err.Error(), tt.body)
			}
			if tt.wantScoped {
				assertRequestScopedTestError(t, err)
				if !isRequestScopedExecutorError(err) {
					t.Fatalf("error %T is not recognized as cliproxyexecutor.RequestScopedError", err)
				}
				return
			}
			assertNotRequestScopedTestError(t, err)
			if isRequestScopedExecutorError(err) {
				t.Fatalf("error %T unexpectedly recognized as cliproxyexecutor.RequestScopedError", err)
			}
		})
	}
}

func isRequestScopedExecutorError(err error) bool {
	var requestErr cliproxyexecutor.RequestScopedError
	return errors.As(err, &requestErr) && requestErr.IsRequestScoped()
}

func newXAISpeechScopeManager(t *testing.T, baseURL string, count int) (*cliproxyauth.Manager, []string) {
	t.Helper()
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewXAIExecutor(&config.Config{}))

	reg := registry.GetGlobalRegistry()
	authIDs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		auth := &cliproxyauth.Auth{
			ID:       uuid.NewString() + "-xai-speech-scope",
			Provider: "xai",
			Attributes: map[string]string{
				"base_url":  baseURL + "/v1",
				"auth_kind": "oauth",
			},
			Metadata: map[string]any{"access_token": "xai-token-" + uuid.NewString()},
		}
		reg.RegisterClient(auth.ID, "xai", []*registry.ModelInfo{{ID: "grok-tts"}})
		authID := auth.ID
		t.Cleanup(func() { reg.UnregisterClient(authID) })
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
		authIDs = append(authIDs, auth.ID)
	}
	return manager, authIDs
}

func TestXAIExecutorSpeechUnknownVoiceDoesNotRotateOrCoolCredentials(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"voice not found"}`))
	}))
	defer server.Close()

	manager, authIDs := newXAISpeechScopeManager(t, server.URL, 2)
	reg := registry.GetGlobalRegistry()

	_, err := manager.Execute(context.Background(), []string{"xai"}, cliproxyexecutor.Request{
		Model:   "grok-tts",
		Payload: []byte(`{"text":"hello","voice_id":"nope","language":"auto"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(xaiSpeechHandlerType)})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 404")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no cross-credential retry)", got)
	}

	now := time.Now()
	for _, id := range authIDs {
		updated, ok := manager.GetByID(id)
		if !ok || updated == nil {
			t.Fatalf("auth %s not found", id)
		}
		if updated.Unavailable || updated.NextRetryAfter.After(now) {
			t.Fatalf("auth %s cooled down: unavailable=%v next_retry_after=%v", id, updated.Unavailable, updated.NextRetryAfter)
		}
		if state := updated.ModelStates["grok-tts"]; state != nil && (state.Unavailable || state.NextRetryAfter.After(now)) {
			t.Fatalf("auth %s model grok-tts cooled down: %+v", id, state)
		}
		if reg.IsModelSuspendedForClient(id, "grok-tts") {
			t.Fatalf("auth %s model grok-tts suspended in registry", id)
		}
	}
}

func TestXAIExecutorSpeechModelNotFoundRotatesCredentials(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"model_not_found","message":"The model grok-tts does not exist"}}`))
	}))
	defer server.Close()

	manager, _ := newXAISpeechScopeManager(t, server.URL, 2)

	_, err := manager.Execute(context.Background(), []string{"xai"}, cliproxyexecutor.Request{
		Model:   "grok-tts",
		Payload: []byte(`{"text":"hello","voice_id":"eve","language":"auto"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(xaiSpeechHandlerType)})
	if err == nil {
		t.Fatal("Execute() error = nil, want upstream 404")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("upstream attempts = %d, want 2 (model_not_found keeps credential rotation)", got)
	}
}
