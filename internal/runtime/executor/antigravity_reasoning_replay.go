package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type antigravityReasoningReplayScope struct {
	modelName     string
	sessionKey    string
	cacheSnapshot internalcache.AntigravityReasoningReplaySnapshot
}

func (s antigravityReasoningReplayScope) valid() bool {
	return strings.TrimSpace(s.modelName) != "" && strings.TrimSpace(s.sessionKey) != ""
}

func antigravityReasoningReplayScopeFromPayload(modelName string, payload []byte) antigravityReasoningReplayScope {
	sessionID := antigravityReplaySessionIDFromPayload(payload)
	if sessionID == "" {
		if stable := strings.TrimSpace(generateStableSessionID(payload)); stable != "" {
			sessionID = strings.TrimPrefix(stable, "-")
			if sessionID == "" {
				sessionID = stable
			}
		}
	}
	if sessionID == "" {
		return antigravityReasoningReplayScope{}
	}
	return antigravityReasoningReplayScope{
		modelName:  strings.TrimSpace(modelName),
		sessionKey: "session:" + sessionID,
	}
}

func antigravityReasoningReplayScopeFromRequest(ctx context.Context, modelName string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, payload []byte) antigravityReasoningReplayScope {
	// Prefer an explicit downstream session over a provider sessionId synthesized
	// from request text. This keeps identical prompts in separate client sessions
	// from sharing an opaque Gemini reasoning chain.
	if sessionKey := antigravityReasoningReplayClientSessionKey(ctx, req, opts); sessionKey != "" {
		return antigravityReasoningReplayScope{modelName: modelName, sessionKey: sessionKey}
	}
	if scope := antigravityReasoningReplayScopeFromPayload(modelName, payload); scope.valid() {
		return scope
	}
	if scope := antigravityReasoningReplayScopeFromPayload(modelName, req.Payload); scope.valid() {
		return scope
	}
	_ = ctx
	return antigravityReasoningReplayScope{}
}

func antigravityReasoningReplayClientSessionKey(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	for _, raw := range [][]byte{opts.OriginalRequest, req.Payload} {
		if scope, ok := helps.ClaudeCodeExecutionScope(ctx, raw, opts.Headers); ok {
			if lane := antigravityClaudeReplaySystemLane(raw); lane != "" {
				return scope + ":context:" + lane
			}
			return scope
		}
	}
	if value := strings.TrimSpace(opts.Headers.Get("Session-Id")); value != "" {
		return "responses:" + value
	}
	for _, raw := range [][]byte{opts.OriginalRequest, req.Payload} {
		if len(raw) == 0 {
			continue
		}
		for _, path := range []string{"session_id", "metadata.session_id"} {
			if value := strings.TrimSpace(gjson.GetBytes(raw, path).String()); value != "" {
				return "responses:" + value
			}
		}
	}
	if value := metadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return "execution:" + value
	}
	if value := metadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return "execution:" + value
	}
	for _, raw := range [][]byte{opts.OriginalRequest, req.Payload} {
		if value := strings.TrimSpace(gjson.GetBytes(raw, "prompt_cache_key").String()); value != "" {
			return "prompt-cache:" + value
		}
	}
	return ""
}

func antigravityClaudeReplaySystemLane(payload []byte) string {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return ""
	}
	var value any
	if errUnmarshal := json.Unmarshal([]byte(system.Raw), &value); errUnmarshal != nil {
		return ""
	}
	value = antigravityClaudeReplayNormalizeSystem(value)
	normalized, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return ""
	}
	sum := sha256.Sum256(normalized)
	return fmt.Sprintf("%x", sum[:16])
}

func antigravityClaudeReplayNormalizeSystem(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, child := range typed {
			if strings.EqualFold(strings.TrimSpace(key), "cache_control") {
				continue
			}
			normalized[key] = antigravityClaudeReplayNormalizeSystem(child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, child := range typed {
			normalized[index] = antigravityClaudeReplayNormalizeSystem(child)
		}
		return normalized
	default:
		return value
	}
}

func antigravityReplaySessionIDFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range []string{"sessionId", "session_id", "request.sessionId", "request.session_id"} {
		if id := strings.TrimSpace(gjson.GetBytes(payload, path).String()); id != "" {
			return id
		}
	}
	return ""
}

func antigravityReasoningReplayPendingModelContentIndex(payload []byte) (contentIndex int, basePartIndex int) {
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return 0, 0
	}
	arr := contents.Array()
	if len(arr) == 0 {
		return 0, 0
	}
	last := arr[len(arr)-1]
	if strings.EqualFold(strings.TrimSpace(last.Get("role").String()), "model") {
		parts := last.Get("parts")
		hasFunctionResponse := false
		if parts.IsArray() {
			parts.ForEach(func(_, part gjson.Result) bool {
				hasFunctionResponse = hasFunctionResponse || part.Get("functionResponse").Exists()
				return !hasFunctionResponse
			})
		}
		if !hasFunctionResponse {
			base := 0
			if parts.IsArray() {
				base = len(parts.Array())
			}
			return len(arr) - 1, base
		}
	}
	return len(arr), 0
}

func antigravityReasoningReplayResolveContentIndex(payload []byte, cached int) int {
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return cached
	}
	arr := contents.Array()
	if cached >= 0 && cached < len(arr) {
		return cached
	}
	return -1
}

func prepareAntigravityGeminiReasoningReplayPayload(ctx context.Context, modelName string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, payload []byte) ([]byte, antigravityReasoningReplayScope, error) {
	if !antigravityUsesReasoningReplayCache(modelName) {
		return payload, antigravityReasoningReplayScope{}, nil
	}
	updated, scope, replayApplied, errReplay := applyAntigravityReasoningReplayCache(ctx, modelName, req, opts, payload)
	if errReplay != nil {
		return payload, scope, errReplay
	}
	if replayApplied {
		updated = normalizeAntigravityGeminiFunctionResponseRoles(updated)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(updated); errPairing != nil {
		originalPairingValid := internalsignature.ValidateGeminiFunctionCallPairing(payload) == nil
		if replayApplied && originalPairingValid && scope.valid() {
			if _, errDelete := internalcache.DeleteAntigravityReasoningReplayItemsIfUnchanged(ctx, scope.modelName, scope.sessionKey, scope.cacheSnapshot); errDelete != nil {
				return payload, scope, errDelete
			}
		}
		return payload, scope, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("antigravity executor: invalid Gemini function call history: %v", errPairing)}
	}
	return updated, scope, nil
}

