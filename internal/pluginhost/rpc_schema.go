package pluginhost

import (
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type rpcLifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type rpcRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  rpcCapabilities    `json:"capabilities"`
}

type rpcCapabilities struct {
	ModelRegistrar                bool                         `json:"model_registrar"`
	ModelProvider                 bool                         `json:"model_provider"`
	AuthProvider                  bool                         `json:"auth_provider"`
	FrontendAuthProvider          bool                         `json:"frontend_auth_provider"`
	FrontendAuthProviderExclusive bool                         `json:"frontend_auth_provider_exclusive"`
	Scheduler                     bool                         `json:"scheduler"`
	ModelRouter                   bool                         `json:"model_router"`
	Executor                      bool                         `json:"executor"`
	ExecutorModelScope            pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats          []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats         []string                     `json:"executor_output_formats,omitempty"`
	RequestTranslator             bool                         `json:"request_translator"`
	RequestNormalizer             bool                         `json:"request_normalizer"`
	RequestInterceptor            bool                         `json:"request_interceptor"`
	ResponseTranslator            bool                         `json:"response_translator"`
	ResponseBeforeTranslator      bool                         `json:"response_before_translator"`
	ResponseAfterTranslator       bool                         `json:"response_after_translator"`
	ResponseInterceptor           bool                         `json:"response_interceptor"`
	StreamChunkInterceptor        bool                         `json:"response_stream_interceptor"`
	ThinkingApplier               bool                         `json:"thinking_applier"`
	UsagePlugin                   bool                         `json:"usage_plugin"`
	CommandLinePlugin             bool                         `json:"command_line_plugin"`
	ManagementAPI                 bool                         `json:"management_api"`
}

type rpcIdentifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcExecutorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type rpcAuthLoginStartRequest struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthLoginPollRequest struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthModelRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcExecutorHTTPRequest struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcRequestInterceptRequest struct {
	pluginapi.RequestInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcResponseInterceptRequest struct {
	pluginapi.ResponseInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcStreamChunkInterceptRequest struct {
	pluginapi.StreamChunkInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcThinkingApplyRequest struct {
	pluginapi.ThinkingApplyRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcManagementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

type rpcEmptyResponse struct{}

func rpcCapabilitiesFromPlugin(plugin pluginapi.Plugin) rpcCapabilities {
	caps := plugin.Capabilities
	return rpcCapabilities{
		ModelRegistrar:                caps.ModelRegistrar != nil,
		ModelProvider:                 caps.ModelProvider != nil,
		AuthProvider:                  caps.AuthProvider != nil,
		FrontendAuthProvider:          caps.FrontendAuthProvider != nil,
		FrontendAuthProviderExclusive: caps.FrontendAuthProvider != nil && caps.FrontendAuthProviderExclusive,
		Scheduler:                     caps.Scheduler != nil,
		ModelRouter:                   caps.ModelRouter != nil,
		Executor:                      caps.Executor != nil,
		ExecutorModelScope:            normalizedExecutorModelScope(caps),
		ExecutorInputFormats:          append([]string(nil), caps.ExecutorInputFormats...),
		ExecutorOutputFormats:         append([]string(nil), caps.ExecutorOutputFormats...),
		RequestTranslator:             caps.RequestTranslator != nil,
		RequestNormalizer:             caps.RequestNormalizer != nil,
		RequestInterceptor:            caps.RequestInterceptor != nil,
		ResponseTranslator:            caps.ResponseTranslator != nil,
		ResponseBeforeTranslator:      caps.ResponseBeforeTranslator != nil,
		ResponseAfterTranslator:       caps.ResponseAfterTranslator != nil,
		ResponseInterceptor:           caps.ResponseInterceptor != nil,
		StreamChunkInterceptor:        caps.StreamChunkInterceptor != nil,
		ThinkingApplier:               caps.ThinkingApplier != nil,
		UsagePlugin:                   caps.UsagePlugin != nil,
		CommandLinePlugin:             caps.CommandLinePlugin != nil,
		ManagementAPI:                 caps.ManagementAPI != nil,
	}
}

func marshalRPCResult(v any) ([]byte, error) {
	result, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return marshalRPCEnvelope(json.RawMessage(result))
}
