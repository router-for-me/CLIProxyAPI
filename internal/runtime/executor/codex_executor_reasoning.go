package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexReasoningReplayScope struct {
	modelName          string
	sessionKey         string
	requestFingerprint string
}

func (s codexReasoningReplayScope) valid() bool {
	return strings.TrimSpace(s.modelName) != "" && strings.TrimSpace(s.sessionKey) != ""
}

func applyCodexReasoningReplayCache(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) ([]byte, codexReasoningReplayScope) {
	updated, scope, _ := applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
	return updated, scope
}

func applyCodexReasoningReplayCacheRequired(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) ([]byte, codexReasoningReplayScope, error) {
	scope := codexReasoningReplayScopeFromRequest(ctx, from, req, opts, body)
	if !scope.valid() {
		return body, scope, nil
	}
	items, ok, errReplay := internalcache.GetCodexReasoningReplayItemsRequired(ctx, scope.modelName, scope.sessionKey)
	if errReplay != nil || !ok {
		return body, scope, errReplay
	}
	updated, ok := insertCodexReasoningReplayTurns(body, items)
	if !ok {
		return body, scope, nil
	}
	return updated, scope, nil
}

func codexReasoningReplayScopeFromRequest(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) codexReasoningReplayScope {
	if !codexReasoningReplayEnabledForSource(from) {
		return codexReasoningReplayScope{}
	}
	modelName := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if modelName == "" {
		modelName = thinking.ParseSuffix(req.Model).ModelName
	}
	inputItems := gjson.GetBytes(body, "input").Array()
	return codexReasoningReplayScope{
		modelName:          modelName,
		sessionKey:         codexReasoningReplaySessionKey(ctx, from, req, opts, body),
		requestFingerprint: codexReplayInputPrefixFingerprint(inputItems, len(inputItems)),
	}
}

func codexReasoningReplayEnabledForSource(from sdktranslator.Format) bool {
	return sourceFormatEqual(from, sdktranslator.FormatClaude)
}

func sourceFormatEqual(from, want sdktranslator.Format) bool {
	return strings.EqualFold(strings.TrimSpace(from.String()), want.String())
}

func codexClaudeCodeReplaySessionKey(ctx context.Context, payload []byte, headers http.Header) string {
	sessionKey, _ := helps.ClaudeCodeExecutionScope(ctx, payload, headers)
	return sessionKey
}

func codexReasoningReplaySessionKey(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte) string {
	if ctx == nil {
		ctx = context.Background()
	}
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		if sessionKey := codexClaudeCodeReplaySessionKey(ctx, req.Payload, opts.Headers); sessionKey != "" {
			return sessionKey
		}
	}
	if value := metadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return "execution:" + value
	}
	if value := metadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return "execution:" + value
	}
	if value := codexReasoningReplaySessionKeyFromPayload(body); value != "" {
		return value
	}
	if value := codexReasoningReplaySessionKeyFromPayload(req.Payload); value != "" {
		return value
	}
	if value := codexReasoningReplaySessionKeyFromHeaders(opts.Headers); value != "" {
		return value
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		if value := codexReasoningReplaySessionKeyFromHeaders(ginCtx.Request.Header); value != "" {
			return value
		}
	}
	if sourceFormatEqual(from, sdktranslator.FormatOpenAI) {
		if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			return "prompt-cache:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
		}
	}
	return ""
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func codexReasoningReplaySessionKeyFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if promptCacheKey := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); promptCacheKey != "" {
		return "prompt-cache:" + promptCacheKey
	}
	if windowID := strings.TrimSpace(gjson.GetBytes(payload, "client_metadata.x-codex-window-id").String()); windowID != "" {
		return "window:" + windowID
	}
	if turnMetadata := strings.TrimSpace(gjson.GetBytes(payload, "client_metadata.x-codex-turn-metadata").String()); turnMetadata != "" {
		return codexReasoningReplaySessionKeyFromTurnMetadata(turnMetadata)
	}
	return ""
}

func codexReasoningReplaySessionKeyFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if turnMetadata := strings.TrimSpace(headers.Get("X-Codex-Turn-Metadata")); turnMetadata != "" {
		if key := codexReasoningReplaySessionKeyFromTurnMetadata(turnMetadata); key != "" {
			return key
		}
	}
	if windowID := strings.TrimSpace(headerValueCaseInsensitive(headers, "X-Codex-Window-Id")); windowID != "" {
		return "window:" + windowID
	}
	for _, headerName := range []string{"Session_id", "session_id", "Session-Id"} {
		if value := strings.TrimSpace(headerValueCaseInsensitive(headers, headerName)); value != "" {
			return "session-id:" + value
		}
	}
	if conversationID := strings.TrimSpace(headerValueCaseInsensitive(headers, "Conversation_id")); conversationID != "" {
		return "conversation_id:" + conversationID
	}
	return ""
}

