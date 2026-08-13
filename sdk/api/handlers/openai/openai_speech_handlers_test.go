package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// speechCaptureExecutor records what the handler asked the auth manager to run and
// replies with a fixed binary payload.
type speechCaptureExecutor struct {
	mu       sync.Mutex
	models   []string
	payloads [][]byte
	audio    []byte
}

func (e *speechCaptureExecutor) Identifier() string { return "xai" }

func (e *speechCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.models = append(e.models, req.Model)
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	e.mu.Unlock()

	audio := e.audio
	if len(audio) == 0 {
		audio = []byte{0xFF, 0xFB, 0x90, 0x00}
	}
	// Upstream headers are intentionally returned here; the handler must still
	// derive Content-Type itself because the pipeline drops them by default.
	return coreexecutor.Response{Payload: audio}, nil
}

func (e *speechCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "ExecuteStream not implemented"}
}

func (e *speechCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *speechCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *speechCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func (e *speechCaptureExecutor) Payloads() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]byte, len(e.payloads))
	copy(out, e.payloads)
	return out
}

func (e *speechCaptureExecutor) Models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.models))
	copy(out, e.models)
	return out
}

func newSpeechTestHandler(t *testing.T, executor *speechCaptureExecutor, cfg *sdkconfig.SDKConfig) *OpenAIAPIHandler {
	t.Helper()

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	authID := "speech-test-auth"
	auth := &coreauth.Auth{ID: authID, Provider: "xai", Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{
		{ID: xaiSpeechModel, Object: "model", OwnedBy: "xai", Type: "xai"},
	})
	manager.RefreshSchedulerEntry(authID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	if cfg == nil {
		cfg = &sdkconfig.SDKConfig{}
	}
	return NewOpenAIAPIHandler(apihandlers.NewBaseAPIHandlers(cfg, manager))
}

func performSpeechRequest(t *testing.T, method, routePath, requestPath, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, routePath, handler)

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, requestPath, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func TestSpeechModelValidation(t *testing.T) {
	for _, model := range []string{"grok-tts", "xai/grok-tts", "x-ai/grok-tts", "grok/grok-tts", "GROK-TTS"} {
		if !isXAISpeechModel(model) {
			t.Fatalf("expected %s to route to xAI speech", model)
		}
	}
	for _, model := range []string{"grok-4.6", "codex/grok-tts", "gpt-4o-mini-tts", ""} {
		if isXAISpeechModel(model) {
			t.Fatalf("expected %s to be rejected as an xAI speech model", model)
		}
	}
}

func TestSpeechModelValidationAllowsOpenAICompatSpeechModels(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-openai-compat-speech-model-validation"
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{
		{ID: "compat-speech-model", Object: "model", OwnedBy: "compat", Type: registry.OpenAISpeechModelType},
		{ID: "compat-chat-model2", Object: "model", OwnedBy: "compat", Type: "openai-compatibility"},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	if !isSupportedSpeechModel("compat-speech-model") {
		t.Fatal("expected configured openai-compatibility speech model to be supported")
	}
	if isSupportedSpeechModel("compat-chat-model2") {
		t.Fatal("expected non-speech openai-compatibility model to be rejected")
	}
}

func TestClampXAISpeechSpeed(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{in: 0.25, want: xaiSpeechMinSpeed},
		{in: 0.7, want: 0.7},
		{in: 1.0, want: 1.0},
		{in: 1.5, want: 1.5},
		{in: 4.0, want: xaiSpeechMaxSpeed},
	}
	for _, tc := range cases {
		if got := clampXAISpeechSpeed(tc.in); got != tc.want {
			t.Fatalf("clampXAISpeechSpeed(%g) = %g, want %g", tc.in, got, tc.want)
		}
	}
}

func TestBuildXAISpeechRequestMapsOpenAIFields(t *testing.T) {
	rawJSON := []byte(`{"model":"grok-tts","input":"hello world","voice":"ara","response_format":"wav","speed":4.0,"instructions":"be cheerful"}`)

	payload, contentType, errMessage := buildXAISpeechRequest(rawJSON)
	if errMessage != "" {
		t.Fatalf("buildXAISpeechRequest() error = %q", errMessage)
	}

	if got := gjson.GetBytes(payload, "text").String(); got != "hello world" {
		t.Fatalf("text = %q, want hello world", got)
	}
	if got := gjson.GetBytes(payload, "voice_id").String(); got != "ara" {
		t.Fatalf("voice_id = %q, want ara", got)
	}
	if got := gjson.GetBytes(payload, "language").String(); got != xaiSpeechDefaultLanguage {
		t.Fatalf("language = %q, want %s", got, xaiSpeechDefaultLanguage)
	}
	if got := gjson.GetBytes(payload, "output_format.codec").String(); got != "wav" {
		t.Fatalf("output_format.codec = %q, want wav", got)
	}
	if got := gjson.GetBytes(payload, "speed").Float(); got != xaiSpeechMaxSpeed {
		t.Fatalf("speed = %g, want %g", got, xaiSpeechMaxSpeed)
	}
	// xAI's /v1/tts takes no model field and has no instructions equivalent.
	if gjson.GetBytes(payload, "model").Exists() {
		t.Fatalf("model must not be forwarded upstream: %s", string(payload))
	}
	if gjson.GetBytes(payload, "instructions").Exists() {
		t.Fatalf("instructions must not be forwarded upstream: %s", string(payload))
	}
	if contentType != "audio/wav" {
		t.Fatalf("contentType = %q, want audio/wav", contentType)
	}
}

func TestBuildXAISpeechRequestOmitsDefaultOutputFormat(t *testing.T) {
	payload, contentType, errMessage := buildXAISpeechRequest([]byte(`{"model":"grok-tts","input":"hi","voice":"eve"}`))
	if errMessage != "" {
		t.Fatalf("buildXAISpeechRequest() error = %q", errMessage)
	}
	if gjson.GetBytes(payload, "output_format").Exists() {
		t.Fatalf("output_format should be omitted for default mp3: %s", string(payload))
	}
	if gjson.GetBytes(payload, "speed").Exists() {
		t.Fatalf("speed should be omitted when absent: %s", string(payload))
	}
	if contentType != "audio/mpeg" {
		t.Fatalf("contentType = %q, want audio/mpeg", contentType)
	}
}

func TestBuildXAISpeechRequestHonorsLangCode(t *testing.T) {
	payload, _, errMessage := buildXAISpeechRequest([]byte(`{"model":"grok-tts","input":"hola","voice":"eve","lang_code":"es-ES"}`))
	if errMessage != "" {
		t.Fatalf("buildXAISpeechRequest() error = %q", errMessage)
	}
	if got := gjson.GetBytes(payload, "language").String(); got != "es-ES" {
		t.Fatalf("language = %q, want es-ES", got)
	}
}

func TestBuildXAISpeechRequestForwardsAllowlistedExtras(t *testing.T) {
	rawJSON := []byte(`{"model":"grok-tts","input":"hi","voice":"eve","sample_rate":48000,"text_normalization":true,"optimize_streaming_latency":2}`)
	payload, _, errMessage := buildXAISpeechRequest(rawJSON)
	if errMessage != "" {
		t.Fatalf("buildXAISpeechRequest() error = %q", errMessage)
	}
	if got := gjson.GetBytes(payload, "output_format.sample_rate").Int(); got != 48000 {
		t.Fatalf("sample_rate = %d, want 48000", got)
	}
	if !gjson.GetBytes(payload, "text_normalization").Bool() {
		t.Fatalf("text_normalization not forwarded: %s", string(payload))
	}
	if got := gjson.GetBytes(payload, "optimize_streaming_latency").Int(); got != 2 {
		t.Fatalf("optimize_streaming_latency = %d, want 2", got)
	}
}

func TestBuildXAISpeechRequestRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name    string
		rawJSON string
	}{
		{name: "missing input", rawJSON: `{"model":"grok-tts","voice":"eve"}`},
		{name: "blank input", rawJSON: `{"model":"grok-tts","input":"   ","voice":"eve"}`},
		{name: "missing voice", rawJSON: `{"model":"grok-tts","input":"hi"}`},
		{name: "opus unsupported", rawJSON: `{"model":"grok-tts","input":"hi","voice":"eve","response_format":"opus"}`},
		{name: "aac unsupported", rawJSON: `{"model":"grok-tts","input":"hi","voice":"eve","response_format":"aac"}`},
		{name: "flac unsupported", rawJSON: `{"model":"grok-tts","input":"hi","voice":"eve","response_format":"flac"}`},
		{name: "speed out of range", rawJSON: `{"model":"grok-tts","input":"hi","voice":"eve","speed":9}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, errMessage := buildXAISpeechRequest([]byte(tc.rawJSON)); errMessage == "" {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestBuildXAISpeechRequestRejectsOversizedInput(t *testing.T) {
	oversized := strings.Repeat("a", xaiSpeechMaxTextLength+1)
	payload, _, errMessage := buildXAISpeechRequest([]byte(`{"model":"grok-tts","voice":"eve","input":"` + oversized + `"}`))
	if errMessage == "" {
		t.Fatalf("expected oversized input to be rejected, got payload %s", string(payload))
	}
}

func TestBuildXAITTSPassthroughStripsModelAndDefaults(t *testing.T) {
	rawJSON := []byte(`{"model":"grok-tts","text":"hello","voice_id":"rex","speed":1.2,"replace":{"CPA":"see pee ay"}}`)

	payload, routingModel, contentType, errMessage := buildXAITTSPassthroughRequest(rawJSON)
	if errMessage != "" {
		t.Fatalf("buildXAITTSPassthroughRequest() error = %q", errMessage)
	}
	if routingModel != xaiSpeechModel {
		t.Fatalf("routingModel = %q, want %s", routingModel, xaiSpeechModel)
	}
	if gjson.GetBytes(payload, "model").Exists() {
		t.Fatalf("model must be stripped before forwarding: %s", string(payload))
	}
	if got := gjson.GetBytes(payload, "language").String(); got != xaiSpeechDefaultLanguage {
		t.Fatalf("language = %q, want %s", got, xaiSpeechDefaultLanguage)
	}
	// Everything else must survive untouched.
	if got := gjson.GetBytes(payload, "voice_id").String(); got != "rex" {
		t.Fatalf("voice_id = %q, want rex", got)
	}
	if got := gjson.GetBytes(payload, "speed").Float(); got != 1.2 {
		t.Fatalf("speed = %g, want 1.2", got)
	}
	if got := gjson.GetBytes(payload, "replace.CPA").String(); got != "see pee ay" {
		t.Fatalf("replace.CPA = %q, want 'see pee ay'", got)
	}
	if contentType != "audio/mpeg" {
		t.Fatalf("contentType = %q, want audio/mpeg", contentType)
	}
}

func TestBuildXAITTSPassthroughPreservesExplicitLanguage(t *testing.T) {
	payload, _, contentType, errMessage := buildXAITTSPassthroughRequest([]byte(`{"text":"bonjour","voice_id":"eve","language":"fr","output_format":{"codec":"wav"}}`))
	if errMessage != "" {
		t.Fatalf("buildXAITTSPassthroughRequest() error = %q", errMessage)
	}
	if got := gjson.GetBytes(payload, "language").String(); got != "fr" {
		t.Fatalf("language = %q, want fr", got)
	}
	if contentType != "audio/wav" {
		t.Fatalf("contentType = %q, want audio/wav", contentType)
	}
}

func TestSpeechContentType(t *testing.T) {
	cases := map[string]string{
		"mp3":   "audio/mpeg",
		"wav":   "audio/wav",
		"pcm":   "audio/pcm",
		"opus":  "audio/opus",
		"aac":   "audio/aac",
		"flac":  "audio/flac",
		"mulaw": "audio/basic",
		"alaw":  "audio/x-alaw-basic",
		"weird": "application/octet-stream",
	}
	for codec, want := range cases {
		if got := speechContentType(codec, false); got != want {
			t.Fatalf("speechContentType(%q) = %q, want %q", codec, got, want)
		}
	}
	// with_timestamps flips the upstream response from binary to JSON.
	if got := speechContentType("mp3", true); got != "application/json" {
		t.Fatalf("speechContentType with timestamps = %q, want application/json", got)
	}
}

func TestAudioSpeechRejectsBadRequests(t *testing.T) {
	handler := newSpeechTestHandler(t, &speechCaptureExecutor{}, nil)
	cases := []struct {
		name string
		body string
	}{
		{name: "missing model", body: `{"input":"hi","voice":"eve"}`},
		{name: "unsupported model", body: `{"model":"grok-4.6","input":"hi","voice":"eve"}`},
		{name: "sse stream format", body: `{"model":"grok-tts","input":"hi","voice":"eve","stream_format":"sse"}`},
		{name: "invalid json", body: `{"model":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := performSpeechRequest(t, http.MethodPost, audioSpeechPath, audioSpeechPath, tc.body, handler.AudioSpeech)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body.String())
			}
			if got := gjson.GetBytes(resp.Body.Bytes(), "error.type").String(); got != "invalid_request_error" {
				t.Fatalf("error.type = %q, want invalid_request_error", got)
			}
		})
	}
}

