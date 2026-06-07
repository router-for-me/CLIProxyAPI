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
var _ ProviderExecutor = (*compileTimePlugin)(nil)
var _ HostHTTPClient = (*compileTimePlugin)(nil)
var _ RequestTranslator = (*compileTimePlugin)(nil)
var _ RequestNormalizer = (*compileTimePlugin)(nil)
var _ ResponseTranslator = (*compileTimePlugin)(nil)
var _ ResponseNormalizer = (*compileTimePlugin)(nil)
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

func TestManagementRouteMenuFieldsExposeManagementUIHints(t *testing.T) {
	route := ManagementRoute{
		Method:      "GET",
		Path:        "/plugins/example/status",
		Menu:        "Example Status",
		Description: "Shows example plugin status.",
		Handler:     compileTimePlugin{},
	}
	if route.Menu == "" || route.Description == "" {
		t.Fatalf("management route missing menu fields: %#v", route)
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