func codexReasoningReplaySessionKeyFromTurnMetadata(turnMetadata string) string {
	if promptCacheKey := strings.TrimSpace(gjson.Get(turnMetadata, "prompt_cache_key").String()); promptCacheKey != "" {
		return "prompt-cache:" + promptCacheKey
	}
	if windowID := strings.TrimSpace(gjson.Get(turnMetadata, "window_id").String()); windowID != "" {
		return "window:" + windowID
	}
	return ""
}

func codexInputHasValidReasoningEncryptedContent(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}
		encryptedContent := item.Get("encrypted_content")
		if encryptedContent.Type != gjson.String {
			continue
		}
		if _, err := signature.InspectGPTReasoningSignature(encryptedContent.String()); err == nil {
			return true
		}
	}
	return false
}

type codexReasoningReplayTurn struct {
	marked               bool
	assistantFingerprint string
	requestFingerprint   string
	callIDs              []string
	items                [][]byte
}

func insertCodexReasoningReplayTurns(body []byte, replayItems [][]byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() || len(replayItems) == 0 {
		return body, false
	}
	inputItems := input.Array()
	turns := splitCodexReasoningReplayTurns(replayItems)
	insertions := make(map[int][][]byte)
	usedAnchorIndexes := make(map[int]bool)
	fallbackAnchorEnd := len(inputItems) - 1
	inserted := false
	for turnIndex := len(turns) - 1; turnIndex >= 0; turnIndex-- {
		turn := turns[turnIndex]
		if len(turn.items) == 0 {
			continue
		}
		if !turn.marked {
			items := filterCodexReasoningReplayItemsForInput(body, turn.items)
			if len(items) == 0 {
				continue
			}
			index := codexReasoningReplayInsertIndex(inputItems, items)
			items = codexAlignReasoningReplayToolCallIDs(inputItems, items)
			insertions[index] = append(items, insertions[index]...)
			inserted = true
			continue
		}

		anchorIndex, matched := codexReasoningReplayTurnAnchorIndex(inputItems, turn, fallbackAnchorEnd, usedAnchorIndexes)
		if !matched {
			continue
		}
		usedAnchorIndexes[anchorIndex] = true
		if turn.requestFingerprint == "" {
			fallbackAnchorEnd = anchorIndex - 1
		}
		items := filterCodexReasoningReplayTurnItems(inputItems, turn.items)
		if len(items) == 0 {
			continue
		}
		items = codexAlignReasoningReplayToolCallIDs(inputItems, items)
		insertions[anchorIndex] = append(items, insertions[anchorIndex]...)
		inserted = true
	}
	if !inserted {
		return body, false
	}

	items := make([]string, 0, len(inputItems)+len(replayItems))
	for index, inputItem := range inputItems {
		for _, replayItem := range insertions[index] {
			items = append(items, string(replayItem))
		}
		items = append(items, inputItem.Raw)
	}
	for _, replayItem := range insertions[len(inputItems)] {
		items = append(items, string(replayItem))
	}
	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return body, false
	}
	return updated, true
}

func splitCodexReasoningReplayTurns(items [][]byte) []codexReasoningReplayTurn {
	turns := make([]codexReasoningReplayTurn, 0)
	current := codexReasoningReplayTurn{}
	appendCurrent := func() {
		if len(current.items) > 0 {
			turns = append(turns, current)
		}
	}
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		if strings.TrimSpace(itemResult.Get("type").String()) == internalcache.CodexReasoningReplayTurnType {
			appendCurrent()
			current = codexReasoningReplayTurn{
				marked:               true,
				assistantFingerprint: strings.TrimSpace(itemResult.Get("assistant_fingerprint").String()),
				requestFingerprint:   strings.TrimSpace(itemResult.Get("request_fingerprint").String()),
			}
			if callIDs := itemResult.Get("call_ids"); callIDs.IsArray() {
				for _, callIDResult := range callIDs.Array() {
					if callID := strings.TrimSpace(callIDResult.String()); callID != "" {
						current.callIDs = append(current.callIDs, callID)
					}
				}
			}
			continue
		}
		current.items = append(current.items, item)
	}
	appendCurrent()
	return turns
}

