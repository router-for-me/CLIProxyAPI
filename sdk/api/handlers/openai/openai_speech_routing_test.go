package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type speechCaptureExecutor struct {
	mu      sync.Mutex
	models  []string
	sources []string
}

func (e *speechCaptureExecutor) Identifier() string { return "xai" }

func (e *speechCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.models = append(e.models, req.Model)
	e.sources = append(e.sources, opts.SourceFormat.String())
	e.mu.Unlock()
	return coreexecutor.Response{Payload: []byte("ID3audio"), Headers: http.Header{"Content-Type": []string{"audio/mpeg"}}}, nil
}

func (e *speechCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.models = append(e.models, "stream")
	e.mu.Unlock()
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

func (e *speechCaptureExecutor) calls() (models, sources []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.models...), append([]string(nil), e.sources...)
}

func newSpeechRoutingTestHandlers(t *testing.T, executor *speechCaptureExecutor) (*OpenAIAPIHandler, *OpenAIResponsesAPIHandler) {
	t.Helper()

	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "speech-routing-auth", Provider: "xai", Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: defaultXAISpeechModel}, {ID: xaiSpeechVoiceModel}})
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	return NewOpenAIAPIHandler(base), NewOpenAIResponsesAPIHandler(base)
}

func performSpeechRoutingRequest(t *testing.T, path string, body string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST(path, handler)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	return resp
}

func TestSpeechEndpointsRouteSpeechOnlyModelsToExecutor(t *testing.T) {
	executor := &speechCaptureExecutor{}
	handler, _ := newSpeechRoutingTestHandlers(t, executor)

	cases := []struct {
		name      string
		path      string
		handler   gin.HandlerFunc
		body      string
		wantModel string
	}{
		{name: "audio speech default model", path: "/v1/audio/speech", handler: handler.AudioSpeech, body: `{"model":"tts-1","input":"hello","voice":"nova"}`, wantModel: defaultXAISpeechModel},
		{name: "audio speech grok-tts", path: "/v1/audio/speech", handler: handler.AudioSpeech, body: `{"model":"xai/grok-tts","input":"hello"}`, wantModel: defaultXAISpeechModel},
		{name: "audio speech voice model", path: "/v1/audio/speech", handler: handler.AudioSpeech, body: `{"model":"grok-voice-tts-1.0","input":"hello"}`, wantModel: xaiSpeechVoiceModel},
		{name: "native tts", path: "/v1/tts", handler: handler.XAITTS, body: `{"text":"hello","voice_id":"eve"}`, wantModel: defaultXAISpeechModel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := executor.calls()
			resp := performSpeechRoutingRequest(t, tc.path, tc.body, tc.handler)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusOK, resp.Body.String())
			}
			if resp.Body.String() != "ID3audio" {
				t.Fatalf("body = %q", resp.Body.String())
			}
			models, sources := executor.calls()
			if len(models) != len(before)+1 {
				t.Fatalf("executor calls = %v, want one new call", models)
			}
			if got := models[len(models)-1]; got != tc.wantModel {
				t.Fatalf("executor model = %q, want %q", got, tc.wantModel)
			}
			if got := sources[len(sources)-1]; got != xaiSpeechHandlerType {
				t.Fatalf("executor source format = %q, want %q", got, xaiSpeechHandlerType)
			}
		})
	}
}

func TestChatAndResponsesRejectSpeechOnlyModels(t *testing.T) {
	executor := &speechCaptureExecutor{}
	chat, responses := newSpeechRoutingTestHandlers(t, executor)

	for _, model := range []string{"grok-tts", "xai/grok-tts", "grok-voice-tts-1.0"} {
		t.Run(model, func(t *testing.T) {
			chatBody := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
			chatStreamBody := `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`
			responsesBody := `{"model":"` + model + `","input":"hi"}`
			responsesStreamBody := `{"model":"` + model + `","stream":true,"input":"hi"}`
			requests := []struct {
				name    string
				path    string
				body    string
				handler gin.HandlerFunc
			}{
				{name: "chat", path: "/v1/chat/completions", body: chatBody, handler: chat.ChatCompletions},
				{name: "chat stream", path: "/v1/chat/completions", body: chatStreamBody, handler: chat.ChatCompletions},
				{name: "responses", path: "/v1/responses", body: responsesBody, handler: responses.Responses},
				{name: "responses stream", path: "/v1/responses", body: responsesStreamBody, handler: responses.Responses},
			}
			for _, request := range requests {
				resp := performSpeechRoutingRequest(t, request.path, request.body, request.handler)
				if resp.Code < 400 || resp.Code >= 500 {
					t.Fatalf("%s status = %d, want 4xx: %s", request.name, resp.Code, resp.Body.String())
				}
				if !strings.Contains(resp.Body.String(), "/v1/audio/speech") {
					t.Fatalf("%s body = %s, want speech endpoint hint", request.name, resp.Body.String())
				}
			}
			if models, _ := executor.calls(); len(models) != 0 {
				t.Fatalf("executor received speech-only model on chat route: %v", models)
			}
		})
	}
}
