package pluginapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type compileTimePlugin struct{}

var _ ModelRegistrar = (*compileTimePlugin)(nil)
var _ ModelProvider = (*compileTimePlugin)(nil)
var _ AuthProvider = (*compileTimePlugin)(nil)
var _ FrontendAuthProvider = (*compileTimePlugin)(nil)
var _ Scheduler = (*compileTimePlugin)(nil)
var _ ProviderExecutor = (*compileTimePlugin)(nil)
var _ HostHTTPClient = (*compileTimePlugin)(nil)
var _ RequestTranslator = (*compileTimePlugin)(nil)
var _ RequestNormalizer = (*compileTimePlugin)(nil)
var _ ResponseTranslator = (*compileTimePlugin)(nil)
var _ ResponseNormalizer = (*compileTimePlugin)(nil)
var _ RequestInterceptor = (*compileTimePlugin)(nil)
var _ ResponseInterceptor = (*compileTimePlugin)(nil)
var _ StreamChunkInterceptor = (*compileTimePlugin)(nil)
var _ ThinkingApplier = (*compileTimePlugin)(nil)
var _ UsagePlugin = (*compileTimePlugin)(nil)
var _ CommandLinePlugin = (*compileTimePlugin)(nil)
var _ ManagementAPI = (*compileTimePlugin)(nil)
var _ ManagementHandler = (*compileTimePlugin)(nil)

func TestMetadataConfigFieldsExposePluginSchema(t *testing.T) {
	meta := Metadata{
		Name:             "example",
		Version:          "1.0.0",
		Author:           "test",
		GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
		Logo:             "https://example.com/logo.svg",
		ConfigFields: []ConfigField{{
			Name:        "mode",
			Type:        ConfigFieldTypeEnum,
			EnumValues:  []string{"safe", "fast"},
			Description: "Execution mode.",
		}},
	}
	if meta.Logo == "" || len(meta.ConfigFields) != 1 {
		t.Fatalf("metadata missing logo or config fields: %#v", meta)
	}
}

func TestResourceRouteMenuFieldsExposeManagementUIHints(t *testing.T) {
	route := ResourceRoute{
		Path:        "/status",
		Menu:        "Example Status",
		Description: "Shows example plugin status.",
		Handler:     compileTimePlugin{},
	}
	if route.Menu == "" || route.Description == "" {
		t.Fatalf("resource route missing menu fields: %#v", route)
	}
}

func TestHostInjectedHTTPClientIsNotEncodedInPluginJSON(t *testing.T) {
	requests := []struct {
		name string
		req  any
		dst  any
	}{
		{
			name: "auth login start",
			req:  AuthLoginStartRequest{Provider: "plugin-example", HTTPClient: compileTimePlugin{}},
			dst:  &AuthLoginStartRequest{},
		},
		{
			name: "auth login poll",
			req:  AuthLoginPollRequest{Provider: "plugin-example", HTTPClient: compileTimePlugin{}},
			dst:  &AuthLoginPollRequest{},
		},
		{
			name: "auth refresh",
			req:  AuthRefreshRequest{AuthID: "auth-1", HTTPClient: compileTimePlugin{}},
			dst:  &AuthRefreshRequest{},
		},
		{
			name: "auth model",
			req:  AuthModelRequest{AuthID: "auth-1", HTTPClient: compileTimePlugin{}},
			dst:  &AuthModelRequest{},
		},
		{
			name: "executor request",
			req:  ExecutorRequest{Model: "model-1", HTTPClient: compileTimePlugin{}},
			dst:  &ExecutorRequest{},
		},
		{
			name: "executor http request",
			req:  ExecutorHTTPRequest{AuthID: "auth-1", HTTPClient: compileTimePlugin{}},
			dst:  &ExecutorHTTPRequest{},
		},
	}

	for _, tt := range requests {
		raw, errMarshal := json.Marshal(tt.req)
		if errMarshal != nil {
			t.Fatalf("%s marshal error = %v", tt.name, errMarshal)
		}
		if strings.Contains(string(raw), "HTTPClient") {
			t.Fatalf("%s JSON contains host HTTPClient: %s", tt.name, raw)
		}
		withLegacyHTTPClient := append(raw[:len(raw)-1], []byte(`,"HTTPClient":{}}`)...)
		if errUnmarshal := json.Unmarshal(withLegacyHTTPClient, tt.dst); errUnmarshal != nil {
			t.Fatalf("%s unmarshal with legacy HTTPClient object error = %v", tt.name, errUnmarshal)
		}
	}
}