func clearAntigravityReasoningReplayOnInvalidSignature(ctx context.Context, scope antigravityReasoningReplayScope, statusCode int, body []byte) error {
	if !scope.valid() {
		return nil
	}
	if statusCode != http.StatusBadRequest {
		return nil
	}
	bodyText := strings.ToLower(string(body))
	if !strings.Contains(bodyText, "thoughtsignature") && !strings.Contains(bodyText, "thought_signature") && !strings.Contains(bodyText, "signature") {
		return nil
	}
	_, errDelete := internalcache.DeleteAntigravityReasoningReplayItemsIfUnchanged(ctx, scope.modelName, scope.sessionKey, scope.cacheSnapshot)
	return errDelete
}

func applyAntigravityReasoningReplayCache(ctx context.Context, modelName string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, payload []byte) ([]byte, antigravityReasoningReplayScope, bool, error) {
	scope := antigravityReasoningReplayScopeFromRequest(ctx, modelName, req, opts, payload)
	if !scope.valid() {
		return payload, scope, false, nil
	}
	items, snapshot, ok, err := internalcache.GetAntigravityReasoningReplayItemsWithSnapshotRequired(ctx, scope.modelName, scope.sessionKey)
	scope.cacheSnapshot = snapshot
	if err != nil || !ok || len(items) == 0 {
		return payload, scope, false, err
	}
	updated := payload
	changed := false
	for _, item := range items {
		eligible := filterAntigravityReasoningReplayItemsForRequest(updated, [][]byte{item})
		if len(eligible) != 1 {
			continue
		}
		next, applied := insertAntigravityReasoningReplayItems(updated, eligible)
		if !applied {
			continue
		}
		updated = next
		changed = true
	}
	if !changed {
		return payload, scope, false, nil
	}
	return updated, scope, true, nil
}

func filterAntigravityReasoningReplayItemsForRequest(payload []byte, items [][]byte) [][]byte {
	filtered := make([][]byte, 0, len(items))
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		switch strings.TrimSpace(itemResult.Get("type").String()) {
		case "function_call_part":
			signature := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
			if ci, pi, foundCall := antigravityFunctionCallPartLocationForReplay(payload, itemResult); foundCall {
				if signature == "" || antigravityHasNativeThoughtSignature(gjson.GetBytes(payload, fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", ci, pi)).String()) {
					continue
				}
				break
			}
			callID := strings.TrimSpace(itemResult.Get("call_id").String())
			if callID == "" {
				continue
			}
			responseIndex, foundResponse := antigravityFunctionResponseContentIndex(payload, callID)
			if !foundResponse {
				continue
			}
			contextMatches := antigravityReplayItemContextMatches(payload, itemResult, responseIndex)
			if !contextMatches && responseIndex > 0 {
				previousRole := gjson.GetBytes(payload, fmt.Sprintf("request.contents.%d.role", responseIndex-1)).String()
				contextMatches = strings.EqualFold(strings.TrimSpace(previousRole), "model") && antigravityReplayItemContextMatches(payload, itemResult, responseIndex-1)
			}
			if !contextMatches {
				continue
			}
		case "thought_signature":
			if antigravityRequestHasThoughtSignatureAt(payload, itemResult) {
				continue
			}
		default:
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func antigravityExistingToolCallKeys(payload []byte) map[string]bool {
	existing := make(map[string]bool)
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return existing
	}
	for _, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for _, part := range parts.Array() {
			if fc := part.Get("functionCall"); fc.Exists() {
				for _, key := range antigravityReplayToolCallKeysFromPart(fc) {
					existing[key] = true
				}
			}
		}
	}
	return existing
}

func antigravityReplayToolCallKeys(itemResult gjson.Result) []string {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	if callID == "" {
		callID = strings.TrimSpace(itemResult.Get("id").String())
	}
	name := strings.TrimSpace(itemResult.Get("name").String())
	if name == "" {
		return nil
	}
	args := itemResult.Get("args").Raw
	key := antigravityFunctionCallKey(name, args, callID)
	if key == "" {
		return nil
	}
	return []string{key}
}

func antigravityReplayToolCallKeysFromPart(fc gjson.Result) []string {
	return antigravityReplayToolCallKeys(gjson.Parse(fc.Raw))
}

func antigravityFunctionCallKey(name, argsRaw, callID string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.TrimSpace(argsRaw) != "" {
		argsRaw = string(antigravityCanonicalReplayJSON([]byte(argsRaw)))
	}
	h := sha256.Sum256([]byte(strings.Join([]string{name, argsRaw, callID}, "\x00")))
	return fmt.Sprintf("fc:%x", h[:8])
}

func antigravityAnyKeyExists(existing map[string]bool, keys []string) bool {
	for _, key := range keys {
		if existing[key] {
			return true
		}
	}
	return false
}

func antigravityNeedsSignatureReplayForExistingFunctionCall(payload []byte, itemResult gjson.Result) bool {
	if strings.TrimSpace(itemResult.Get("thoughtSignature").String()) == "" {
		return false
	}
	ci, pi, ok := antigravityFunctionCallPartLocationForReplay(payload, itemResult)
	if !ok {
		return false
	}
	pathSig := fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", ci, pi)
	return !antigravityHasNativeThoughtSignature(gjson.GetBytes(payload, pathSig).String())
}

func antigravityRequestHasMatchingFunctionResponse(payload []byte, itemResult gjson.Result) bool {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	if callID == "" {
		return true
	}
	_, ok := antigravityFunctionResponseContentIndex(payload, callID)
	return ok
}

func antigravityFunctionResponseContentIndex(payload []byte, callID string) (int, bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return -1, false
	}
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return -1, false
	}
	for i, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for _, part := range parts.Array() {
			fr := part.Get("functionResponse")
			if fr.Exists() && strings.TrimSpace(fr.Get("id").String()) == callID {
				return i, true
			}
		}
	}
	return -1, false
}

func antigravityPayloadHasFunctionCallID(payload []byte, callID string) bool {
	_, _, ok := antigravityFunctionCallPartLocation(payload, callID)
	return ok
}

