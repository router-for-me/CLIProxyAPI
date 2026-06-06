package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Register is called once when the host first loads this .so file.
func Register(configYAML []byte) pluginapi.Plugin {
	return buildPlugin(configYAML)
}

// Reconfigure is called on config hot reload while this plugin remains enabled.
func Reconfigure(configYAML []byte) pluginapi.Plugin {
	return buildPlugin(configYAML)
}

func buildPlugin(configYAML []byte) pluginapi.Plugin {
	example := &examplePlugin{configYAML: append([]byte(nil), configYAML...)}
	return pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             "example",
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "config1",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Enables the example boolean option.",
				},
				{
					Name:        "config2",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Stores the example string option.",
				},
				{
					Name:        "config3",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Stores the example integer option.",
				},
				{
					Name:        "mode",
					Type:        pluginapi.ConfigFieldTypeEnum,
					EnumValues:  []string{"safe", "fast"},
					Description: "Selects the example execution mode.",
				},
			},
		},
		Capabilities: pluginapi.Capabilities{
			ModelProvider:            example,
			AuthProvider:             example,
			FrontendAuthProvider:     example,
			Executor:                 example,
			ExecutorModelScope:       pluginapi.ExecutorModelScopeBoth,
			RequestTranslator:        example,
			RequestNormalizer:        example,
			ResponseTranslator:       example,
			ResponseBeforeTranslator: example,
			ResponseAfterTranslator:  example,
			ThinkingApplier:          example,
			UsagePlugin:              example,
			CommandLinePlugin:        example,
			ManagementAPI:            example,
		},
	}
}

type examplePlugin struct {
	configYAML []byte
	mu         sync.Mutex
	usageCount int64
}

var _ pluginapi.AuthProvider = (*examplePlugin)(nil)
var _ pluginapi.ModelProvider = (*examplePlugin)(nil)
var _ pluginapi.ProviderExecutor = (*examplePlugin)(nil)
var _ pluginapi.ThinkingApplier = (*examplePlugin)(nil)

// Native logic always has higher priority than plugin logic.
// Native model registration always runs before plugin model discovery.
// Executor-backed plugin models can be static, OAuth auth-bound, or both.
func (p *examplePlugin) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{
		Provider: "plugin-example",
		Models: []pluginapi.ModelInfo{{
			ID:                         "plugin-example-model",
			Object:                     "model",
			OwnedBy:                    "plugin-example",
			Type:                       "chat",
			DisplayName:                "Plugin Example Model",
			Name:                       "plugin-example-model",
			Version:                    "0.1.0",
			Description:                "Deterministic example model provided by a Go dynamic plugin.",
			InputTokenLimit:            4096,
			OutputTokenLimit:           1024,
			SupportedGenerationMethods: []string{"generateContent", "chat.completions"},
			ContextLength:              4096,
			MaxCompletionTokens:        1024,
			SupportedParameters:        []string{"model", "messages", "stream", "thinking", "reasoning_effort"},
			SupportedInputModalities:   []string{"text"},
			SupportedOutputModalities:  []string{"text"},
			Thinking:                   &pluginapi.ThinkingSupport{ZeroAllowed: true, DynamicAllowed: true},
			UserDefined:                true,
		}},
	}, nil
}

func (p *examplePlugin) ModelsForAuth(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return p.StaticModels(ctx, pluginapi.StaticModelRequest{Plugin: req.Plugin, Host: req.Host})
}

func (p *examplePlugin) Identifier() string {
	return "plugin-example"
}

func (p *examplePlugin) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	if !strings.EqualFold(req.Provider, "plugin-example") {
		return pluginapi.AuthParseResponse{}, nil
	}
	return pluginapi.AuthParseResponse{
		Handled: true,
		Auth: pluginapi.AuthData{
			Provider:    "plugin-example",
			ID:          req.FileName,
			FileName:    req.FileName,
			Label:       "Plugin Example",
			StorageJSON: append([]byte(nil), req.RawJSON...),
			Metadata: map[string]any{
				"type": "plugin-example",
			},
		},
	}, nil
}

