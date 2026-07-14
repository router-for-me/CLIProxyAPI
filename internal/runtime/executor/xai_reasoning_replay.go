package executor

import (
	"context"
	"net/http"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
	sessionKey = helps.IsolateClientReasoningReplaySessionKey(ctx, sessionKey)
	return xaiReasoningReplayScope{
		modelName:  thinking.ParseSuffix(req.Model).ModelName,
		sessionKey: sessionKey,
	}
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
	lastAssistantMessage, hasLastAssistantMessage := xaiInputLastAssistantMessage(inputItems)
	cachedAssistantMessage, hasCachedAssistantMessage := xaiReplayAssistantMessage(items)
	assistantMessageMatches := hasLastAssistantMessage && hasCachedAssistantMessage &&
		xaiAssistantMessageContentEqual(lastAssistantMessage.Get("content"), cachedAssistantMessage.Get("content"))
	ambiguousAssistantHistory := hasLastAssistantMessage && hasCachedAssistantMessage && !assistantMessageMatches
	if ambiguousAssistantHistory {
		return nil
	}
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
			if assistantMessageMatches {
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
		itemType := strings.TrimSpace(inputItem.Get("type").String())
		if (itemType != "" && itemType != "message") || !strings.EqualFold(strings.TrimSpace(inputItem.Get("role").String()), "assistant") {
			continue
		}
		return inputItem, true
	}
	return gjson.Result{}, false
}

func xaiReplayAssistantMessage(items [][]byte) (gjson.Result, bool) {
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		if strings.TrimSpace(itemResult.Get("type").String()) == "message" &&
			strings.EqualFold(strings.TrimSpace(itemResult.Get("role").String()), "assistant") {
			return itemResult, true
		}
	}
	return gjson.Result{}, false
}

type xaiAssistantMessagePart struct {
	partType string
	value    string
}

func xaiAssistantMessageContentEqual(left, right gjson.Result) bool {
	leftParts, leftOK := xaiAssistantMessageParts(left)
	rightParts, rightOK := xaiAssistantMessageParts(right)
	if !leftOK || !rightOK || len(leftParts) != len(rightParts) {
		return false
	}
	for i := range leftParts {
		if leftParts[i] != rightParts[i] {
			return false
		}
	}
	return true
}

func xaiAssistantMessageParts(content gjson.Result) ([]xaiAssistantMessagePart, bool) {
	if content.Type == gjson.String {
		return []xaiAssistantMessagePart{{partType: "output_text", value: content.String()}}, true
	}
	if !content.IsArray() {
		return nil, false
	}
	parts := make([]xaiAssistantMessagePart, 0, len(content.Array()))
	for _, part := range content.Array() {
		partType := strings.TrimSpace(part.Get("type").String())
		switch partType {
		case "output_text":
			text := part.Get("text")
			if text.Type != gjson.String {
				return nil, false
			}
			parts = append(parts, xaiAssistantMessagePart{partType: partType, value: text.String()})
		case "refusal":
			refusal := part.Get("refusal")
			if refusal.Type != gjson.String {
				return nil, false
			}
			parts = append(parts, xaiAssistantMessagePart{partType: partType, value: refusal.String()})
		default:
			return nil, false
		}
	}
	return parts, len(parts) > 0
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

func clearXAIReasoningReplayAfterCompaction(ctx context.Context, scope xaiReasoningReplayScope) {
	if !scope.valid() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errDelete := internalcache.DeleteXAIReasoningReplayItemRequired(ctx, scope.modelName, scope.sessionKey); errDelete != nil {
		log.Warnf("xai reasoning replay cache delete failed after successful compaction: %v", errDelete)
	}
}

// clearXAIReasoningReplayOnInvalidEncryptedContent best-effort clears stale
// replay state when upstream rejects injected encrypted reasoning content.
// Delete failures never replace the upstream status error.
func clearXAIReasoningReplayOnInvalidEncryptedContent(ctx context.Context, scope xaiReasoningReplayScope, statusCode int, body []byte) {
	if !scope.valid() {
		return
	}
	if statusCode < 400 || statusCode >= 500 {
		return
	}
	if !xaiBodyIndicatesInvalidEncryptedContent(body) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errDelete := internalcache.DeleteXAIReasoningReplayItemRequired(ctx, scope.modelName, scope.sessionKey); errDelete != nil {
		log.Warnf("xai reasoning replay cache delete failed after invalid encrypted content: %v", errDelete)
	}
}

func xaiBodyIndicatesInvalidEncryptedContent(body []byte) bool {
	lower := strings.ToLower(strings.TrimSpace(string(body)))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "invalid_encrypted_content") ||
		strings.Contains(lower, "invalid signature in thinking block") ||
		(strings.Contains(lower, "encrypted_content") && strings.Contains(lower, "invalid"))
}