func antigravityFunctionCallPartLocation(payload []byte, callID string) (contentIndex int, partIndex int, ok bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return -1, -1, false
	}
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return -1, -1, false
	}
	for ci, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for pi, part := range parts.Array() {
			fc := part.Get("functionCall")
			if fc.Exists() && strings.TrimSpace(fc.Get("id").String()) == callID {
				return ci, pi, true
			}
		}
	}
	return -1, -1, false
}

func antigravityFunctionCallPartLocationForReplay(payload []byte, itemResult gjson.Result) (contentIndex int, partIndex int, ok bool) {
	name := strings.TrimSpace(itemResult.Get("name").String())
	args := itemResult.Get("args")
	if name == "" || !args.Exists() {
		return -1, -1, false
	}
	wantedKey := antigravityFunctionCallKey(name, args.Raw, "")
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	if callID == "" {
		callID = strings.TrimSpace(itemResult.Get("id").String())
	}
	if callID != "" {
		ci, pi, found := antigravityFunctionCallPartLocation(payload, callID)
		if found {
			if antigravityReplayItemContextMatches(payload, itemResult, ci) {
				fc := gjson.GetBytes(payload, fmt.Sprintf("request.contents.%d.parts.%d.functionCall", ci, pi))
				if antigravityFunctionCallKey(fc.Get("name").String(), fc.Get("args").Raw, "") == wantedKey {
					return ci, pi, true
				}
			}
			return -1, -1, false
		}
	}
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return -1, -1, false
	}
	contentArr := contents.Array()
	cachedCI := int(itemResult.Get("contentIndex").Int())
	if targetOccurrence := itemResult.Get("targetOccurrence"); targetOccurrence.Exists() {
		if cachedCI < 0 || cachedCI >= len(contentArr) || !antigravityReplayItemContextMatches(payload, itemResult, cachedCI) {
			return -1, -1, false
		}
		wantedOccurrence := int(targetOccurrence.Int())
		occurrence := 0
		for pi, part := range contentArr[cachedCI].Get("parts").Array() {
			fc := part.Get("functionCall")
			if !fc.Exists() || antigravityFunctionCallKey(fc.Get("name").String(), fc.Get("args").Raw, "") != wantedKey {
				continue
			}
			if occurrence == wantedOccurrence {
				return cachedCI, pi, true
			}
			occurrence++
		}
		return -1, -1, false
	}

	matches := make([][2]int, 0, 1)
	for ci, content := range contentArr {
		if !antigravityReplayItemContextMatches(payload, itemResult, ci) {
			continue
		}
		for pi, part := range content.Get("parts").Array() {
			fc := part.Get("functionCall")
			if fc.Exists() && antigravityFunctionCallKey(fc.Get("name").String(), fc.Get("args").Raw, "") == wantedKey {
				matches = append(matches, [2]int{ci, pi})
			}
		}
	}
	if len(matches) == 1 {
		return matches[0][0], matches[0][1], true
	}
	return -1, -1, false
}

func insertAntigravityModelFunctionCallBeforeContent(payload []byte, beforeIndex int, name, callID, thoughtSig string, args gjson.Result) ([]byte, bool) {
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return payload, false
	}
	arr := contents.Array()
	if beforeIndex < 0 || beforeIndex > len(arr) {
		return payload, false
	}
	fc := map[string]any{"name": name}
	if callID != "" {
		fc["id"] = callID
	}
	if args.Exists() {
		fc["args"] = args.Value()
	}
	part := map[string]any{"functionCall": fc}
	if thoughtSig == "" {
		thoughtSig = "skip_thought_signature_validator"
	}
	part["thoughtSignature"] = thoughtSig
	newContent := map[string]any{
		"role":  "model",
		"parts": []any{part},
	}
	newArr := make([]any, 0, len(arr)+1)
	for i := 0; i < beforeIndex; i++ {
		newArr = append(newArr, arr[i].Value())
	}
	newArr = append(newArr, newContent)
	for i := beforeIndex; i < len(arr); i++ {
		newArr = append(newArr, arr[i].Value())
	}
	updated, err := sjson.SetBytes(payload, "request.contents", newArr)
	if err != nil {
		return payload, false
	}
	return updated, true
}

func appendAntigravityFunctionCallToModelContent(payload []byte, contentIndex int, name, callID, thoughtSig string, args gjson.Result) ([]byte, bool) {
	contentPath := fmt.Sprintf("request.contents.%d", contentIndex)
	if !strings.EqualFold(strings.TrimSpace(gjson.GetBytes(payload, contentPath+".role").String()), "model") || !gjson.GetBytes(payload, contentPath+".parts").IsArray() {
		return payload, false
	}
	fc := map[string]any{"name": name}
	if callID != "" {
		fc["id"] = callID
	}
	if args.Exists() {
		fc["args"] = args.Value()
	}
	part := map[string]any{"functionCall": fc}
	if thoughtSig == "" {
		hasFunctionCall := false
		gjson.GetBytes(payload, contentPath+".parts").ForEach(func(_, existingPart gjson.Result) bool {
			hasFunctionCall = existingPart.Get("functionCall").Exists()
			return !hasFunctionCall
		})
		if !hasFunctionCall {
			thoughtSig = "skip_thought_signature_validator"
		}
	}
	if thoughtSig != "" {
		part["thoughtSignature"] = thoughtSig
	}
	updated, errSet := sjson.SetBytes(payload, contentPath+".parts.-1", part)
	if errSet != nil {
		return payload, false
	}
	return updated, true
}

func antigravityRemoveThoughtSignatureFromOtherParts(payload []byte, contentIndex int, signature, keepPartPath string) []byte {
	signature = strings.TrimSpace(signature)
	partsPath := fmt.Sprintf("request.contents.%d.parts", contentIndex)
	parts := gjson.GetBytes(payload, partsPath)
	if signature == "" || !parts.IsArray() {
		return payload
	}
	out := payload
	for partIndex, part := range parts.Array() {
		partPath := fmt.Sprintf("%s.%d", partsPath, partIndex)
		if partPath == keepPartPath || antigravityNativePartThoughtSignature(part) != signature {
			continue
		}
		for _, field := range []string{"thoughtSignature", "thought_signature", "extra_content.google.thought_signature"} {
			out, _ = sjson.DeleteBytes(out, partPath+"."+field)
		}
	}
	return out
}

