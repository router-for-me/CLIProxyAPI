package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var removableUnsupportedParameters = map[string]struct{}{
	"frequency_penalty": {},
	"presence_penalty":  {},
	"seed":              {},
	"stop":              {},
	"temperature":       {},
	"top_k":             {},
	"top_p":             {},
}

func applyModelCapabilityPolicy(req coreexecutor.RequestAfterAuthInterceptRequest) coreexecutor.RequestAfterAuthInterceptResponse {
	modelID, info := providerModelInfo(req)
	if info == nil || len(req.Body) == 0 {
		return coreexecutor.RequestAfterAuthInterceptResponse{}
	}

	if limit := modelOutputTokenLimit(info); limit > 0 {
		for _, field := range outputTokenFields(req.SourceFormat.String()) {
			value := gjson.GetBytes(req.Body, field.path)
			if value.Type == gjson.Number && value.Int() > int64(limit) {
				return capabilityLimitError(req.Provider, modelID, field.parameter, value.Int(), limit)
			}
		}
	}
	if info.Thinking != nil && len(info.Thinking.Levels) > 0 {
		if parameter, effort := requestedReasoningEffort(req.SourceFormat.String(), req.Body); effort != "" && !containsCapabilityValue(info.Thinking.Levels, effort) {
			return capabilityRequestError(
				"unsupported_reasoning_effort",
				parameter,
				fmt.Sprintf("reasoning effort %q is not supported by %s/%s", effort, req.Provider, modelID),
			)
		}
	}

	body := req.Body
	changed := false
	for _, parameter := range info.UnsupportedParameters {
		parameter = strings.ToLower(strings.TrimSpace(parameter))
		if _, removable := removableUnsupportedParameters[parameter]; !removable {
			continue
		}
		for _, path := range unsupportedParameterPaths(req.SourceFormat.String(), parameter) {
			if !gjson.GetBytes(body, path).Exists() {
				continue
			}
			updated, errDelete := sjson.DeleteBytes(body, path)
			if errDelete != nil {
				continue
			}
			body = updated
			changed = true
		}
	}
	if !changed {
		return coreexecutor.RequestAfterAuthInterceptResponse{}
	}
	return coreexecutor.RequestAfterAuthInterceptResponse{Body: body}
}

func providerModelInfo(req coreexecutor.RequestAfterAuthInterceptRequest) (string, *registry.ModelInfo) {
	for _, candidate := range []string{req.Model, req.RequestedModel} {
		modelID := strings.TrimSpace(thinking.ParseSuffix(candidate).ModelName)
		if modelID == "" {
			continue
		}
		if info := registry.GetGlobalRegistry().GetProviderModelInfo(modelID, req.Provider); info != nil {
			return modelID, info
		}
	}
	return "", nil
}

func modelOutputTokenLimit(info *registry.ModelInfo) int {
	if info.MaxCompletionTokens > 0 {
		return info.MaxCompletionTokens
	}
	return info.OutputTokenLimit
}

type outputTokenField struct {
	parameter string
	path      string
}

func outputTokenFields(sourceFormat string) []outputTokenField {
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "openai", "openai-response", "responses":
		return []outputTokenField{
			{parameter: "max_output_tokens", path: "max_output_tokens"},
			{parameter: "max_completion_tokens", path: "max_completion_tokens"},
			{parameter: "max_tokens", path: "max_tokens"},
		}
	case "claude":
		return []outputTokenField{{parameter: "max_tokens", path: "max_tokens"}}
	case "gemini":
		return []outputTokenField{
			{parameter: "max_output_tokens", path: "generationConfig.maxOutputTokens"},
			{parameter: "max_output_tokens", path: "generation_config.max_output_tokens"},
		}
	default:
		return nil
	}
}

func unsupportedParameterPaths(sourceFormat, parameter string) []string {
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "openai", "openai-response", "responses", "claude":
		return []string{parameter}
	case "gemini":
		switch parameter {
		case "frequency_penalty":
			return []string{"generationConfig.frequencyPenalty", "generation_config.frequency_penalty"}
		case "presence_penalty":
			return []string{"generationConfig.presencePenalty", "generation_config.presence_penalty"}
		case "stop":
			return []string{"generationConfig.stopSequences", "generation_config.stop_sequences"}
		case "top_p":
			return []string{"generationConfig.topP", "generation_config.top_p"}
		case "top_k":
			return []string{"generationConfig.topK", "generation_config.top_k"}
		default:
			return []string{"generationConfig." + parameter, "generation_config." + parameter}
		}
	default:
		return nil
	}
}

func requestedReasoningEffort(sourceFormat string, body []byte) (string, string) {
	var paths []string
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "openai-response", "responses":
		paths = []string{"reasoning.effort", "reasoning_effort"}
	case "openai":
		paths = []string{"reasoning_effort", "reasoning.effort"}
	case "gemini":
		paths = []string{"generationConfig.thinkingConfig.thinkingLevel", "generation_config.thinking_config.thinking_level"}
	default:
		return "", ""
	}
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			return path, strings.ToLower(strings.TrimSpace(value.String()))
		}
	}
	return "", ""
}

func containsCapabilityValue(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func capabilityLimitError(provider, model, parameter string, requested int64, limit int) coreexecutor.RequestAfterAuthInterceptResponse {
	return capabilityRequestError(
		"max_output_tokens_exceeded",
		parameter,
		fmt.Sprintf("%s %d exceeds the %s/%s limit of %d", parameter, requested, provider, model, limit),
	)
}

func capabilityRequestError(code, parameter, message string) coreexecutor.RequestAfterAuthInterceptResponse {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"param":   parameter,
			"code":    code,
		},
	})
	return coreexecutor.RequestAfterAuthInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusBadRequest,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		ResponseBody:    body,
	}
}
