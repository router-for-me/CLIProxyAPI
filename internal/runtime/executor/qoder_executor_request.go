package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	qoderauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type qoderPreparedRequest struct {
	httpRequest   *http.Request
	openAIRequest []byte
}

func (e *QoderExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, reporter *helps.UsageReporter) (*qoderPreparedRequest, error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "qoder: responses/compact is not supported"}
	}
	storage, err := qoderauth.CredentialFromAuth(auth)
	if err != nil {
		return nil, err
	}
	if storage.UserID == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "qoder: user id is missing"}
	}
	model := normalizeQoderModel(req.Model)
	if model == "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "qoder: model is required"}
	}
	reporter.SetUpstreamModel(model)
	provider := qoderauth.ProviderForRegion(storage.Region)

	from := opts.SourceFormat
	if from == "" {
		from = sdktranslator.FormatOpenAI
	}
	originalSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalSource = opts.OriginalRequest
	}
	original := bytes.Clone(originalSource)
	openAIRequest := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, sdktranslator.FormatOpenAI, model, bytes.Clone(req.Payload), true)
	openAIRequest, err = helps.ApplyRequestThinking(openAIRequest, req, opts, from.String(), provider, provider)
	if err != nil {
		return nil, err
	}
	reporter.SetTranslatedReasoningEffort(openAIRequest, sdktranslator.FormatOpenAI.String())

	var chatRequest map[string]any
	decoder := json.NewDecoder(bytes.NewReader(openAIRequest))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&chatRequest); errDecode != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: "qoder: invalid chat request"}
	}
	modelConfig, err := e.resolveModelConfig(ctx, auth, storage, model)
	if err != nil {
		return nil, err
	}
	messages, systemText := normalizeQoderMessages(chatRequest["messages"])
	lastUser := qoderLastUserText(messages)
	maxTokens := qoderMaxTokens(chatRequest, modelConfig)
	effort, disabled := resolveQoderEffort(chatRequest, modelConfig)
	isReasoning, _ := modelConfig["is_reasoning"].(bool)
	if disabled {
		isReasoning = false
	} else if effort != "" {
		isReasoning = true
	}
	modelConfig["key"] = model
	if _, exists := modelConfig["display_name"]; !exists {
		modelConfig["display_name"] = model
	}
	if _, exists := modelConfig["source"]; !exists {
		modelConfig["source"] = "system"
	}
	if _, exists := modelConfig["format"]; !exists {
		modelConfig["format"] = "openai"
	}
	modelConfig["is_reasoning"] = isReasoning

	parameters := qoderParameters(chatRequest, maxTokens, effort, disabled)
	requestBody := map[string]any{
		"request_id":       uuid.NewString(),
		"request_set_id":   uuid.NewString(),
		"chat_record_id":   uuid.NewString(),
		"session_id":       uuid.NewString(),
		"stream":           true,
		"chat_task":        "FREE_INPUT",
		"is_reply":         false,
		"is_retry":         false,
		"source":           1,
		"version":          "3",
		"session_type":     "qodercli",
		"agent_id":         "agent_common",
		"task_id":          "common",
		"aliyun_user_type": "",
		"system":           systemText,
		"messages":         messages,
		"tools":            qoderRawOrEmpty(chatRequest["tools"]),
		"parameters":       parameters,
		"chat_context": map[string]any{
			"text":       map[string]any{"type": "text", "text": lastUser},
			"features":   []any{},
			"chatPrompt": "",
			"imageUrls":  nil,
			"extra": map[string]any{
				"modelConfig":     map[string]any{"key": model, "is_reasoning": isReasoning},
				"originalContent": map[string]any{"type": "text", "text": lastUser},
			},
		},
		"model_config": modelConfig,
		"custom_model": nil,
		"business": map[string]any{
			"product":  "cli",
			"version":  qoderauth.ClientVersion,
			"type":     "agent",
			"id":       uuid.NewString(),
			"name":     truncateQoderText(lastUser, 40),
			"begin_at": time.Now().UnixMilli(),
			"stage":    "start",
		},
	}
	businessBody, err := qoderauth.MarshalWire(requestBody)
	if err != nil {
		return nil, fmt.Errorf("qoder executor: marshal request: %w", err)
	}
	businessBody = helps.ApplyPayloadConfigWithRequest(e.cfg, model, provider, from.String(), "", businessBody, original, helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)
	encodedBody := []byte(qoderauth.EncodeBody(businessBody))
	endpoints, err := qoderauth.ResolveEndpoints(storage.Region)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoints.ChatURL(), bytes.NewReader(encodedBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if errSign := qoderauth.SignRequest(httpReq, encodedBody, storage); errSign != nil {
		return nil, errSign
	}
	finalModel := gjson.GetBytes(businessBody, "model_config.key").String()
	if finalModel != "" {
		reporter.SetUpstreamModel(finalModel)
		httpReq.Header.Set("X-Model-Key", finalModel)
	} else {
		httpReq.Header.Del("X-Model-Key")
	}
	modelSource := gjson.GetBytes(businessBody, "model_config.source").String()
	if modelSource != "" {
		httpReq.Header.Set("X-Model-Source", modelSource)
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       httpReq.URL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      businessBody,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	return &qoderPreparedRequest{httpRequest: httpReq, openAIRequest: openAIRequest}, nil
}

func (e *QoderExecutor) resolveModelConfig(ctx context.Context, auth *cliproxyauth.Auth, storage *qoderauth.TokenStorage, model string) (map[string]any, error) {
	config, err := qoderauth.CachedModelConfig(auth.ID, model)
	if err == nil {
		return config, nil
	}
	if errors.Is(err, qoderauth.ErrModelNotInCatalog) {
		return nil, statusErr{code: http.StatusNotFound, msg: fmt.Sprintf("qoder: model %q is not available for this account", model)}
	}
	if !errors.Is(err, qoderauth.ErrCatalogNotCached) {
		return nil, err
	}
	catalog, errFetch := qoderauth.FetchCatalog(ctx, helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), auth.ID, storage)
	if errFetch != nil {
		return nil, fmt.Errorf("qoder: load account model catalog: %w", errFetch)
	}
	raw, exists := catalog.Configs[model]
	if !exists {
		return nil, statusErr{code: http.StatusNotFound, msg: fmt.Sprintf("qoder: model %q is not available for this account", model)}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&config); errDecode != nil {
		return nil, fmt.Errorf("qoder: decode model config: %w", errDecode)
	}
	return config, nil
}

