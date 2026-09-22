package gemini

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestParseInteractionsRequestTarget(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantModel string
		wantAgent string
		wantErr   bool
	}{
		{name: "model", body: `{"model":"gemini-3.5-flash","input":"hi"}`, wantModel: "gemini-3.5-flash"},
		{name: "model resource name", body: `{"model":"models/gemini-3.5-flash","input":"hi"}`, wantModel: "models/gemini-3.5-flash"},
		{name: "agent", body: `{"agent":"agents/test-agent","input":"hi"}`, wantAgent: "agents/test-agent"},
		{name: "missing", body: `{"input":"hi"}`, wantErr: true},
		{name: "both", body: `{"model":"gemini-3.5-flash","agent":"agents/test-agent","input":"hi"}`, wantErr: true},
		{name: "stream string", body: `{"model":"gemini-3.5-flash","stream":"true","input":"hi"}`, wantErr: true},
		{name: "stream true", body: `{"model":"gemini-3.5-flash","stream":true,"input":"hi"}`, wantModel: "gemini-3.5-flash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, errParse := parseInteractionsRequestTarget([]byte(tt.body))
			if tt.wantErr {
				if errParse == nil {
					t.Fatal("parseInteractionsRequestTarget() error = nil, want error")
				}
				return
			}
			if errParse != nil {
				t.Fatalf("parseInteractionsRequestTarget() error = %v", errParse)
			}
			if target.Model != tt.wantModel || target.Agent != tt.wantAgent {
				t.Fatalf("target = %#v, want model %q agent %q", target, tt.wantModel, tt.wantAgent)
			}
		})
	}
}

func TestPrepareInteractionsExecutionTargetNormalizesModelResourceName(t *testing.T) {
	target, errParse := parseInteractionsRequestTarget([]byte(`{"model":"models/gemini-3.5-flash","input":"hi"}`))
	if errParse != nil {
		t.Fatalf("parseInteractionsRequestTarget() error = %v", errParse)
	}
	model, body := prepareInteractionsExecutionTarget([]byte(`{"model":"models/gemini-3.5-flash","input":"hi"}`), target)
	if model != "gemini-3.5-flash" {
		t.Fatalf("model = %q, want gemini-3.5-flash", model)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gemini-3.5-flash" {
		t.Fatalf("body model = %q, want gemini-3.5-flash. Body: %s", got, string(body))
	}
}

func TestPrepareInteractionsExecutionTargetPreservesBareModel(t *testing.T) {
	target, errParse := parseInteractionsRequestTarget([]byte(`{"model":"gemini-3.5-flash","input":"hi"}`))
	if errParse != nil {
		t.Fatalf("parseInteractionsRequestTarget() error = %v", errParse)
	}
	model, body := prepareInteractionsExecutionTarget([]byte(`{"model":"gemini-3.5-flash","input":"hi"}`), target)
	if model != "gemini-3.5-flash" {
		t.Fatalf("model = %q, want gemini-3.5-flash", model)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gemini-3.5-flash" {
		t.Fatalf("body model = %q, want gemini-3.5-flash. Body: %s", got, string(body))
	}
}

func TestBuildInteractionsExecutionRequestUsesAgentAuthSelectionModel(t *testing.T) {
	target, errParse := parseInteractionsRequestTarget([]byte(`{"agent":"agents/test-agent","input":"hi"}`))
	if errParse != nil {
		t.Fatalf("parseInteractionsRequestTarget() error = %v", errParse)
	}
	req := buildInteractionsExecutionRequest(target, "agents/test-agent", []byte(`{"agent":"agents/test-agent","input":"hi"}`), "")
	if req.ForcedProvider != "gemini-interactions" {
		t.Fatalf("ForcedProvider = %q, want gemini-interactions", req.ForcedProvider)
	}
	if req.AuthSelectionModel != interactionsAgentAuthSelectionModel {
		t.Fatalf("AuthSelectionModel = %q, want %q", req.AuthSelectionModel, interactionsAgentAuthSelectionModel)
	}
	if req.Model != "agents/test-agent" {
		t.Fatalf("Model = %q, want agents/test-agent", req.Model)
	}
	if got := gjson.GetBytes(req.Body, "agent").String(); got != "agents/test-agent" {
		t.Fatalf("body agent = %q, want agents/test-agent", got)
	}
}

func TestInteractionsRejectsInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{`))
	h := NewGeminiAPIHandler(&handlers.BaseAPIHandler{})

	h.Interactions(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_request_error") {
		t.Fatalf("body = %s, want invalid_request_error", rec.Body.String())
	}
}

func TestInteractionsRejectsMissingModelAndAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{"input":"hi"}`))
	h := NewGeminiAPIHandler(&handlers.BaseAPIHandler{})

	h.Interactions(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exactly one of model or agent") {
		t.Fatalf("body = %s, want model/agent validation error", rec.Body.String())
	}
}

func TestInteractionsRejectsBothModelAndAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{"model":"gemini-3.5-flash","agent":"agents/test-agent","input":"hi"}`))
	h := NewGeminiAPIHandler(&handlers.BaseAPIHandler{})

	h.Interactions(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exactly one of model or agent") {
		t.Fatalf("body = %s, want model/agent validation error", rec.Body.String())
	}
}

func TestInteractionsRejectsNonBooleanStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{"model":"gemini-3.5-flash","stream":"true","input":"hi"}`))
	h := NewGeminiAPIHandler(&handlers.BaseAPIHandler{})

	h.Interactions(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_request_error") {
		t.Fatalf("body = %s, want invalid_request_error", rec.Body.String())
	}
}

func TestInteractionsAgentUsesNativeInteractionsEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotPath string
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, errRead.Error(), http.StatusBadRequest)
			return
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewGeminiInteractionsExecutor(&config.Config{RequestRetry: 1}))
	auth := &coreauth.Auth{
		ID:       "interactions-agent-native-auth",
		Provider: "gemini-interactions",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
		Metadata: map[string]any{"email": "interactions-agent@example.com"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: interactionsAgentAuthSelectionModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{"agent":"agents/test-agent","input":"hi"}`))
	h := NewGeminiAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))

	h.Interactions(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/v1beta/interactions" {
		t.Fatalf("path = %q, want /v1beta/interactions", gotPath)
	}
	if got := gjson.GetBytes(upstreamBody, "agent").String(); got != "agents/test-agent" {
		t.Fatalf("upstream agent = %q, want agents/test-agent. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "id").String(); got != "interaction_1" {
		t.Fatalf("response id = %q, want interaction_1. Body: %s", got, rec.Body.String())
	}
}

func TestInteractionsAntigravityModelUsesTranslatorBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	model := "interactions-antigravity-bridge-model"
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:generateContent" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, errRead.Error(), http.StatusBadRequest)
			return
		}
		upstreamBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"text":"translated-ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`))
	}))
	defer server.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewAntigravityExecutor(&config.Config{RequestRetry: 1}))
	auth := &coreauth.Auth{
		ID:       "interactions-antigravity-bridge-auth",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{"model":"`+model+`","input":"hi","generation_config":{"top_p":0.8}}`))
	h := NewGeminiAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))

	h.Interactions(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gjson.GetBytes(upstreamBody, "input").Exists() {
		t.Fatalf("upstream body still contains raw interactions input: %s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("upstream request text = %q, want hi. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.generationConfig.topP").Float(); got != 0.8 {
		t.Fatalf("upstream topP = %v, want 0.8. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(rec.Body.Bytes(), "steps.0.content.0.text").String(); got != "translated-ok" {
		t.Fatalf("response text = %q, want translated-ok. Body: %s", got, rec.Body.String())
	}
	if gjson.GetBytes(rec.Body.Bytes(), "response").Exists() {
		t.Fatalf("response still contains raw antigravity response wrapper: %s", rec.Body.String())
	}
}

func TestForwardInteractionsStreamWrapsBareJSONAsSSEData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{}`))
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`{"type":"interaction.completed"}`)
	close(data)
	close(errs)
	h := NewGeminiAPIHandler(&handlers.BaseAPIHandler{})

	h.forwardInteractionsStream(ctx, rec, func(error) {}, data, errs)

	if got := rec.Body.String(); got != "data: {\"type\":\"interaction.completed\"}\n\n" {
		t.Fatalf("body = %q, want SSE data frame", got)
	}
}
