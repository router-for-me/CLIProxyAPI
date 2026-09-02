package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	providerHistoryTaintedPrefix      = "tainted:"
	providerHistoryTaintedMetadataKey = "provider_history_tainted"
)

var providerBoundHistoryFields = [...]string{"id", "encrypted_content", "provider_item_id"}

var providerHistoryRecoverableHostOutputNames = map[string]struct{}{
	"automation_update": {},
}

type providerHistoryNormalization struct {
	Body           []byte
	Changed        bool
	DroppedItems   int
	StrippedFields int
}

type providerHistoryError struct {
	reason string
}

func (e *providerHistoryError) Error() string {
	return fmt.Sprintf("provider-bound response history cannot be replayed safely: %s", e.reason)
}

func (*providerHistoryError) IsRequestScoped() bool { return true }

func (*providerHistoryError) StatusCode() int { return http.StatusConflict }

func normalizeProviderBoundResponseHistory(body []byte) (providerHistoryNormalization, error) {
	result := providerHistoryNormalization{Body: bytes.Clone(body)}
	if len(bytes.TrimSpace(body)) == 0 {
		return result, nil
	}

	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return providerHistoryNormalization{}, &providerHistoryError{reason: "invalid_json"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return providerHistoryNormalization{}, &providerHistoryError{reason: "invalid_json"}
	}
	if providerHistoryHasCredentialBoundToolResource(request["tools"]) {
		return providerHistoryNormalization{}, &providerHistoryError{reason: "foreign_tool_resource_requires_rehydration"}
	}
	input, exists := request["input"]
	if !exists {
		return result, nil
	}
	items, ok := input.([]any)
	if !ok {
		// String input has no replayable provider identity.
		if _, isString := input.(string); isString {
			return result, nil
		}
		return providerHistoryNormalization{}, &providerHistoryError{reason: "invalid_input_history"}
	}

	normalized := make([]any, 0, len(items))
	for _, rawItem := range items {
		item, okItem := rawItem.(map[string]any)
		if !okItem {
			return providerHistoryNormalization{}, &providerHistoryError{reason: "invalid_history_item"}
		}
		itemType, _ := item["type"].(string)
		if itemType == "" {
			if _, hasRole := item["role"].(string); hasRole {
				itemType = "message"
			}
		}
		if providerHistoryHasCredentialBoundFileReference(item) {
			return providerHistoryNormalization{}, &providerHistoryError{reason: "foreign_file_reference_requires_rehydration"}
		}
		switch itemType {
		case "message", "reasoning", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output", "additional_tools":
		case "compaction":
			return providerHistoryNormalization{}, &providerHistoryError{reason: "foreign_compaction_requires_rehydration"}
		default:
			return providerHistoryNormalization{}, &providerHistoryError{reason: "unsupported_history_item"}
		}
		if providerHistoryIsRecoverableOrphanHostOutput(item, items) {
			result.Changed = true
			result.DroppedItems++
			continue
		}

		for _, field := range providerBoundHistoryFields {
			if _, present := item[field]; present {
				delete(item, field)
				result.Changed = true
				result.StrippedFields++
			}
		}
		if itemType == "reasoning" && !hasSemanticHistoryValue(item["summary"]) {
			result.Changed = true
			result.DroppedItems++
			continue
		}
		normalized = append(normalized, item)
	}

	if reason := providerHistoryToolPairError(normalized); reason != "" {
		return providerHistoryNormalization{}, &providerHistoryError{reason: reason}
	}
	if len(normalized) == 0 && len(items) > 0 {
		return providerHistoryNormalization{}, &providerHistoryError{reason: "no_provider_neutral_history"}
	}
	if !result.Changed {
		return result, nil
	}

	request["input"] = normalized
	normalizedBody, err := json.Marshal(request)
	if err != nil {
		return providerHistoryNormalization{}, &providerHistoryError{reason: "encode_normalized_history"}
	}
	result.Body = normalizedBody
	return result, nil
}

func providerHistoryHasCredentialBoundToolResource(value any) bool {
	tools, ok := value.([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		tool, okTool := rawTool.(map[string]any)
		if !okTool {
			continue
		}
		toolType, _ := tool["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(toolType), "file_search") {
			continue
		}
		if hasSemanticHistoryValue(tool["vector_store_id"]) || hasSemanticHistoryValue(tool["vector_store_ids"]) {
			return true
		}
	}
	return false
}

func providerHistoryIsRecoverableOrphanHostOutput(item map[string]any, items []any) bool {
	itemType, _ := item["type"].(string)
	if itemType != "function_call_output" {
		return false
	}
	callID, _ := item["call_id"].(string)
	if strings.TrimSpace(callID) != "" {
		return false
	}
	name, _ := item["name"].(string)
	name = strings.TrimSpace(name)
	if _, ok := providerHistoryRecoverableHostOutputNames[name]; !ok {
		return false
	}
	for _, rawItem := range items {
		candidate, _ := rawItem.(map[string]any)
		candidateType, _ := candidate["type"].(string)
		candidateName, _ := candidate["name"].(string)
		if candidateType == "function_call" && strings.TrimSpace(candidateName) == name {
			return false
		}
	}
	return true
}

func hasSemanticHistoryValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		for _, item := range typed {
			if hasSemanticHistoryValue(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if hasSemanticHistoryValue(item) {
				return true
			}
		}
	default:
		return value != nil
	}
	return false
}