func codexReasoningReplayTurnAnchorIndex(inputItems []gjson.Result, turn codexReasoningReplayTurn, fallbackEnd int, used map[int]bool) (int, bool) {
	searchEnd := fallbackEnd
	if turn.requestFingerprint != "" {
		searchEnd = len(inputItems) - 1
	}
	if searchEnd >= len(inputItems) {
		searchEnd = len(inputItems) - 1
	}
	matchesRequestPrefix := func(index int) bool {
		return turn.requestFingerprint == "" || codexReplayInputPrefixFingerprint(inputItems, index) == turn.requestFingerprint
	}
	if len(turn.callIDs) > 0 {
		callIDs := make(map[string]bool)
		for _, callID := range turn.callIDs {
			for _, candidate := range codexReplayComparableCallIDs(callID) {
				callIDs[candidate] = true
			}
		}
		for index := searchEnd; index >= 0; index-- {
			if used[index] || !matchesRequestPrefix(index) {
				continue
			}
			itemType := strings.TrimSpace(inputItems[index].Get("type").String())
			if itemType != "function_call" && itemType != "custom_tool_call" && itemType != "function_call_output" && itemType != "custom_tool_call_output" {
				continue
			}
			for _, candidate := range codexReplayComparableCallIDs(inputItems[index].Get("call_id").String()) {
				if callIDs[candidate] {
					return index, true
				}
			}
		}
	}
	if turn.assistantFingerprint != "" {
		for index := searchEnd; index >= 0; index-- {
			if used[index] || !matchesRequestPrefix(index) {
				continue
			}
			if codexReplayAssistantMessageFingerprint(inputItems[index]) == turn.assistantFingerprint {
				return index, true
			}
		}
	}
	if len(turn.callIDs) == 0 && turn.assistantFingerprint == "" {
		return codexReasoningReplayInsertIndex(inputItems, turn.items), true
	}
	return 0, false
}