func antigravityRequestHasThoughtSignatureAt(payload []byte, itemResult gjson.Result) bool {
	partPath, ok := antigravityThoughtSignatureReplayPartPath(payload, itemResult)
	if !ok {
		return false
	}
	return antigravityHasNativeThoughtSignature(gjson.GetBytes(payload, partPath+".thoughtSignature").String())
}

func antigravityHasNativeThoughtSignature(signature string) bool {
	signature = strings.TrimSpace(signature)
	return signature != "" && signature != "skip_thought_signature_validator"
}

func antigravityReplayPartFingerprint(part gjson.Result) (kind, fingerprint string) {
	if part.Get("functionCall").Exists() || part.Get("functionResponse").Exists() {
		return "", ""
	}
	text := part.Get("text")
	if !text.Exists() {
		return "", ""
	}
	kind = "text"
	if part.Get("thought").Bool() {
		kind = "thought"
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + text.String()))
	return kind, fmt.Sprintf("%x", sum[:])
}

func antigravityReplayPartOccurrence(parts []gjson.Result, targetPartIndex int, targetKind, targetHash string) int {
	occurrence := 0
	for partIndex := 0; partIndex < targetPartIndex && partIndex < len(parts); partIndex++ {
		kind, fingerprint := antigravityReplayPartFingerprint(parts[partIndex])
		if kind == targetKind && fingerprint == targetHash {
			occurrence++
		}
	}
	return occurrence
}

func antigravityReplayContextFingerprint(payload []byte, beforeContentIndex int) string {
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() || beforeContentIndex < 0 {
		return ""
	}
	contentArr := contents.Array()
	if beforeContentIndex > len(contentArr) {
		return ""
	}
	var context strings.Builder
	for _, path := range []string{"request.systemInstruction", "request.tools", "request.toolConfig"} {
		if value := gjson.GetBytes(payload, path); value.Exists() {
			context.WriteString(path)
			context.WriteByte('\x00')
			context.Write(antigravityCanonicalReplayJSON([]byte(value.Raw)))
			context.WriteByte('\x00')
		}
	}
	for ci := 0; ci < beforeContentIndex; ci++ {
		content := contentArr[ci]
		context.WriteString(strings.ToLower(strings.TrimSpace(content.Get("role").String())))
		context.WriteByte('\x00')
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			normalized := []byte(part.Raw)
			for _, signaturePath := range []string{"thoughtSignature", "thought_signature", "extra_content.google.thought_signature"} {
				normalized, _ = sjson.DeleteBytes(normalized, signaturePath)
			}
			context.Write(antigravityCanonicalReplayJSON(normalized))
			context.WriteByte('\x00')
			return true
		})
	}
	if context.Len() == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(context.String()))
	return fmt.Sprintf("%x", sum[:])
}

func antigravityCanonicalReplayJSON(raw []byte) []byte {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return bytes.TrimSpace(raw)
	}
	canonical, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return bytes.TrimSpace(raw)
	}
	return canonical
}

func antigravityReplayItemContextMatches(payload []byte, itemResult gjson.Result, contentIndex int) bool {
	expected := strings.TrimSpace(itemResult.Get("contextHash").String())
	return expected == "" || expected == antigravityReplayContextFingerprint(payload, contentIndex)
}

func antigravitySetReplayItemContextHash(item []byte, payload []byte, contentIndex int) []byte {
	if contextHash := antigravityReplayContextFingerprint(payload, contentIndex); contextHash != "" {
		item, _ = sjson.SetBytes(item, "contextHash", contextHash)
	}
	return item
}

func antigravityThoughtSignatureReplayPartPath(payload []byte, itemResult gjson.Result) (string, bool) {
	ci := int(itemResult.Get("contentIndex").Int())
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return "", false
	}
	contentArr := contents.Array()
	if ci < 0 || ci >= len(contentArr) || !strings.EqualFold(strings.TrimSpace(contentArr[ci].Get("role").String()), "model") {
		return "", false
	}
	if !antigravityReplayItemContextMatches(payload, itemResult, ci) {
		return "", false
	}
	parts := contentArr[ci].Get("parts")
	if !parts.IsArray() {
		return "", false
	}
	partArr := parts.Array()
	targetKind := strings.TrimSpace(itemResult.Get("targetKind").String())
	targetHash := strings.TrimSpace(itemResult.Get("targetHash").String())
	if targetHash != "" {
		if targetOccurrence := itemResult.Get("targetOccurrence"); targetOccurrence.Exists() {
			wanted := int(targetOccurrence.Int())
			occurrence := 0
			for pi, part := range partArr {
				kind, fingerprint := antigravityReplayPartFingerprint(part)
				if fingerprint != targetHash || (targetKind != "" && kind != targetKind) {
					continue
				}
				if occurrence == wanted {
					return fmt.Sprintf("request.contents.%d.parts.%d", ci, pi), true
				}
				occurrence++
			}
			return "", false
		}
		pi := int(itemResult.Get("partIndex").Int())
		if pi >= 0 && pi < len(partArr) {
			kind, fingerprint := antigravityReplayPartFingerprint(partArr[pi])
			if fingerprint == targetHash && (targetKind == "" || kind == targetKind) {
				return fmt.Sprintf("request.contents.%d.parts.%d", ci, pi), true
			}
		}
		for pi, part := range partArr {
			kind, fingerprint := antigravityReplayPartFingerprint(part)
			if fingerprint == targetHash && (targetKind == "" || kind == targetKind) {
				return fmt.Sprintf("request.contents.%d.parts.%d", ci, pi), true
			}
		}
		return "", false
	}

	pi := int(itemResult.Get("partIndex").Int())
	if pi >= 0 && pi < len(partArr) && partArr[pi].Type != gjson.Null {
		if kind, _ := antigravityReplayPartFingerprint(partArr[pi]); kind != "" {
			return fmt.Sprintf("request.contents.%d.parts.%d", ci, pi), true
		}
	}
	// Legacy cache entries may point at a streamed signature-only part after
	// multiple text chunks. Attach them to the last semantic part in the same
	// model content, never to a different turn.
	for candidate := len(partArr) - 1; candidate >= 0; candidate-- {
		if kind, _ := antigravityReplayPartFingerprint(partArr[candidate]); kind != "" {
			return fmt.Sprintf("request.contents.%d.parts.%d", ci, candidate), true
		}
	}
	return "", false
}