// xaiSSETerminalFailure extracts a client-facing status and error body from a
// terminal SSE event (type "error" or "response.failed") so callers can clear
// stale replay state even when the HTTP status was 200.
func xaiSSETerminalFailure(eventData []byte) (status int, body []byte, ok bool) {
	eventType := strings.TrimSpace(gjson.GetBytes(eventData, "type").String())
	switch eventType {
	case "error", "response.failed":
	default:
		return 0, nil, false
	}

	body = xaiExtractTerminalErrorBody(eventData)
	if len(body) == 0 {
		// Still treat the event as terminal with a minimal error body so callers
		// return rather than waiting for response.completed.
		body = []byte(`{"error":{"message":"upstream response failed","type":"api_error"}}`)
	}

	// Only valid 4xx/5xx values count: a response.failed can carry an SSE transport
	// status like 200, which must not become a success/non-HTTP error status.
	if s := int(gjson.GetBytes(eventData, "status").Int()); xaiIsHTTPErrorStatus(s) {
		status = s
	}
	if status <= 0 {
		if s := int(gjson.GetBytes(eventData, "status_code").Int()); xaiIsHTTPErrorStatus(s) {
			status = s
		}
	}
	if status <= 0 {
		status = xaiNestedExplicitWebsocketStatus(body)
	}
	if status <= 0 {
		status = xaiNestedExplicitWebsocketStatus(eventData)
	}
	if status <= 0 {
		// Classify only from the extracted structured error body, never the full
		// event: the event can carry response.output / response.instructions text
		// that would let caller/model content drive the synthesized status.
		status = xaiStatusFromErrorSemantics(body)
	}
	if status <= 0 {
		// Unknown statusless upstream failure: treat as transient server-side so
		// retry/failover/cooldown stays eligible (genuine client faults are already
		// mapped to 400 by the invalid_request / invalid_encrypted_content cases).
		status = http.StatusInternalServerError
	}
	if status == http.StatusBadRequest {
		// Tag synthesized client errors with the canonical request-error type so
		// the conductor's isRequestInvalidError recognizes the 400 and returns it
		// once instead of retrying across every model/credential.
		body = xaiEnsureInvalidRequestType(body)
	}
	return status, body, true
}

// xaiEnsureInvalidRequestType canonicalizes a classified client-error body so
// error.type is the canonical "invalid_request_error" that the conductor's
// isRequestInvalidError recognizes. A non-canonical but meaningful existing type
// (e.g. "invalid_request", "invalid_prompt", "context_length_exceeded") is
// preserved as error.code when code is empty, so the specific reason is not lost
// while routing still treats the 400 as request-scoped (returned once, not
// retried across every model/credential).
func xaiEnsureInvalidRequestType(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	existingType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
	if strings.EqualFold(existingType, "invalid_request_error") {
		return body
	}
	if existingType != "" && strings.TrimSpace(gjson.GetBytes(body, "error.code").String()) == "" {
		if updated, err := sjson.SetBytes(body, "error.code", existingType); err == nil {
			body = updated
		}
	}
	updated, err := sjson.SetBytes(body, "error.type", "invalid_request_error")
	if err != nil {
		return body
	}
	return updated
}

// xaiWebsocketErrorObject extracts the structured error object of a websocket
// error event, never sibling fields such as response.output or instructions, so
// caller- or model-supplied content in the event cannot drive status
// classification or replay-cache cleanup.
func xaiWebsocketErrorObject(payload []byte) []byte {
	return xaiExtractTerminalErrorBody(payload)
}

// xaiExtractTerminalErrorBody builds the canonical {"error":{...}} object for a
// terminal error event by combining the nested error object (error /
// response.error / body.error — body.error covers the Codex websocket shape) with
// flat top-level structured fields (code, message, error_type, param). Merging
// both shapes ensures an event that mixes a nested error value with flat fields
// (e.g. {"error":"rejected","code":"invalid_encrypted_content"}) is fully
// classified. Sibling fields such as response.output are never read.
func xaiExtractTerminalErrorBody(eventData []byte) []byte {
	var base []byte
	for _, path := range []string{"error", "response.error", "body.error"} {
		if body := xaiTerminalErrorBody(eventData, path); len(body) > 0 {
			base = body
			break
		}
	}
	return xaiMergeFlatErrorFields(base, eventData)
}

// xaiMergeFlatErrorFields overlays flat top-level error fields onto base.error.*
// without overwriting values already present. base may be empty, in which case a
// body is built solely from the flat fields. Root "type" is the event name, not
// an error class, so only "error_type" maps to error.type.
func xaiMergeFlatErrorFields(base []byte, eventData []byte) []byte {
	fields := []struct{ src, dst string }{
		{"message", "error.message"},
		{"code", "error.code"},
		{"error_type", "error.type"},
		{"param", "error.param"},
	}
	out := base
	for _, f := range fields {
		val := strings.TrimSpace(gjson.GetBytes(eventData, f.src).String())
		if val == "" {
			continue
		}
		if len(out) == 0 {
			out = []byte(`{"error":{}}`)
		}
		if strings.TrimSpace(gjson.GetBytes(out, f.dst).String()) != "" {
			continue
		}
		if updated, err := sjson.SetBytes(out, f.dst, val); err == nil {
			out = updated
		}
	}
	return out
}