func (p *examplePlugin) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("plugin-example login is not interactive")
}

func (p *examplePlugin) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "plugin-example login is not interactive"}, nil
}

func (p *examplePlugin) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return pluginapi.AuthRefreshResponse{
		Auth: pluginapi.AuthData{
			Provider:    req.AuthProvider,
			ID:          req.AuthID,
			StorageJSON: append([]byte(nil), req.StorageJSON...),
			Metadata:    cloneAnyMap(req.Metadata),
			Attributes:  cloneStringMap(req.Attributes),
		},
	}, nil
}

// A plugin can register multiple command-line flags.
// Flags are registered by priority. Existing native flags, reserved help/h flags,
// or higher-priority plugin flags win and cannot be registered again.
func (p *examplePlugin) RegisterCommandLine(context.Context, pluginapi.CommandLineRegistrationRequest) (pluginapi.CommandLineRegistrationResponse, error) {
	return pluginapi.CommandLineRegistrationResponse{
		Flags: []pluginapi.CommandLineFlag{
			{
				Name:         "plugin-example-command",
				Usage:        "Run the example plugin command-line handler",
				Type:         "bool",
				DefaultValue: "false",
			},
			{
				Name:         "plugin-example-message",
				Usage:        "Message passed to the example plugin command-line handler",
				Type:         "string",
				DefaultValue: "hello",
			},
		},
	}, nil
}

// Global plugins.enabled=false or per-plugin enabled=false skips command-line execution after reload.
// The host passes every command-line argument and all triggered plugin flags to ExecuteCommandLine.
func (p *examplePlugin) ExecuteCommandLine(ctx context.Context, req pluginapi.CommandLineExecutionRequest) (pluginapi.CommandLineExecutionResponse, error) {
	message := req.Flags["plugin-example-message"].Value
	if triggeredMessage, ok := req.TriggeredFlags["plugin-example-message"]; ok {
		message = triggeredMessage.Value
	}
	return pluginapi.CommandLineExecutionResponse{
		Stdout: []byte(fmt.Sprintf("example plugin command executed with %d argument(s), message=%q\n", len(req.Args), message)),
	}, nil
}

// A plugin can register multiple Management API routes.
// Management API routes are exact routes under /v0/management/ and cannot override
// native routes or higher-priority plugin routes that are already registered.
func (p *examplePlugin) RegisterManagement(context.Context, pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{
				Method:      http.MethodGet,
				Path:        "/plugins/example/status",
				Menu:        "Example Status",
				Description: "Shows example plugin runtime status.",
				Handler:     p,
			},
			{
				Method:      http.MethodGet,
				Path:        "/plugins/example/capabilities",
				Menu:        "Example Capabilities",
				Description: "Shows example plugin capability details.",
				Handler:     p,
			},
		},
	}, nil
}

// Plugin Management API routes still require the normal Management API key,
// and are skipped when Home mode or Management API availability disables them.
func (p *examplePlugin) HandleManagement(ctx context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	p.mu.Lock()
	usageCount := p.usageCount
	p.mu.Unlock()

	body := []byte(fmt.Sprintf(`{"plugin":"example","usage_count":%d}`+"\n", usageCount))
	if strings.HasSuffix(req.Path, "/capabilities") {
		body = []byte(`{"plugin":"example","capabilities":["command-line","management-api","auth-provider","model-provider","frontend-auth","executor","raw-http","request-translator","request-normalizer","response-translator","response-normalizer","thinking-applier","usage"]}` + "\n")
	}

	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: body,
	}, nil
}

// Global plugins.enabled=false or per-plugin enabled=false skips plugin execution after reload.
func (p *examplePlugin) Authenticate(ctx context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
	authenticated := req.Headers.Get("X-Plugin-Example") == "allow"
	if !authenticated {
		return pluginapi.FrontendAuthResponse{}, nil
	}

	return pluginapi.FrontendAuthResponse{
		Authenticated: true,
		Principal:     "plugin-example-user",
		Metadata: map[string]string{
			"provider": "plugin-example",
		},
	}, nil
}