func antigravityExistingReplayPartPath(payload []byte, contentIndex int, partIndex int) (string, bool) {
	if contentIndex < 0 || partIndex < 0 {
		return "", false
	}
	partsPath := fmt.Sprintf("request.contents.%d.parts", contentIndex)
	parts := gjson.GetBytes(payload, partsPath)
	if !parts.IsArray() {
		return "", false
	}
	arr := parts.Array()
	if partIndex >= len(arr) || arr[partIndex].Type == gjson.Null {
		return "", false
	}
	return fmt.Sprintf("%s.%d", partsPath, partIndex), true
}

func antigravityReplayPartWritePath(payload []byte, contentIndex int, partIndex int) string {
	if path, ok := antigravityExistingReplayPartPath(payload, contentIndex, partIndex); ok {
		return path
	}
	partsPath := fmt.Sprintf("request.contents.%d.parts", contentIndex)
	if gjson.GetBytes(payload, partsPath).IsArray() {
		return partsPath + ".-1"
	}
	return partsPath + ".0"
}

func insertAntigravityReasoningReplayItems(payload []byte, items [][]byte) ([]byte, bool) {
	out := payload
	changed := false
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		switch strings.TrimSpace(itemResult.Get("type").String()) {
		case "thought_signature":
			sig := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
			if sig == "" {
				continue
			}
			partPath, exists := antigravityThoughtSignatureReplayPartPath(out, itemResult)
			if !exists {
				continue
			}
			path := partPath + ".thoughtSignature"
			if antigravityHasNativeThoughtSignature(gjson.GetBytes(out, path).String()) {
				continue
			}
			ci := int(itemResult.Get("contentIndex").Int())
			out = antigravityRemoveThoughtSignatureFromOtherParts(out, ci, sig, partPath)
			updated, err := sjson.SetBytes(out, path, sig)
			if err != nil {
				continue
			}
			out = updated
			changed = true
		case "function_call_part":
			updated, ok := mergeAntigravityFunctionCallPartReplay(out, itemResult)
			if ok {
				out = updated
				changed = true
			}
		}
	}
	return out, changed
}

func mergeAntigravityFunctionCallPartReplay(payload []byte, itemResult gjson.Result) ([]byte, bool) {
	name := strings.TrimSpace(itemResult.Get("name").String())
	args := itemResult.Get("args")
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	sig := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
	if name == "" || !args.Exists() {
		return payload, false
	}
	if ci, pi, exists := antigravityFunctionCallPartLocationForReplay(payload, itemResult); exists {
		if sig != "" {
			partPath := fmt.Sprintf("request.contents.%d.parts.%d", ci, pi)
			pathSig := partPath + ".thoughtSignature"
			if !antigravityHasNativeThoughtSignature(gjson.GetBytes(payload, pathSig).String()) {
				payload = antigravityRemoveThoughtSignatureFromOtherParts(payload, ci, sig, partPath)
				if updated, err := sjson.SetBytes(payload, pathSig, sig); err == nil {
					return updated, true
				}
			}
		}
		return payload, false
	}
	if callID != "" {
		if antigravityPayloadHasFunctionCallID(payload, callID) {
			// The ID is present but its semantic payload did not match above. Never
			// replay or reinsert an opaque signature onto that changed call.
			return payload, false
		}
		if frIndex, ok := antigravityFunctionResponseContentIndex(payload, callID); ok {
			if antigravityReplayItemContextMatches(payload, itemResult, frIndex) {
				return insertAntigravityModelFunctionCallBeforeContent(payload, frIndex, name, callID, sig, args)
			}
			parallelModelIndex := frIndex - 1
			if parallelModelIndex >= 0 && antigravityReplayItemContextMatches(payload, itemResult, parallelModelIndex) {
				if updated, appended := appendAntigravityFunctionCallToModelContent(payload, parallelModelIndex, name, callID, sig, args); appended {
					return updated, true
				}
			}
		}
	} else {
		// Without a native call ID, only an exact semantic match is safe. Never
		// put an opaque signature on a different call at the old numeric slot.
		return payload, false
	}

	ci := antigravityReasoningReplayResolveContentIndex(payload, int(itemResult.Get("contentIndex").Int()))
	if ci < 0 || !antigravityReplayItemContextMatches(payload, itemResult, ci) {
		return payload, false
	}
	pi := int(itemResult.Get("partIndex").Int())
	out := payload
	changed := false

	partPath, exists := antigravityExistingReplayPartPath(out, ci, pi)
	if !exists {
		fc := map[string]any{"name": name}
		if callID != "" {
			fc["id"] = callID
		}
		if args.Type == gjson.String {
			fc["args"] = args.String()
		} else {
			var parsed any
			if json.Unmarshal([]byte(args.Raw), &parsed) == nil {
				fc["args"] = parsed
			}
		}
		part := map[string]any{"functionCall": fc}
		if sig != "" {
			part["thoughtSignature"] = sig
		}
		if updated, err := sjson.SetBytes(out, antigravityReplayPartWritePath(out, ci, pi), part); err == nil {
			return updated, true
		}
		return payload, false
	}

	pathSig := partPath + ".thoughtSignature"
	if sig != "" && !antigravityHasNativeThoughtSignature(gjson.GetBytes(out, pathSig).String()) {
		out = antigravityRemoveThoughtSignatureFromOtherParts(out, ci, sig, partPath)
		if updated, err := sjson.SetBytes(out, pathSig, sig); err == nil {
			out = updated
			changed = true
		}
	}
	pathFC := partPath + ".functionCall"
	if !gjson.GetBytes(out, pathFC).Exists() {
		fc := map[string]any{"name": name}
		if callID != "" {
			fc["id"] = callID
		}
		if args.Type == gjson.String {
			fc["args"] = args.String()
		} else {
			var parsed any
			if json.Unmarshal([]byte(args.Raw), &parsed) == nil {
				fc["args"] = parsed
			}
		}
		if updated, err := sjson.SetBytes(out, pathFC, fc); err == nil {
			out = updated
			changed = true
		}
	}
	return out, changed
}

type antigravityPendingThoughtSignature struct {
	signature  string
	targetKind string
}

