package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type xaiReasoningReplayScope struct {
	modelName  string
	sessionKey string
}

var getXAIReasoningReplayItemsRequired = internalcache.GetXAIReasoningReplayItemsRequired

func (s xaiReasoningReplayScope) valid() bool {
	return strings.TrimSpace(s.modelName) != "" && strings.TrimSpace(s.sessionKey) != ""
}

func applyXAIReasoningReplayCacheRequired(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) ([]byte, xaiReasoningReplayScope, error) {
	scope := xaiReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	if !scope.valid() {
		return body, scope, nil
	}
	items, ok, errReplay := getXAIReasoningReplayItemsRequired(ctx, scope.modelName, scope.sessionKey)
	if errReplay != nil {
		log.Warnf("xai reasoning replay cache read failed: %v", errReplay)
		return body, scope, nil
	}
	if !ok {
		return body, scope, nil
	}
	items = filterXAIReasoningReplayItemsForInput(body, items)
	if len(items) == 0 {
		return body, scope, nil
	}
	updated, ok := insertCodexReasoningReplayItems(body, items)
	if !ok {
		return body, scope, nil
	}
	return updated, scope, nil
}

func xaiReasoningReplayScopeFromRequest(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) xaiReasoningReplayScope {
	if !xaiReasoningReplayEnabledForSource(from) {
		return xaiReasoningReplayScope{}
	}
	// End-to-end WebSocket requests use upstream previous_response_id state.
	// Replaying encrypted reasoning as input as well would duplicate the turn.
	if cliproxyexecutor.DownstreamWebsocket(ctx) && strings.TrimSpace(gjson.GetBytes(req.Payload, "previous_response_id").String()) != "" {
		return xaiReasoningReplayScope{}
	}
	sessionKey := codexReasoningReplaySessionKey(ctx, from, req, opts, body)
	sessionKey = xaiReasoningReplayIsolateSessionKey(ctx, from, sessionKey)
	return xaiReasoningReplayScope{
		modelName:  thinking.ParseSuffix(req.Model).ModelName,
		sessionKey: sessionKey,
	}
}

// xaiReasoningReplayIsolateSessionKey namespaces client-controlled session keys
// by the downstream CPA API key so two callers cannot share encrypted reasoning
// or assistant text by reusing prompt_cache_key / window / session headers.
// Trusted execution session keys keep their existing form. OpenAI Responses
// without a caller API key is disabled rather than storing under a global key.
func xaiReasoningReplayIsolateSessionKey(ctx context.Context, from sdktranslator.Format, sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ""
	}
	if strings.HasPrefix(sessionKey, "execution:") {
		return sessionKey
	}
	apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx))
	if apiKey == "" {
		if sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse) {
			return ""
		}
		return sessionKey
	}
	sum := sha256.Sum256([]byte(apiKey))
	return "caller:" + hex.EncodeToString(sum[:8]) + ":" + sessionKey
}

func xaiReasoningReplayEnabledForSource(from sdktranslator.Format) bool {
	return sourceFormatEqual(from, sdktranslator.FormatClaude) ||
		sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse)
}

func xaiInputHasReasoningEncryptedContent(inputItems []gjson.Result, encryptedContent string) bool {
	if encryptedContent == "" {
		return false
	}
	for _, item := range inputItems {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}
		inputEncryptedContent := item.Get("encrypted_content")
		if inputEncryptedContent.Type != gjson.String {
			continue
		}
		if inputEncryptedContent.String() == encryptedContent {
			return true
		}
	}
	return false
}