func TestSchedulerTypesExposeRoutingFields(t *testing.T) {
	request := SchedulerPickRequest{
		Plugin:    Metadata{Name: "scheduler-plugin"},
		Provider:  "openai",
		Providers: []string{"openai", "gemini"},
		Model:     "gpt-test",
		Stream:    true,
		Options: SchedulerOptions{
			Headers:  map[string][]string{"X-Test": []string{"1"}},
			Metadata: map[string]any{"tenant": "demo"},
		},
		Candidates: []SchedulerAuthCandidate{{
			ID:         "auth-1",
			Provider:   "openai",
			Priority:   10,
			Status:     "ready",
			Attributes: map[string]string{"region": "us"},
			Metadata:   map[string]any{"load": float64(0.5)},
		}},
	}
	response := SchedulerPickResponse{
		AuthID:          request.Candidates[0].ID,
		DelegateBuiltin: SchedulerBuiltinRoundRobin,
		Handled:         true,
	}

	if request.Plugin.Name != "scheduler-plugin" {
		t.Fatalf("Plugin.Name = %q", request.Plugin.Name)
	}
	if request.Provider != "openai" {
		t.Fatalf("Provider = %q", request.Provider)
	}
	if len(request.Providers) != 2 || request.Providers[1] != "gemini" {
		t.Fatalf("Providers = %#v", request.Providers)
	}
	if request.Model != "gpt-test" {
		t.Fatalf("Model = %q", request.Model)
	}
	if !request.Stream {
		t.Fatalf("Stream = %v", request.Stream)
	}
	if got := request.Options.Headers["X-Test"]; len(got) != 1 || got[0] != "1" {
		t.Fatalf("Options.Headers = %#v", request.Options.Headers)
	}
	if request.Options.Metadata["tenant"] != "demo" {
		t.Fatalf("Options.Metadata = %#v", request.Options.Metadata)
	}
	if len(request.Candidates) != 1 {
		t.Fatalf("Candidates = %#v", request.Candidates)
	}
	candidate := request.Candidates[0]
	if candidate.ID != "auth-1" || candidate.Provider != "openai" || candidate.Priority != 10 || candidate.Status != "ready" {
		t.Fatalf("Candidate = %#v", candidate)
	}
	if candidate.Attributes["region"] != "us" {
		t.Fatalf("Candidate.Attributes = %#v", candidate.Attributes)
	}
	if candidate.Metadata["load"] != float64(0.5) {
		t.Fatalf("Candidate.Metadata = %#v", candidate.Metadata)
	}
	if response.AuthID != "auth-1" || response.DelegateBuiltin != SchedulerBuiltinRoundRobin || !response.Handled {
		t.Fatalf("SchedulerPickResponse = %#v", response)
	}
}

func (compileTimePlugin) RegisterModels(context.Context, ModelRegistrationRequest) (ModelRegistrationResponse, error) {
	return ModelRegistrationResponse{}, nil
}

func (compileTimePlugin) StaticModels(context.Context, StaticModelRequest) (ModelResponse, error) {
	return ModelResponse{}, nil
}

func (compileTimePlugin) ModelsForAuth(context.Context, AuthModelRequest) (ModelResponse, error) {
	return ModelResponse{}, nil
}

func (compileTimePlugin) Identifier() string { return "compile-time" }

func (compileTimePlugin) ParseAuth(context.Context, AuthParseRequest) (AuthParseResponse, error) {
	return AuthParseResponse{}, nil
}

func (compileTimePlugin) StartLogin(context.Context, AuthLoginStartRequest) (AuthLoginStartResponse, error) {
	return AuthLoginStartResponse{}, nil
}