func xaiTerminalErrorBody(eventData []byte, path string) []byte {
	errorResult := gjson.GetBytes(eventData, path)
	if !errorResult.Exists() {
		return nil
	}
	body := []byte(`{"error":{}}`)
	if errorResult.Type == gjson.JSON {
		body, _ = sjson.SetRawBytes(body, "error", []byte(errorResult.Raw))
	} else if message := strings.TrimSpace(errorResult.String()); message != "" {
		body, _ = sjson.SetBytes(body, "error.message", message)
	} else {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(body, "error.message").String()) == "" &&
		strings.TrimSpace(gjson.GetBytes(body, "error.code").String()) == "" &&
		strings.TrimSpace(gjson.GetBytes(body, "error.type").String()) == "" {
		// A type-only error object (e.g. {"type":"rate_limit_exceeded"}) is still a
		// classifiable structured error; only drop it when message, code, and type
		// are all absent.
		return nil
	}
	return body
}

// xaiStatusFromErrorSemantics maps statusless semantic error codes/types to an
// HTTP-like status for cooldown/retry and replay-cache cleanup gates.
func xaiStatusFromErrorSemantics(payload []byte) int {
	if len(payload) == 0 {
		return 0
	}
	// codes holds only the structured error identifiers (code/type). These are
	// safe to match standard auth/quota codes against because they are not
	// free-form text that can echo caller- or model-supplied content.
	codes := strings.ToLower(strings.Join([]string{
		gjson.GetBytes(payload, "error.code").String(),
		gjson.GetBytes(payload, "error.type").String(),
		gjson.GetBytes(payload, "code").String(),
		gjson.GetBytes(payload, "error_type").String(),
		gjson.GetBytes(payload, "response.error.code").String(),
		gjson.GetBytes(payload, "response.error.type").String(),
	}, " "))
	// messages holds the human-readable text; it can echo caller/model content,
	// so it is only consulted after the structured code/type identifiers below.
	messages := strings.ToLower(strings.Join([]string{
		gjson.GetBytes(payload, "error.message").String(),
		gjson.GetBytes(payload, "message").String(),
		gjson.GetBytes(payload, "response.error.message").String(),
	}, " "))

	// Pass 1 — structured code/type identifiers (high confidence). These win over
	// message heuristics so a client error (e.g. type invalid_request_error) whose
	// message merely echoes "rate limit" is not misclassified as retryable.
	// Specific quota / rate / server codes are checked before the generic
	// invalid_request bucket, because an event can carry both a specific code and
	// a generic type (e.g. code=rate_limit_exceeded, type=invalid_request_error)
	// and the specific code must win.
	switch {
	case strings.Contains(codes, "invalid_api_key"),
		strings.Contains(codes, "authentication_error"),
		// A deactivated account is an auth/permission failure, not recoverable
		// quota, so it must leave the quota backoff path.
		strings.Contains(codes, "account_deactivated"):
		return http.StatusUnauthorized
	case strings.Contains(codes, "insufficient_quota"),
		strings.Contains(codes, "free-usage-exhausted"):
		return http.StatusTooManyRequests
	case strings.Contains(codes, "rate_limit"): // rate_limit / rate_limit_exceeded
		return http.StatusTooManyRequests
	case strings.Contains(codes, "server_error"),
		strings.Contains(codes, "internal_error"):
		return http.StatusInternalServerError
	case strings.Contains(codes, "overloaded"):
		return http.StatusServiceUnavailable
	case strings.Contains(codes, "invalid_encrypted_content"),
		strings.Contains(codes, "invalid_request"), // invalid_request / invalid_request_error
		strings.Contains(codes, "invalid_prompt"),
		strings.Contains(codes, "context_length_exceeded"),
		strings.Contains(codes, "context_too_large"):
		return http.StatusBadRequest
	}

	// Pass 2 — message/body heuristics. Only client-error (400) classifications
	// are derived from free-form message text; a 400 is returned to the caller
	// with no credential action. Auth, quota, rate, and server statuses — which
	// trigger refresh, cooldown, or suspension — come exclusively from explicit
	// status or the structured codes classified in pass 1, so caller- or
	// model-echoed text cannot suspend or cool credentials.
	haystack := codes + " " + messages
	switch {
	case strings.Contains(haystack, "invalid_encrypted_content"),
		strings.Contains(haystack, "invalid signature in thinking"),
		strings.Contains(haystack, "request validation error"),
		strings.Contains(haystack, "context length"),
		strings.Contains(haystack, "maximum context"),
		strings.Contains(haystack, "too many tokens"):
		return http.StatusBadRequest
	default:
		return 0
	}
}