func filterCodexReasoningReplayTurnItems(inputItems []gjson.Result, items [][]byte) [][]byte {
	existingReasoning := make(map[string]bool)
	existingCalls := make(map[string]bool)
	existingOutputs := make(map[string]bool)
	for _, inputItem := range inputItems {
		itemType := strings.TrimSpace(inputItem.Get("type").String())
		switch itemType {
		case "reasoning":
			if encryptedContent := strings.TrimSpace(inputItem.Get("encrypted_content").String()); encryptedContent != "" {
				existingReasoning[encryptedContent] = true
			}
		case "function_call_output", "custom_tool_call_output":
			for _, candidate := range codexReplayComparableCallIDs(inputItem.Get("call_id").String()) {
				existingOutputs[candidate] = true
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
			if existingReasoning[strings.TrimSpace(itemResult.Get("encrypted_content").String())] {
				continue
			}
		case "function_call", "custom_tool_call":
			keys := codexReplayToolCallKeys(itemResult)
			if len(keys) == 0 || codexReplayAnyToolCallKeyExists(existingCalls, keys) {
				continue
			}
			hasMatchingOutput := false
			for _, candidate := range codexReplayComparableCallIDs(itemResult.Get("call_id").String()) {
				if existingOutputs[candidate] {
					hasMatchingOutput = true
					break
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

func codexReplayAssistantMessageFingerprint(item gjson.Result) string {
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType != "" && itemType != "message" {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(item.Get("role").String()), "assistant") {
		return ""
	}
	content := item.Get("content")
	var builder strings.Builder
	if content.Type == gjson.String {
		builder.WriteString(content.String())
	} else if content.IsArray() {
		for _, part := range content.Array() {
			switch strings.TrimSpace(part.Get("type").String()) {
			case "input_text", "output_text":
				builder.WriteString(part.Get("text").String())
			case "refusal":
				builder.WriteString("\x00refusal\x00")
				builder.WriteString(part.Get("refusal").String())
			default:
				return ""
			}
		}
	} else {
		return ""
	}
	if builder.Len() == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func codexReplayInputPrefixFingerprint(inputItems []gjson.Result, end int) string {
	if end < 0 || end > len(inputItems) {
		return ""
	}
	hasher := sha256.New()
	for index := 0; index < end; index++ {
		_, _ = hasher.Write([]byte("\x00item\x00"))
		_, _ = hasher.Write([]byte(inputItems[index].Raw))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func filterCodexReasoningReplayItemsForInput(body []byte, items [][]byte) [][]byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}

	hasInputReasoning := codexInputHasValidReasoningEncryptedContent(body)
	existingCalls := make(map[string]bool)
	existingOutputs := make(map[string]bool)
	for _, inputItem := range input.Array() {
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
			if hasInputReasoning {
				continue
			}
		case "function_call", "custom_tool_call":
			keys := codexReplayToolCallKeys(itemResult)
			if len(keys) == 0 || codexReplayAnyToolCallKeyExists(existingCalls, keys) {
				continue
			}
			// Only inject if there is a matching output in the request
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

func insertCodexReasoningReplayItems(body []byte, replayItems [][]byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() || len(replayItems) == 0 {
		return body, false
	}
	inputItems := input.Array()
	insertIndex := codexReasoningReplayInsertIndex(inputItems, replayItems)
	replayItems = codexAlignReasoningReplayToolCallIDs(inputItems, replayItems)
	items := make([]string, 0, len(inputItems)+len(replayItems))
	for i, inputItem := range inputItems {
		if i == insertIndex {
			for _, replayItem := range replayItems {
				items = append(items, string(replayItem))
			}
		}
		items = append(items, inputItem.Raw)
	}
	if insertIndex == len(inputItems) {
		for _, replayItem := range replayItems {
			items = append(items, string(replayItem))
		}
	}
	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(items, ",")+"]"))
	if err != nil {
		return body, false
	}
	return updated, true
}

func codexReasoningReplayInsertIndex(inputItems []gjson.Result, replayItems [][]byte) int {
	replayCallIDs := make(map[string]bool)
	for _, replayItem := range replayItems {
		itemResult := gjson.ParseBytes(replayItem)
		itemType := strings.TrimSpace(itemResult.Get("type").String())
		if itemType != "function_call" && itemType != "custom_tool_call" {
			continue
		}
		for _, callID := range codexReplayComparableCallIDs(itemResult.Get("call_id").String()) {
			replayCallIDs[callID] = true
		}
	}
	if len(replayCallIDs) > 0 {
		for index, inputItem := range inputItems {
			itemType := strings.TrimSpace(inputItem.Get("type").String())
			if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
				continue
			}
			callID := strings.TrimSpace(inputItem.Get("call_id").String())
			if callID == "" || replayCallIDs[callID] {
				return index
			}
		}
	}
	for index := len(inputItems) - 1; index >= 0; index-- {
		inputItem := inputItems[index]
		if role, ok := codexReplayMessageRole(inputItem); ok && role == "assistant" {
			return index
		}
	}
	for index, inputItem := range inputItems {
		if shouldInsertCodexReasoningReplayBefore(inputItem) {
			return index
		}
	}
	return len(inputItems)
}

func codexAlignReasoningReplayToolCallIDs(inputItems []gjson.Result, replayItems [][]byte) [][]byte {
	outputCallIDs := codexReplayOutputCallIDs(inputItems)
	if len(outputCallIDs) == 0 {
		return replayItems
	}

	aligned := make([][]byte, 0, len(replayItems))
	for _, replayItem := range replayItems {
		itemResult := gjson.ParseBytes(replayItem)
		itemType := strings.TrimSpace(itemResult.Get("type").String())
		if itemType != "function_call" && itemType != "custom_tool_call" {
			aligned = append(aligned, replayItem)
			continue
		}

		callID := strings.TrimSpace(itemResult.Get("call_id").String())
		outputCallID := ""
		for _, candidate := range codexReplayComparableCallIDs(callID) {
			if value := outputCallIDs[candidate]; value != "" {
				outputCallID = value
				break
			}
		}
		if outputCallID == "" || outputCallID == callID {
			aligned = append(aligned, replayItem)
			continue
		}

		updated, err := sjson.SetBytes(replayItem, "call_id", outputCallID)
		if err != nil {
			aligned = append(aligned, replayItem)
			continue
		}
		aligned = append(aligned, updated)
	}
	return aligned
}

func codexReplayOutputCallIDs(inputItems []gjson.Result) map[string]string {
	outputCallIDs := make(map[string]string)
	for _, inputItem := range inputItems {
		itemType := strings.TrimSpace(inputItem.Get("type").String())
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			continue
		}
		callID := strings.TrimSpace(inputItem.Get("call_id").String())
		if callID == "" {
			continue
		}
		for _, candidate := range codexReplayComparableCallIDs(callID) {
			outputCallIDs[candidate] = callID
		}
	}
	return outputCallIDs
}

func shouldInsertCodexReasoningReplayBefore(item gjson.Result) bool {
	role, ok := codexReplayMessageRole(item)
	if !ok {
		return true
	}
	switch role {
	case "developer", "system":
		return false
	default:
		return true
	}
}

func codexReplayMessageRole(item gjson.Result) (string, bool) {
	itemType := strings.TrimSpace(item.Get("type").String())
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "" || (itemType != "" && itemType != "message") {
		return "", false
	}
	return role, true
}

func codexReplayToolCallKeys(item gjson.Result) []string {
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType != "function_call" && itemType != "custom_tool_call" {
		return nil
	}
	callIDs := codexReplayComparableCallIDs(item.Get("call_id").String())
	if len(callIDs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(callIDs))
	for _, callID := range callIDs {
		keys = append(keys, itemType+":"+callID)
	}
	return keys
}

func codexReplayAnyToolCallKeyExists(existing map[string]bool, keys []string) bool {
	for _, key := range keys {
		if existing[key] {
			return true
		}
	}
	return false
}

func codexReplayComparableCallIDs(callID string) []string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil
	}

	claudeVisibleCallID := shortenCodexReplayCallIDIfNeeded(util.SanitizeClaudeToolID(callID))
	if claudeVisibleCallID == "" || claudeVisibleCallID == callID {
		return []string{callID}
	}
	return []string{callID, claudeVisibleCallID}
}

func shortenCodexReplayCallIDIfNeeded(id string) string {
	const limit = 64
	if len(id) <= limit {
		return id
	}

	sum := sha256.Sum256([]byte(id))
	suffix := "_" + hex.EncodeToString(sum[:8])
	prefixLen := limit - len(suffix)
	if prefixLen <= 0 {
		return suffix[len(suffix)-limit:]
	}
	return id[:prefixLen] + suffix
}

func cacheCodexReasoningReplayFromCompleted(scope codexReasoningReplayScope, completedData []byte) {
	if !scope.valid() {
		return
	}
	output := gjson.GetBytes(completedData, "response.output")
	if !output.IsArray() {
		return
	}
	replayItems := make([][]byte, 0, len(output.Array()))
	callIDs := make([]string, 0)
	assistantFingerprint := ""
	for _, item := range output.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "reasoning":
			replayItems = append(replayItems, []byte(item.Raw))
		case "function_call", "custom_tool_call":
			replayItems = append(replayItems, []byte(item.Raw))
			if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
				callIDs = append(callIDs, callID)
			}
		case "message":
			if fingerprint := codexReplayAssistantMessageFingerprint(item); fingerprint != "" {
				assistantFingerprint = fingerprint
			}
		}
	}
	if len(replayItems) == 0 {
		return
	}

	hasher := sha256.New()
	_, _ = hasher.Write([]byte(scope.requestFingerprint))
	_, _ = hasher.Write([]byte("\x00assistant\x00" + assistantFingerprint))
	for _, callID := range callIDs {
		_, _ = hasher.Write([]byte("\x00call\x00" + callID))
	}
	for _, item := range replayItems {
		_, _ = hasher.Write([]byte("\x00item\x00"))
		_, _ = hasher.Write(item)
	}
	marker := []byte(`{"type":"` + internalcache.CodexReasoningReplayTurnType + `"}`)
	marker, _ = sjson.SetBytes(marker, "id", hex.EncodeToString(hasher.Sum(nil)))
	if assistantFingerprint != "" {
		marker, _ = sjson.SetBytes(marker, "assistant_fingerprint", assistantFingerprint)
	}
	if scope.requestFingerprint != "" {
		marker, _ = sjson.SetBytes(marker, "request_fingerprint", scope.requestFingerprint)
	}
	for _, callID := range callIDs {
		marker, _ = sjson.SetBytes(marker, "call_ids.-1", callID)
	}
	items := make([][]byte, 0, len(replayItems)+1)
	items = append(items, marker)
	items = append(items, replayItems...)
	internalcache.AppendCodexReasoningReplayItemsBestEffort(context.Background(), scope.modelName, scope.sessionKey, items)
}

func clearCodexReasoningReplayOnInvalidSignature(ctx context.Context, scope codexReasoningReplayScope, statusCode int, body []byte) error {
	if !scope.valid() {
		return nil
	}
	code, _, ok := codexStatusErrorClassification(statusCode, body)
	if ok && code == "thinking_signature_invalid" {
		return internalcache.DeleteCodexReasoningReplayItemRequired(ctx, scope.modelName, scope.sessionKey)
	}
	return nil
}