func filterXAIReasoningReplayItemsForInput(body []byte, items [][]byte) [][]byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}

	inputItems := input.Array()
	_, hasLastAssistantMessage := xaiInputLastAssistantMessage(inputItems)
	existingCalls := make(map[string]bool)
	existingOutputs := make(map[string]bool)
	for _, inputItem := range inputItems {
		itemType := strings.TrimSpace(inputItem.Get("type").String())
		if itemType == "function_call_output" || itemType == "custom_tool_call_output" {
			callID := strings.TrimSpace(inputItem.Get("call_id").String())
			if callID != "" {
				for _, candidate := range codexReplayComparableCallIDs(callID) {
					existingOutputs[candidate] = true
				}
			}
		}
		for _, key := range codexReplayToolCallKeys(inputItem) {
			existingCalls[key] = true
		}
	}

	filtered := make([][]byte, 0, len(items))
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		switch strings.TrimSpace(itemResult.Get("type").String()) {
		case "reasoning":
			if xaiInputHasReasoningEncryptedContent(inputItems, itemResult.Get("encrypted_content").String()) {
				continue
			}
		case "message":
			// If the client already has any trailing assistant message, never inject
			// another one. Equal content is a true duplicate; unequal content would
			// create two adjacent assistants (partial-history / drift), which breaks
			// turn pairing. Inject reasoning alone in that case.
			if hasLastAssistantMessage {
				continue
			}
		case "function_call", "custom_tool_call":
			keys := codexReplayToolCallKeys(itemResult)
			if len(keys) == 0 || codexReplayAnyToolCallKeyExists(existingCalls, keys) {
				continue
			}
			hasMatchingOutput := false
			callID := strings.TrimSpace(itemResult.Get("call_id").String())
			if callID != "" {
				for _, candidate := range codexReplayComparableCallIDs(callID) {
					if existingOutputs[candidate] {
						hasMatchingOutput = true
						break
					}
				}
			}
			if !hasMatchingOutput {
				continue
			}
			for _, key := range keys {
				existingCalls[key] = true
			}
		default:
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func xaiInputLastAssistantMessage(inputItems []gjson.Result) (gjson.Result, bool) {
	for i := len(inputItems) - 1; i >= 0; i-- {
		inputItem := inputItems[i]
		if strings.TrimSpace(inputItem.Get("type").String()) != "message" ||
			!strings.EqualFold(strings.TrimSpace(inputItem.Get("role").String()), "assistant") {
			continue
		}
		return inputItem, true
	}
	return gjson.Result{}, false
}

func xaiAssistantMessageContentEqual(left, right gjson.Result) bool {
	if !left.IsArray() || !right.IsArray() {
		return false
	}
	leftParts := left.Array()
	rightParts := right.Array()
	if len(leftParts) != len(rightParts) {
		return false
	}
	for i := range leftParts {
		if strings.TrimSpace(leftParts[i].Get("type").String()) != strings.TrimSpace(rightParts[i].Get("type").String()) ||
			xaiAssistantMessagePartValue(leftParts[i]) != xaiAssistantMessagePartValue(rightParts[i]) {
			return false
		}
	}
	return true
}

func xaiAssistantMessagePartValue(part gjson.Result) string {
	switch strings.TrimSpace(part.Get("type").String()) {
	case "refusal":
		if refusal := part.Get("refusal"); refusal.Type == gjson.String {
			return refusal.String()
		}
		return part.Get("text").String()
	default:
		return part.Get("text").String()
	}
}

func cacheXAIReasoningReplayFromCompleted(ctx context.Context, scope xaiReasoningReplayScope, completedData []byte) {
	if !scope.valid() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	output := gjson.GetBytes(completedData, "response.output")
	if !output.IsArray() {
		return
	}
	items := make([][]byte, 0, len(output.Array()))
	for _, item := range output.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "reasoning", "message", "function_call", "custom_tool_call":
			items = append(items, []byte(item.Raw))
		default:
			continue
		}
	}
	switch internalcache.StoreXAIReasoningReplayItems(ctx, scope.modelName, scope.sessionKey, items) {
	case internalcache.XAIReasoningReplayStored:
		return
	case internalcache.XAIReasoningReplayNoReplayableState:
		// Successful completed turn without cacheable reasoning must not leave
		// a previous turn's encrypted state to be injected later.
		if errDelete := internalcache.DeleteXAIReasoningReplayItemRequired(ctx, scope.modelName, scope.sessionKey); errDelete != nil {
			log.Warnf("xai reasoning replay cache delete failed after non-replayable completed output: %v", errDelete)
		}
	case internalcache.XAIReasoningReplayStoreBackendError:
		log.Debug("xai reasoning replay cache store backend error; retaining previous entry")
	default:
		// Invalid args: nothing to store or clear.
	}
}