type antigravityReasoningReplayAccumulator struct {
	scope                   antigravityReasoningReplayScope
	requestPayload          []byte
	items                   [][]byte
	seenFC                  map[string]bool
	seenSignatures          map[string]bool
	segmentOccurrences      map[string]int
	functionCallOccurrences map[string]int
	contentIndex            int
	nextPartIndex           int
	visibleText             strings.Builder
	thoughtText             strings.Builder
	visiblePartIndex        int
	thoughtPartIndex        int
	lastResponseKind        string
	pendingSignatures       []antigravityPendingThoughtSignature
	itemBytes               int
	overflow                bool
	terminal                bool
}

func newAntigravityReasoningReplayAccumulator(scope antigravityReasoningReplayScope, requestPayload []byte) *antigravityReasoningReplayAccumulator {
	if !scope.valid() {
		return nil
	}
	contentIndex, basePartIndex := antigravityReasoningReplayPendingModelContentIndex(requestPayload)
	items := antigravityReasoningReplayItemsFromRequest(requestPayload)
	seenSignatures := make(map[string]bool, len(items))
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		if signature := strings.TrimSpace(itemResult.Get("thoughtSignature").String()); signature != "" {
			seenSignatures[signature] = true
		}
	}
	itemBytes := 0
	for _, item := range items {
		itemBytes += len(item)
	}
	segmentOccurrences := make(map[string]int)
	functionCallOccurrences := make(map[string]int)
	if parts := gjson.GetBytes(requestPayload, fmt.Sprintf("request.contents.%d.parts", contentIndex)); parts.IsArray() {
		parts.ForEach(func(_, part gjson.Result) bool {
			if fc := part.Get("functionCall"); fc.Exists() {
				key := antigravityFunctionCallKey(fc.Get("name").String(), fc.Get("args").Raw, "")
				if key != "" {
					functionCallOccurrences[key]++
				}
				return true
			}
			if kind, fingerprint := antigravityReplayPartFingerprint(part); fingerprint != "" {
				segmentOccurrences[kind+"\x00"+fingerprint]++
			}
			return true
		})
	}
	return &antigravityReasoningReplayAccumulator{
		scope:                   scope,
		requestPayload:          append([]byte(nil), requestPayload...),
		items:                   items,
		seenFC:                  make(map[string]bool),
		seenSignatures:          seenSignatures,
		segmentOccurrences:      segmentOccurrences,
		functionCallOccurrences: functionCallOccurrences,
		contentIndex:            contentIndex,
		nextPartIndex:           basePartIndex,
		visiblePartIndex:        -1,
		thoughtPartIndex:        -1,
		itemBytes:               itemBytes,
		overflow:                len(items) > internalcache.AntigravityReasoningReplayCacheMaxItemsPerEntry || itemBytes > internalcache.AntigravityReasoningReplayCacheMaxBytesPerEntry,
	}
}

func antigravityReasoningReplayItemsFromRequest(payload []byte) [][]byte {
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return nil
	}
	items := make([][]byte, 0)
	contents.ForEach(func(contentKey, content gjson.Result) bool {
		if !strings.EqualFold(strings.TrimSpace(content.Get("role").String()), "model") {
			return true
		}
		ci := int(contentKey.Int())
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		partArr := parts.Array()
		functionCallOccurrences := make(map[string]int)
		for pi, part := range partArr {
			signature := antigravityNativePartThoughtSignature(part)
			if !antigravityHasNativeThoughtSignature(signature) {
				signature = ""
			}
			if fc := part.Get("functionCall"); fc.Exists() {
				key := antigravityFunctionCallKey(fc.Get("name").String(), fc.Get("args").Raw, "")
				occurrence := functionCallOccurrences[key]
				if key != "" {
					functionCallOccurrences[key] = occurrence + 1
				}
				if item := buildAntigravityFunctionCallPartItem(ci, pi, occurrence, fc, signature); len(item) > 0 {
					items = append(items, antigravitySetReplayItemContextHash(item, payload, ci))
				}
				continue
			}
			if signature == "" {
				continue
			}
			targetPart := part
			targetPI := pi
			kind, fingerprint := antigravityReplayPartFingerprint(targetPart)
			if fingerprint == "" && pi > 0 {
				targetPI = pi - 1
				targetPart = partArr[targetPI]
				kind, fingerprint = antigravityReplayPartFingerprint(targetPart)
			}
			if fingerprint == "" {
				continue
			}
			item := buildAntigravityThoughtSignatureItem(ci, targetPI, signature, kind, fingerprint)
			item, _ = sjson.SetBytes(item, "targetOccurrence", antigravityReplayPartOccurrence(partArr, targetPI, kind, fingerprint))
			items = append(items, antigravitySetReplayItemContextHash(item, payload, ci))
		}
		return true
	})
	return items
}

func (a *antigravityReasoningReplayAccumulator) appendItem(item []byte) {
	if a == nil || len(item) == 0 || a.overflow {
		return
	}
	if len(a.items)+1 > internalcache.AntigravityReasoningReplayCacheMaxItemsPerEntry || a.itemBytes+len(item) > internalcache.AntigravityReasoningReplayCacheMaxBytesPerEntry {
		a.overflow = true
		return
	}
	a.items = append(a.items, item)
	a.itemBytes += len(item)
}

func (a *antigravityReasoningReplayAccumulator) attachDetachedSignatureToLastFunctionCall(signature string) {
	if a == nil || signature == "" {
		return
	}
	for itemIndex := len(a.items) - 1; itemIndex >= 0; itemIndex-- {
		item := gjson.ParseBytes(a.items[itemIndex])
		if item.Get("type").String() != "function_call_part" {
			continue
		}
		if strings.TrimSpace(item.Get("thoughtSignature").String()) != "" {
			return
		}
		updated, errSet := sjson.SetBytes(a.items[itemIndex], "thoughtSignature", signature)
		if errSet != nil {
			return
		}
		delta := len(updated) - len(a.items[itemIndex])
		if a.itemBytes+delta > internalcache.AntigravityReasoningReplayCacheMaxBytesPerEntry {
			a.overflow = true
			return
		}
		a.items[itemIndex] = updated
		a.itemBytes += delta
		return
	}
}

func (a *antigravityReasoningReplayAccumulator) ObserveSSELine(line []byte) {
	if a == nil {
		return
	}
	payload := helps.JSONPayload(line)
	if payload == nil {
		return
	}
	a.observeResponsePayload(payload)
}