func normalizeQoderModel(model string) string {
	model = strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	for _, prefix := range []string{"qoder-cn/", "qoder/"} {
		if strings.HasPrefix(strings.ToLower(model), prefix) {
			return strings.TrimSpace(model[len(prefix):])
		}
	}
	return model
}

func normalizeQoderMessages(raw any) ([]any, string) {
	items, _ := raw.([]any)
	messages := make([]any, 0, len(items)+1)
	systemParts := make([]string, 0)
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if role == "system" || role == "developer" {
			if text := qoderContentText(message["content"]); text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		clone := make(map[string]any, len(message)+1)
		for key, value := range message {
			clone[key] = value
		}
		if len(qoderToolCalls(clone["tool_calls"])) > 0 && clone["content"] == nil {
			clone["content"] = ""
		}
		if role != "tool" {
			if contents := qoderContents(clone["content"]); len(contents) > 0 {
				clone["contents"] = contents
			}
		}
		messages = append(messages, clone)
	}
	systemText := strings.Join(systemParts, "\n\n")
	if systemText != "" {
		messages = append([]any{map[string]any{"role": "system", "content": systemText, "contents": qoderContents(systemText)}}, messages...)
	}
	return messages, systemText
}

func qoderContents(content any) []any {
	switch typed := content.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": typed}}
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			switch partType {
			case "text", "input_text":
				if text, ok := part["text"].(string); ok {
					out = append(out, map[string]any{"type": "text", "text": text})
				}
			case "image_url", "input_image":
				if image := part["image_url"]; image != nil {
					out = append(out, map[string]any{"type": "image_url", "image_url": image})
				} else if image := part["image"]; image != nil {
					out = append(out, map[string]any{"type": "image_url", "image_url": image})
				}
			}
		}
		return out
	default:
		return nil
	}
}

func qoderContentText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if part, ok := item.(map[string]any); ok {
				if text, ok := part["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func qoderLastUserText(messages []any) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message, ok := messages[index].(map[string]any)
		if !ok || message["role"] != "user" {
			continue
		}
		return qoderContentText(message["content"])
	}
	return ""
}

func qoderMaxTokens(request map[string]any, config map[string]any) int {
	maxTokens := 32000
	if configured := qoderPositiveInt(config["max_output_tokens"]); configured > 0 {
		maxTokens = configured
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if requested := qoderPositiveInt(request[key]); requested > 0 && requested < maxTokens {
			maxTokens = requested
		}
	}
	return maxTokens
}

func qoderParameters(request map[string]any, maxTokens int, effort string, disabled bool) map[string]any {
	parameters := map[string]any{"max_tokens": maxTokens}
	for _, key := range []string{"temperature", "top_p", "stop", "response_format", "reasoning_budget_tokens", "context_length", "max_input_tokens", "tool_choice", "parallel_tool_calls"} {
		if value, exists := request[key]; value != nil && exists {
			parameters[key] = value
		}
	}
	if effort != "" && effort != "none" {
		parameters["reasoning_effort"] = effort
		parameters["enable_thinking"] = true
	} else if disabled {
		parameters["enable_thinking"] = false
	}
	return parameters
}

func resolveQoderEffort(request map[string]any, config map[string]any) (string, bool) {
	requested := strings.ToLower(strings.TrimSpace(stringValue(request["reasoning_effort"])))
	if requested == "" {
		return "", false
	}
	if requested == "none" {
		return "", true
	}
	levels := qoderModelEfforts(config)
	if len(levels) == 0 {
		return requested, false
	}
	for _, level := range levels {
		if level == requested {
			return requested, false
		}
	}
	order := []string{"minimal", "low", "medium", "high", "xhigh", "max"}
	requestedRank := -1
	for index, level := range order {
		if level == requested {
			requestedRank = index
			break
		}
	}
	if requestedRank < 0 {
		return requested, false
	}
	for _, level := range levels {
		for index, candidate := range order {
			if level == candidate && index > requestedRank {
				return level, false
			}
		}
	}
	return levels[len(levels)-1], false
}

func qoderModelEfforts(config map[string]any) []string {
	thinkingConfig, _ := config["thinking_config"].(map[string]any)
	enabled, _ := thinkingConfig["enabled"].(map[string]any)
	efforts, _ := enabled["efforts"].(map[string]any)
	levels := make([]string, 0, len(efforts))
	for _, level := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		if _, exists := efforts[level]; exists {
			levels = append(levels, level)
		}
	}
	return levels
}

func qoderPositiveInt(value any) int {
	number := int(qoderNumber(value))
	if number > 0 {
		return number
	}
	return 0
}

func qoderNumber(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func qoderToolCalls(value any) []any {
	calls, _ := value.([]any)
	return calls
}

func qoderRawOrEmpty(value any) any {
	if value == nil {
		return []any{}
	}
	return value
}

func truncateQoderText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}