// TestAudioSpeechWritesBinaryWithDerivedContentType is the regression test for the
// dropped-upstream-headers trap: the executor returns no headers, so the handler
// must derive Content-Type from the requested codec.
func TestAudioSpeechWritesBinaryWithDerivedContentType(t *testing.T) {
	audio := []byte{0xFF, 0xFB, 0x90, 0x00, 0xDE, 0xAD, 0xBE, 0xEF}
	executor := &speechCaptureExecutor{audio: audio}
	handler := newSpeechTestHandler(t, executor, nil)

	resp := performSpeechRequest(t, http.MethodPost, audioSpeechPath, audioSpeechPath,
		`{"model":"grok-tts","input":"hello","voice":"eve"}`, handler.AudioSpeech)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("Content-Type = %q, want audio/mpeg", got)
	}
	if !bytes.Equal(resp.Body.Bytes(), audio) {
		t.Fatalf("body = %v, want %v", resp.Body.Bytes(), audio)
	}

	models := executor.Models()
	if len(models) != 1 || models[0] != xaiSpeechModel {
		t.Fatalf("executor models = %v, want [%s]", models, xaiSpeechModel)
	}
	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("executor payload count = %d, want 1", len(payloads))
	}
	if gjson.GetBytes(payloads[0], "model").Exists() {
		t.Fatalf("model leaked upstream: %s", string(payloads[0]))
	}
	if got := gjson.GetBytes(payloads[0], "text").String(); got != "hello" {
		t.Fatalf("upstream text = %q, want hello", got)
	}
}