// A plugin executor runs only for a matching auth when no native executor owns the provider.
func (p *examplePlugin) Execute(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{
		Payload: []byte(`{"id":"plugin-example-response","object":"chat.completion","model":"plugin-example-model","choices":[{"index":0,"message":{"role":"assistant","content":"plugin example response"},"finish_reason":"stop"}]}`),
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Metadata: map[string]any{
			"provider": "plugin-example",
		},
	}, nil
}

func (p *examplePlugin) ExecuteStream(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
	chunks <- pluginapi.ExecutorStreamChunk{
		Payload: []byte(`{"id":"plugin-example-stream","object":"chat.completion.chunk","model":"plugin-example-model","choices":[{"index":0,"delta":{"content":"plugin example response"},"finish_reason":"stop"}]}`),
	}
	close(chunks)

	return pluginapi.ExecutorStreamResponse{
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Chunks: chunks,
	}, nil
}

func (p *examplePlugin) CountTokens(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{
		Payload: []byte(`{"input_tokens":0,"output_tokens":0,"total_tokens":0}`),
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}, nil
}

func (p *examplePlugin) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	resp, errDo := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{
		Method:  req.Method,
		URL:     req.URL,
		Headers: req.Headers,
		Body:    req.Body,
	})
	if errDo != nil {
		return pluginapi.ExecutorHTTPResponse{}, errDo
	}
	return pluginapi.ExecutorHTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Headers,
		Body:       resp.Body,
	}, nil
}

// Request/response translators run only when no native translator exists, and only the highest-priority plugin translator runs once.
func (p *examplePlugin) TranslateRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return payloadOrEmptyObject(req.Body), nil
}

// Normalizers run from higher priority to lower priority and are chained.
func (p *examplePlugin) NormalizeRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return payloadOrEmptyObject(req.Body), nil
}

func (p *examplePlugin) TranslateResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return payloadOrEmptyObject(req.Body), nil
}

func (p *examplePlugin) NormalizeResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return payloadOrEmptyObject(req.Body), nil
}

func (p *examplePlugin) ApplyThinking(ctx context.Context, req pluginapi.ThinkingApplyRequest) (pluginapi.PayloadResponse, error) {
	var payload map[string]any
	if len(req.Body) == 0 {
		payload = map[string]any{}
	} else if errUnmarshal := json.Unmarshal(req.Body, &payload); errUnmarshal != nil {
		return pluginapi.PayloadResponse{}, errUnmarshal
	}
	payload["plugin_example_thinking"] = map[string]any{
		"mode":   req.Config.Mode,
		"budget": req.Config.Budget,
		"level":  req.Config.Level,
	}
	out, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return pluginapi.PayloadResponse{}, errMarshal
	}
	return pluginapi.PayloadResponse{Body: out}, nil
}

// If any plugin method panics, host disables that plugin for current process lifetime and never calls it again until restart.
func (p *examplePlugin) HandleUsage(ctx context.Context, record pluginapi.UsageRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.usageCount++
}

func payloadOrEmptyObject(body []byte) pluginapi.PayloadResponse {
	if len(body) == 0 {
		return pluginapi.PayloadResponse{Body: []byte(`{}`)}
	}

	return pluginapi.PayloadResponse{Body: append([]byte(nil), body...)}
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = cloneAnyValue(value)
	}
	return dst
}

func cloneAnyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAnyMap(typed)
	case map[string]string:
		return cloneStringMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAnyValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case http.Header:
		return typed.Clone()
	case url.Values:
		return cloneValues(typed)
	default:
		return value
	}
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneValues(src url.Values) url.Values {
	if len(src) == 0 {
		return nil
	}
	dst := make(url.Values, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}
