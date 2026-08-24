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

const targetCompactionProtocol = "remote_compaction_v2"

var providerBoundHistoryFields = [...]string{"id", "encrypted_content", "provider_item_id"}

type providerHistoryNormalization struct {
	Body                     []byte
	Changed                  bool
	RequiresTargetCompaction bool
	TargetCompactionProtocol string
	DroppedItems             int
	StrippedFields           int
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
	sawCompaction := false
	neutralBeforeCompaction := false
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
		switch itemType {
		case "message", "reasoning", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output":
		case "compaction":
			sawCompaction = true
			result.Changed = true
			result.RequiresTargetCompaction = true
			result.TargetCompactionProtocol = targetCompactionProtocol
			result.DroppedItems++
			continue
		default:
			return providerHistoryNormalization{}, &providerHistoryError{reason: "unsupported_history_item"}
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
		if !sawCompaction {
			neutralBeforeCompaction = true
		}
		normalized = append(normalized, item)
	}
	if sawCompaction && !neutralBeforeCompaction {
		return providerHistoryNormalization{}, &providerHistoryError{reason: "foreign_compaction_requires_rehydration"}
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
	primaryID, fallbackID := extractSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	if primaryID == "" && fallbackID == "" {
		return req, opts, nil
	}

	sourceScope, found := m.providerHistoryScopes.GetAndRefresh(primaryID)
	if !found && fallbackID != "" {
		sourceScope, found = m.providerHistoryScopes.GetAndRefresh(fallbackID)
	}
	if !found || sourceScope == targetScope {
		return req, opts, nil
	}

	normalized, err := normalizeProviderBoundResponseHistory(req.Payload)
	if err != nil {
		return req, opts, err
	}
	if normalized.Changed {
		req.Payload = bytes.Clone(normalized.Body)
		opts.OriginalRequest = bytes.Clone(normalized.Body)
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
	primaryID, fallbackID := extractSessionIDs(result.Options.Headers, result.Options.OriginalRequest, result.Options.Metadata)
	if primaryID == "" && fallbackID == "" {
		return
	}
	m.providerHistoryScopes.SetAliases(scope, primaryID, fallbackID)
}