func (a *antigravityReasoningReplayAccumulator) observeResponsePayload(payload []byte) {
	if finishReason := strings.TrimSpace(gjson.GetBytes(payload, "response.candidates.0.finishReason").String()); finishReason != "" {
		a.terminal = true
	}
	parts := gjson.GetBytes(payload, "response.candidates.0.content.parts")
	if !parts.IsArray() {
		return
	}
	parts.ForEach(func(_, part gjson.Result) bool {
		pi := a.nextPartIndex
		a.nextPartIndex++
		signature := antigravityNativePartThoughtSignature(part)
		if !antigravityHasNativeThoughtSignature(signature) {
			signature = ""
		}
		if fc := part.Get("functionCall"); fc.Exists() {
			if a.lastResponseKind == "text" || a.lastResponseKind == "thought" {
				a.flushPendingThoughtSignaturesForKind(a.lastResponseKind)
			}
			if signature != "" {
				remainingPending := a.pendingSignatures[:0]
				for _, pending := range a.pendingSignatures {
					if pending.targetKind != "" {
						remainingPending = append(remainingPending, pending)
					}
				}
				a.pendingSignatures = remainingPending
			}
			if signature == "" {
				for pendingIndex := len(a.pendingSignatures) - 1; pendingIndex >= 0; pendingIndex-- {
					if a.pendingSignatures[pendingIndex].targetKind == "" {
						signature = a.pendingSignatures[pendingIndex].signature
						a.pendingSignatures = append(a.pendingSignatures[:pendingIndex], a.pendingSignatures[pendingIndex+1:]...)
						break
					}
				}
			}
			keys := antigravityReplayToolCallKeysFromPart(fc)
			for _, key := range keys {
				dedupeKey := key + "\x00" + signature
				if signature == "" {
					dedupeKey = fmt.Sprintf("%s\x00part:%d", key, pi)
				}
				if a.seenFC[dedupeKey] {
					return true
				}
				a.seenFC[dedupeKey] = true
			}
			occurrenceKey := antigravityFunctionCallKey(fc.Get("name").String(), fc.Get("args").Raw, "")
			occurrence := a.functionCallOccurrences[occurrenceKey]
			if occurrenceKey != "" {
				a.functionCallOccurrences[occurrenceKey] = occurrence + 1
			}
			item := buildAntigravityFunctionCallPartItem(a.contentIndex, pi, occurrence, fc, signature)
			if len(item) > 0 {
				a.appendItem(antigravitySetReplayItemContextHash(item, a.requestPayload, a.contentIndex))
				if signature != "" {
					a.seenSignatures[signature] = true
				}
			}
			a.lastResponseKind = "function_call"
			return true
		}

		targetKind := ""
		if part.Get("thought").Bool() {
			targetKind = "thought"
		}
		text := part.Get("text")
		hasSemanticText := text.Exists() && text.String() != ""
		signatureOnly := signature != "" && !hasSemanticText
		if signatureOnly && a.lastResponseKind == "function_call" {
			if !a.seenSignatures[signature] {
				a.attachDetachedSignatureToLastFunctionCall(signature)
				a.seenSignatures[signature] = true
			}
			return true
		}
		if hasSemanticText {
			if targetKind != "thought" {
				targetKind = "text"
			}
			if signature != "" {
				remainingPending := a.pendingSignatures[:0]
				for _, pending := range a.pendingSignatures {
					unboundPrefix := pending.targetKind == ""
					if pending.targetKind == targetKind {
						unboundPrefix = (targetKind == "text" && a.visibleText.Len() == 0) || (targetKind == "thought" && a.thoughtText.Len() == 0)
					}
					if unboundPrefix {
						if pending.signature == signature {
							delete(a.seenSignatures, signature)
						}
						continue
					}
					remainingPending = append(remainingPending, pending)
				}
				a.pendingSignatures = remainingPending
				for _, pending := range a.pendingSignatures {
					if pending.targetKind == targetKind && pending.signature != signature {
						a.flushPendingThoughtSignaturesForKind(targetKind)
						break
					}
				}
			}
			if a.lastResponseKind != "" && a.lastResponseKind != targetKind && (a.lastResponseKind == "text" || a.lastResponseKind == "thought") {
				a.flushPendingThoughtSignaturesForKind(a.lastResponseKind)
			}
			if targetKind == "thought" {
				if a.thoughtText.Len() == 0 {
					a.thoughtPartIndex = pi
				}
				a.thoughtText.WriteString(text.String())
			} else {
				if a.visibleText.Len() == 0 {
					a.visiblePartIndex = pi
				}
				a.visibleText.WriteString(text.String())
			}
			a.lastResponseKind = targetKind
		}
		acceptedSignature := false
		if signature != "" && !a.seenSignatures[signature] {
			if targetKind == "" {
				targetKind = a.lastResponseKind
			}
			unmatchedDetachedCarrier := signatureOnly && a.lastResponseKind == targetKind && ((targetKind == "text" && a.visibleText.Len() == 0) || (targetKind == "thought" && a.thoughtText.Len() == 0))
			if unmatchedDetachedCarrier {
				a.seenSignatures[signature] = true
			} else if len(a.pendingSignatures)+len(a.items)+1 > internalcache.AntigravityReasoningReplayCacheMaxItemsPerEntry || a.itemBytes+len(signature) > internalcache.AntigravityReasoningReplayCacheMaxBytesPerEntry {
				a.overflow = true
				a.seenSignatures[signature] = true
			} else {
				a.pendingSignatures = append(a.pendingSignatures, antigravityPendingThoughtSignature{signature: signature, targetKind: targetKind})
				a.seenSignatures[signature] = true
				acceptedSignature = true
			}
		}
		if acceptedSignature && (signatureOnly || hasSemanticText) {
			switch targetKind {
			case "text":
				if a.visibleText.Len() > 0 {
					a.flushPendingThoughtSignaturesForKind("text")
				}
			case "thought":
				if a.thoughtText.Len() > 0 {
					a.flushPendingThoughtSignaturesForKind("thought")
				}
			}
		}
		return true
	})
}