func providerHistoryHasCredentialBoundFileReference(value any) bool {
	return providerHistoryHasCredentialBoundFileReferenceInContext(value, false)
}

func providerHistoryHasCredentialBoundFileReferenceInContext(value any, fileContext bool) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if providerHistoryHasCredentialBoundFileReferenceInContext(item, fileContext) {
				return true
			}
		}
	case map[string]any:
		itemType, _ := typed["type"].(string)
		switch strings.ToLower(strings.TrimSpace(itemType)) {
		case "file", "image", "image_url", "input_file", "input_image":
			fileContext = true
		}
		if fileContext {
			if fileID, _ := typed["file_id"].(string); strings.TrimSpace(fileID) != "" {
				return true
			}
		}
		for key, item := range typed {
			childFileContext := fileContext || key == "file" || key == "image_url"
			if providerHistoryHasCredentialBoundFileReferenceInContext(item, childFileContext) {
				return true
			}
		}
	}
	return false
}

func providerHistoryToolPairError(items []any) string {
	type toolPair struct {
		family string
		callID string
	}
	calls := make(map[toolPair]int)
	outputs := make(map[toolPair]int)
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		itemType, _ := item["type"].(string)
		family := ""
		isCall := false
		switch itemType {
		case "function_call":
			family, isCall = "function", true
		case "function_call_output":
			family = "function"
		case "custom_tool_call":
			family, isCall = "custom", true
		case "custom_tool_call_output":
			family = "custom"
		default:
			continue
		}
		callID, _ := item["call_id"].(string)
		if strings.TrimSpace(callID) == "" {
			return "missing_call_id"
		}
		pair := toolPair{family: family, callID: callID}
		if isCall {
			calls[pair]++
		} else {
			outputs[pair]++
		}
	}
	if len(calls) != len(outputs) {
		return "unpaired_tool_history"
	}
	for pair, count := range calls {
		if count != 1 || outputs[pair] != 1 {
			return "unpaired_tool_history"
		}
	}
	return ""
}

func providerHistoryScope(provider, authID string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authID = strings.TrimSpace(authID)
	if provider == "" || authID == "" {
		return ""
	}
	return provider + "::" + authID
}

func selectedProviderHistoryScope(provider string, opts cliproxyexecutor.Options) string {
	if opts.Metadata == nil {
		return ""
	}
	authID, _ := opts.Metadata[cliproxyexecutor.SelectedAuthMetadataKey].(string)
	return providerHistoryScope(provider, authID)
}

func providerHistoryScopeState(value string) (scope string, tainted bool) {
	if strings.HasPrefix(value, providerHistoryTaintedPrefix) {
		return strings.TrimPrefix(value, providerHistoryTaintedPrefix), true
	}
	return value, false
}

func markProviderHistoryTainted(opts cliproxyexecutor.Options) cliproxyexecutor.Options {
	metadata := make(map[string]any, len(opts.Metadata)+1)
	for key, value := range opts.Metadata {
		metadata[key] = value
	}
	metadata[providerHistoryTaintedMetadataKey] = true
	opts.Metadata = metadata
	return opts
}

func providerHistoryUsesPreviousResponse(body []byte) bool {
	var request struct {
		PreviousResponseID string `json:"previous_response_id"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return false
	}
	return strings.TrimSpace(request.PreviousResponseID) != ""
}

func providerHistoryIsNeutralIncrementalInput(body []byte) bool {
	var request struct {
		PreviousResponseID string          `json:"previous_response_id"`
		Input              json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil || strings.TrimSpace(request.PreviousResponseID) == "" {
		return false
	}

	var text string
	if json.Unmarshal(request.Input, &text) == nil {
		return true
	}

	var items []map[string]any
	if json.Unmarshal(request.Input, &items) != nil || len(items) != 1 {
		return false
	}
	itemType, _ := items[0]["type"].(string)
	if itemType == "message" {
		return true
	}
	_, hasRole := items[0]["role"].(string)
	return itemType == "" && hasRole
}

func providerHistoryUsesConversation(body []byte) bool {
	var request struct {
		Conversation   json.RawMessage `json:"conversation"`
		ConversationID string          `json:"conversation_id"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return false
	}
	if strings.TrimSpace(request.ConversationID) != "" {
		return true
	}
	rawConversation := bytes.TrimSpace(request.Conversation)
	if len(rawConversation) == 0 || bytes.Equal(rawConversation, []byte("null")) {
		return false
	}
	var conversationID string
	if err := json.Unmarshal(rawConversation, &conversationID); err == nil {
		return strings.TrimSpace(conversationID) != ""
	}
	var conversation struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(rawConversation, &conversation) == nil && strings.TrimSpace(conversation.ID) != ""
}