// TestAudioSpeechKeepAliveDoesNotCorruptAudio guards the keep-alive trap: the
// non-streaming keep-alive writes newlines into the response body, which is
// invisible ahead of JSON but would corrupt an audio payload.
func TestAudioSpeechKeepAliveDoesNotCorruptAudio(t *testing.T) {
	audio := []byte{0xFF, 0xFB, 0x90, 0x00}
	executor := &speechCaptureExecutor{audio: audio}
	handler := newSpeechTestHandler(t, executor, &sdkconfig.SDKConfig{NonStreamKeepAliveInterval: 1})

	resp := performSpeechRequest(t, http.MethodPost, audioSpeechPath, audioSpeechPath,
		`{"model":"grok-tts","input":"hello","voice":"eve"}`, handler.AudioSpeech)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	if !bytes.Equal(resp.Body.Bytes(), audio) {
		t.Fatalf("body = %v, want exactly the upstream audio %v", resp.Body.Bytes(), audio)
	}
}

func TestXAITTSPassthroughEndToEnd(t *testing.T) {
	audio := []byte{0x52, 0x49, 0x46, 0x46}
	executor := &speechCaptureExecutor{audio: audio}
	handler := newSpeechTestHandler(t, executor, nil)

	resp := performSpeechRequest(t, http.MethodPost, ttsPath, ttsPath,
		`{"text":"hello","voice_id":"eve","language":"en","output_format":{"codec":"wav"}}`, handler.XAITTS)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("Content-Type = %q, want audio/wav", got)
	}
	if !bytes.Equal(resp.Body.Bytes(), audio) {
		t.Fatalf("body = %v, want %v", resp.Body.Bytes(), audio)
	}

	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("executor payload count = %d, want 1", len(payloads))
	}
	if got := gjson.GetBytes(payloads[0], "language").String(); got != "en" {
		t.Fatalf("language = %q, want en", got)
	}
}

func TestXAITTSRejectsMissingText(t *testing.T) {
	handler := newSpeechTestHandler(t, &speechCaptureExecutor{}, nil)
	resp := performSpeechRequest(t, http.MethodPost, ttsPath, ttsPath, `{"voice_id":"eve"}`, handler.XAITTS)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.Code, resp.Body.String())
	}
}
