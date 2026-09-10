package main

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// HTTPStatus carries the status the host renders onto the client response.
	HTTPStatus int `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar        bool                         `json:"model_registrar"`
	ModelRouter           bool                         `json:"model_router"`
	UsagePlugin           bool                         `json:"usage_plugin"`
	Executor              bool                         `json:"executor,omitempty"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope,omitempty"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// handleMethod dispatches one host call. Lifecycle methods compile the supplied
// configuration and answer with the capabilities that configuration implies, and
// every executor work method answers with the held-position signal.
func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req lifecycleRequest
		if len(request) > 0 {
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
			}
		}
		if errConfigure := runtimePlugin.configure(req.ConfigYAML); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration(runtimePlugin.loadedConfig()))
	case pluginabi.MethodModelRegister:
		return okEnvelope(runtimePlugin.registeredModels())
	case pluginabi.MethodModelRoute:
		var req rpcModelRouteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode model route request: %w", errUnmarshal)
		}
		return okEnvelope(runtimePlugin.routeWithCallback(req.ModelRouteRequest, req.HostCallbackID))
	case pluginabi.MethodUsageHandle:
		var record pluginapi.UsageRecord
		if errUnmarshal := json.Unmarshal(request, &record); errUnmarshal != nil {
			return nil, fmt.Errorf("decode usage record: %w", errUnmarshal)
		}
		runtimePlugin.handleUsage(record)
		return okEnvelope(map[string]any{})
	case pluginabi.MethodExecutorIdentifier:
		return executorIdentifierResult()
	case pluginabi.MethodExecutorExecute,
		pluginabi.MethodExecutorExecuteStream,
		pluginabi.MethodExecutorCountTokens,
		pluginabi.MethodExecutorHTTPRequest:
		return unavailableEnvelope(), nil
	case pluginabi.MethodPluginShutdown:
		runtimePlugin.shutdown()
		return okEnvelope(map[string]any{})
	default:
		return errorEnvelope(envelopeError{Code: "unknown_method", Message: "unknown method: " + method}), nil
	}
}

// pluginRegistration describes the configured plugin capabilities. The executor
// declaration exists only when routing can self-target an unavailable position.
func pluginRegistration(cfg *compiledConfig) registration {
	capabilities := registrationCapability{ModelRegistrar: true, ModelRouter: true, UsagePlugin: true}
	if cfg != nil && cfg.UnavailableProvider == unavailableError {
		// A host treats a format mismatch as unhandled and lets a self-targeted
		// route fall through, so the declaration names every entry protocol.
		formats := []string{
			string(translator.FormatOpenAI),
			string(translator.FormatOpenAIResponse),
			string(translator.FormatClaude),
			string(translator.FormatGemini),
			string(translator.FormatCodex),
			string(translator.FormatAntigravity),
			string(translator.FormatInteractions),
		}
		capabilities.Executor = true
		capabilities.ExecutorModelScope = pluginapi.ExecutorModelScopeStatic
		capabilities.ExecutorInputFormats = formats
		capabilities.ExecutorOutputFormats = formats
	}
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginIdentifier,
			Version:          pluginVersion,
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable per-conversation model sequence routing."},
				{Name: "session_ttl", Type: pluginapi.ConfigFieldTypeString, Description: "Sliding in-memory conversation cursor TTL (1m through 24h)."},
				{Name: "unavailable_provider", Type: pluginapi.ConfigFieldTypeString, Description: "Skip positions whose provider is not registered, or answer a retryable HTTP 529 without consuming a position."},
				{Name: "diagnostics", Type: pluginapi.ConfigFieldTypeObject, Description: "Bounded content-free JSONL routing and cache diagnostics."},
				{Name: "aliases", Type: pluginapi.ConfigFieldTypeArray, Description: "Client-visible aliases and ordered provider/model target sequences."},
			},
		},
		Capabilities: capabilities,
	}
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

// errorEnvelope renders one failed reply. A detail carrying a positive HTTP
// status reaches the client with that status.
func errorEnvelope(detail envelopeError) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &detail})
	return raw
}