func providerHistorySessionIDs(headers http.Header, payload []byte, metadata map[string]any) []string {
	primaryID, fallbackID := extractExplicitSessionIDs(headers, payload, metadata)
	aliases := mergeSessionAliases(nil, primaryID, fallbackID)

	var request struct {
		PromptCacheKey      string `json:"prompt_cache_key"`
		PromptCacheKeyCamel string `json:"promptCacheKey"`
		Request             *struct {
			PromptCacheKey      string `json:"prompt_cache_key"`
			PromptCacheKeyCamel string `json:"promptCacheKey"`
		} `json:"request"`
	}
	if json.Unmarshal(payload, &request) == nil {
		promptCacheKey := request.PromptCacheKey
		if strings.TrimSpace(promptCacheKey) == "" {
			promptCacheKey = request.PromptCacheKeyCamel
		}
		if strings.TrimSpace(promptCacheKey) == "" && request.Request != nil {
			promptCacheKey = request.Request.PromptCacheKey
			if strings.TrimSpace(promptCacheKey) == "" {
				promptCacheKey = request.Request.PromptCacheKeyCamel
			}
		}
		if promptCacheKey = normalizedSessionCandidate(promptCacheKey); promptCacheKey != "" {
			aliases = mergeSessionAliases(aliases, "pck:"+promptCacheKey)
		}
	}
	return compactSessionAliases(aliases)
}

func (m *Manager) normalizeProviderHistoryAttempt(provider string, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
	if m == nil || m.providerHistoryScopes == nil || (opts.SourceFormat != sdktranslator.FormatOpenAIResponse && opts.SourceFormat != sdktranslator.FormatCodex) {
		return req, opts, nil
	}
	targetScope := ""
	if auth != nil {
		targetScope = providerHistoryScope(provider, auth.ID)
	}
	if targetScope == "" {
		targetScope = selectedProviderHistoryScope(provider, opts)
	}
	if targetScope == "" {
		return req, opts, nil
	}
	sessionIDs := providerHistorySessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	if len(sessionIDs) == 0 {
		return req, opts, nil
	}

	sourceState := ""
	found := false
	for _, sessionID := range sessionIDs {
		if sourceState, found = m.providerHistoryScopes.GetAndRefresh(sessionID); found {
			break
		}
	}
	sourceScope, tainted := providerHistoryScopeState(sourceState)
	if !found || (!tainted && sourceScope == targetScope) {
		return req, opts, nil
	}
	if tainted && sourceScope == targetScope && providerHistoryUsesPreviousResponse(req.Payload) {
		// Only a provider-neutral incremental input can safely bypass full-history
		// projection. Some clients carry a previous_response_id together with the
		// older mixed transcript, which must remain quarantined.
		if providerHistoryIsNeutralIncrementalInput(req.Payload) {
			return req, opts, nil
		}
	}
	if sourceScope != targetScope && providerHistoryUsesPreviousResponse(req.Payload) {
		return req, opts, &providerHistoryError{reason: "foreign_previous_response_requires_rehydration"}
	}
	if sourceScope != targetScope && providerHistoryUsesConversation(req.Payload) {
		return req, opts, &providerHistoryError{reason: "foreign_conversation_requires_rehydration"}
	}

	normalized, err := normalizeProviderBoundResponseHistory(req.Payload)
	if err != nil {
		return req, opts, err
	}
	if normalized.Changed {
		req.Payload = bytes.Clone(normalized.Body)
		opts.OriginalRequest = bytes.Clone(normalized.Body)
		opts = markProviderHistoryTainted(opts)
	}
	return req, opts, nil
}

func (m *Manager) rememberProviderHistoryScope(result Result) {
	if m == nil || m.providerHistoryScopes == nil {
		return
	}
	if result.Options.SourceFormat != sdktranslator.FormatOpenAIResponse && result.Options.SourceFormat != sdktranslator.FormatCodex {
		return
	}
	committed, _ := result.Options.Metadata[cliproxyexecutor.ProviderOutputCommittedMetadataKey].(bool)
	if !result.Success && !committed {
		return
	}
	if generate, explicit := generateFromOptions(result.Options); explicit && !generate {
		return
	}
	scope := providerHistoryScope(result.Provider, result.AuthID)
	if scope == "" {
		return
	}
	sessionIDs := providerHistorySessionIDs(result.Options.Headers, result.Options.OriginalRequest, result.Options.Metadata)
	if len(sessionIDs) == 0 {
		return
	}
	if tainted, _ := result.Options.Metadata[providerHistoryTaintedMetadataKey].(bool); tainted {
		scope = providerHistoryTaintedPrefix + scope
	}
	m.providerHistoryScopes.SetAliases(scope, sessionIDs...)
}