func (compileTimePlugin) PollLogin(context.Context, AuthLoginPollRequest) (AuthLoginPollResponse, error) {
	return AuthLoginPollResponse{}, nil
}

func (compileTimePlugin) RefreshAuth(context.Context, AuthRefreshRequest) (AuthRefreshResponse, error) {
	return AuthRefreshResponse{}, nil
}

func (compileTimePlugin) Authenticate(context.Context, FrontendAuthRequest) (FrontendAuthResponse, error) {
	return FrontendAuthResponse{}, nil
}

func (compileTimePlugin) Pick(context.Context, SchedulerPickRequest) (SchedulerPickResponse, error) {
	return SchedulerPickResponse{}, nil
}

func (compileTimePlugin) Execute(context.Context, ExecutorRequest) (ExecutorResponse, error) {
	return ExecutorResponse{}, nil
}

func (compileTimePlugin) ExecuteStream(context.Context, ExecutorRequest) (ExecutorStreamResponse, error) {
	return ExecutorStreamResponse{}, nil
}

func (compileTimePlugin) CountTokens(context.Context, ExecutorRequest) (ExecutorResponse, error) {
	return ExecutorResponse{}, nil
}

func (compileTimePlugin) HttpRequest(context.Context, ExecutorHTTPRequest) (ExecutorHTTPResponse, error) {
	return ExecutorHTTPResponse{}, nil
}

func (compileTimePlugin) Do(context.Context, HTTPRequest) (HTTPResponse, error) {
	return HTTPResponse{}, nil
}

func (compileTimePlugin) DoStream(context.Context, HTTPRequest) (HTTPStreamResponse, error) {
	return HTTPStreamResponse{}, nil
}

func (compileTimePlugin) TranslateRequest(context.Context, RequestTransformRequest) (PayloadResponse, error) {
	return PayloadResponse{}, nil
}

func (compileTimePlugin) NormalizeRequest(context.Context, RequestTransformRequest) (PayloadResponse, error) {
	return PayloadResponse{}, nil
}

func (compileTimePlugin) TranslateResponse(context.Context, ResponseTransformRequest) (PayloadResponse, error) {
	return PayloadResponse{}, nil
}

func (compileTimePlugin) NormalizeResponse(context.Context, ResponseTransformRequest) (PayloadResponse, error) {
	return PayloadResponse{}, nil
}

func (compileTimePlugin) InterceptRequestBeforeAuth(context.Context, RequestInterceptRequest) (RequestInterceptResponse, error) {
	return RequestInterceptResponse{}, nil
}

func (compileTimePlugin) InterceptRequestAfterAuth(context.Context, RequestInterceptRequest) (RequestInterceptResponse, error) {
	return RequestInterceptResponse{}, nil
}

func (compileTimePlugin) InterceptResponse(context.Context, ResponseInterceptRequest) (ResponseInterceptResponse, error) {
	return ResponseInterceptResponse{}, nil
}

func (compileTimePlugin) InterceptStreamChunk(context.Context, StreamChunkInterceptRequest) (StreamChunkInterceptResponse, error) {
	return StreamChunkInterceptResponse{}, nil
}

func (compileTimePlugin) ApplyThinking(context.Context, ThinkingApplyRequest) (PayloadResponse, error) {
	return PayloadResponse{}, nil
}

func (compileTimePlugin) HandleUsage(context.Context, UsageRecord) {}

func (compileTimePlugin) RegisterCommandLine(context.Context, CommandLineRegistrationRequest) (CommandLineRegistrationResponse, error) {
	return CommandLineRegistrationResponse{}, nil
}

func (compileTimePlugin) ExecuteCommandLine(context.Context, CommandLineExecutionRequest) (CommandLineExecutionResponse, error) {
	return CommandLineExecutionResponse{}, nil
}

func (compileTimePlugin) RegisterManagement(context.Context, ManagementRegistrationRequest) (ManagementRegistrationResponse, error) {
	return ManagementRegistrationResponse{}, nil
}

func (compileTimePlugin) HandleManagement(context.Context, ManagementRequest) (ManagementResponse, error) {
	return ManagementResponse{}, nil
}