func buildAntigravityThoughtSignatureItem(contentIndex, partIndex int, signature, targetKind, targetHash string) []byte {
	item := []byte(fmt.Sprintf(`{"type":"thought_signature","thoughtSignature":%q,"contentIndex":%d,"partIndex":%d}`,
		signature, contentIndex, partIndex))
	if targetKind != "" {
		item, _ = sjson.SetBytes(item, "targetKind", targetKind)
	}
	if targetHash != "" {
		item, _ = sjson.SetBytes(item, "targetHash", targetHash)
	}
	return item
}

func buildAntigravityFunctionCallPartItem(contentIndex, partIndex, targetOccurrence int, fc gjson.Result, signature string) []byte {
	item := map[string]any{
		"type":             "function_call_part",
		"contentIndex":     contentIndex,
		"partIndex":        partIndex,
		"targetOccurrence": targetOccurrence,
		"name":             fc.Get("name").String(),
	}
	if id := strings.TrimSpace(fc.Get("id").String()); id != "" {
		item["call_id"] = id
	}
	if args := fc.Get("args"); args.Exists() {
		if args.Type == gjson.String {
			item["args"] = args.String()
		} else {
			item["args"] = json.RawMessage(args.Raw)
		}
	}
	if signature != "" {
		item["thoughtSignature"] = signature
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil
	}
	return raw
}

func (a *antigravityReasoningReplayAccumulator) flushPendingThoughtSignaturesForKind(targetKind string) {
	if a == nil || (targetKind != "text" && targetKind != "thought") {
		return
	}
	text := a.visibleText.String()
	partIndex := a.visiblePartIndex
	if targetKind == "thought" {
		text = a.thoughtText.String()
		partIndex = a.thoughtPartIndex
	}
	targetHash := ""
	targetOccurrence := 0
	if text != "" {
		sum := sha256.Sum256([]byte(targetKind + "\x00" + text))
		targetHash = fmt.Sprintf("%x", sum[:])
		occurrenceKey := targetKind + "\x00" + targetHash
		targetOccurrence = a.segmentOccurrences[occurrenceKey]
		a.segmentOccurrences[occurrenceKey] = targetOccurrence + 1
	}
	remaining := a.pendingSignatures[:0]
	for _, pending := range a.pendingSignatures {
		if pending.targetKind != targetKind || targetHash == "" {
			remaining = append(remaining, pending)
			continue
		}
		item := buildAntigravityThoughtSignatureItem(a.contentIndex, partIndex, pending.signature, targetKind, targetHash)
		item, _ = sjson.SetBytes(item, "targetOccurrence", targetOccurrence)
		a.appendItem(antigravitySetReplayItemContextHash(item, a.requestPayload, a.contentIndex))
	}
	a.pendingSignatures = remaining
	if targetKind == "thought" {
		a.thoughtText.Reset()
		a.thoughtPartIndex = -1
	} else {
		a.visibleText.Reset()
		a.visiblePartIndex = -1
	}
}

func (a *antigravityReasoningReplayAccumulator) appendPendingThoughtSignatures() {
	if a == nil {
		return
	}
	for index := range a.pendingSignatures {
		if a.pendingSignatures[index].targetKind != "" {
			continue
		}
		switch {
		case a.lastResponseKind == "text" && a.visibleText.Len() > 0:
			a.pendingSignatures[index].targetKind = "text"
		case a.lastResponseKind == "thought" && a.thoughtText.Len() > 0:
			a.pendingSignatures[index].targetKind = "thought"
		case a.visibleText.Len() > 0:
			a.pendingSignatures[index].targetKind = "text"
		case a.thoughtText.Len() > 0:
			a.pendingSignatures[index].targetKind = "thought"
		}
	}
	a.flushPendingThoughtSignaturesForKind("thought")
	a.flushPendingThoughtSignaturesForKind("text")
	a.pendingSignatures = nil
}

func (a *antigravityReasoningReplayAccumulator) Commit(ctx context.Context) {
	if a == nil || !a.scope.valid() || !a.terminal {
		return
	}
	if a.overflow {
		_, _ = internalcache.DeleteAntigravityReasoningReplayItemsIfUnchanged(ctx, a.scope.modelName, a.scope.sessionKey, a.scope.cacheSnapshot)
		return
	}
	a.appendPendingThoughtSignatures()
	if a.overflow {
		_, _ = internalcache.DeleteAntigravityReasoningReplayItemsIfUnchanged(ctx, a.scope.modelName, a.scope.sessionKey, a.scope.cacheSnapshot)
		return
	}
	if len(a.items) == 0 {
		_, _ = internalcache.DeleteAntigravityReasoningReplayItemsIfUnchanged(ctx, a.scope.modelName, a.scope.sessionKey, a.scope.cacheSnapshot)
		return
	}
	if _, errReplace := internalcache.ReplaceAntigravityReasoningReplayItemsIfUnchanged(ctx, a.scope.modelName, a.scope.sessionKey, a.scope.cacheSnapshot, a.items); errReplace != nil {
		_, _ = internalcache.DeleteAntigravityReasoningReplayItemsIfUnchanged(ctx, a.scope.modelName, a.scope.sessionKey, a.scope.cacheSnapshot)
	}
}

func cacheAntigravityReasoningReplayFromResponse(ctx context.Context, scope antigravityReasoningReplayScope, requestPayload, body []byte) {
	if !scope.valid() || len(body) == 0 {
		return
	}
	acc := newAntigravityReasoningReplayAccumulator(scope, requestPayload)
	acc.observeResponsePayload(body)
	acc.Commit(ctx)
}

func applyAntigravityNativeSignatureReplayIfNeeded(modelName string, payload []byte) []byte {
	if antigravityUsesReasoningReplayCache(modelName) {
		return payload
	}
	// Native per-part signature replay is not on upstream/dev; Gemini uses HOME replay only.
	return payload
}

func antigravityUsesReasoningReplayCache(modelName string) bool {
	modelName = strings.ToLower(modelName)
	if strings.Contains(modelName, "claude") {
		return false
	}
	return strings.Contains(modelName, "gemini") || strings.Contains(modelName, "flash") || strings.Contains(modelName, "agent")
}

func antigravityNativePartThoughtSignature(part gjson.Result) string {
	for _, path := range []string{"thoughtSignature", "thought_signature", "extra_content.google.thought_signature"} {
		if signature := strings.TrimSpace(part.Get(path).String()); signature != "" {
			return signature
		}
	}
	return ""
}
