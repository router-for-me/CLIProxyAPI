package executor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type antigravityReasoningReplayScope struct {
	modelName  string
	sessionKey string
}

func (s antigravityReasoningReplayScope) valid() bool {
	return strings.TrimSpace(s.modelName) != "" && strings.TrimSpace(s.sessionKey) != ""
}

func antigravityReasoningReplayScopeFromPayload(modelName string, payload []byte) antigravityReasoningReplayScope {
	sessionID := antigravityReplaySessionIDFromPayload(payload)
	if sessionID == "" {
		return antigravityReasoningReplayScope{}
	}
	return antigravityReasoningReplayScope{
		modelName:  strings.TrimSpace(modelName),
		sessionKey: "session:" + sessionID,
	}
}

func antigravityReasoningReplayScopeFromRequest(ctx context.Context, modelName string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, payload []byte) antigravityReasoningReplayScope {
	if scope := antigravityReasoningReplayScopeFromPayload(modelName, payload); scope.valid() {
		return scope
	}
	if scope := antigravityReasoningReplayScopeFromPayload(modelName, req.Payload); scope.valid() {
		return scope
	}
	if value := metadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return antigravityReasoningReplayScope{modelName: modelName, sessionKey: "execution:" + value}
	}
	if value := metadataString(req.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); value != "" {
		return antigravityReasoningReplayScope{modelName: modelName, sessionKey: "execution:" + value}
	}
	_ = ctx
	return antigravityReasoningReplayScope{}
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
		ci := len(arr) - 1
		parts := last.Get("parts")
		base := 0
		if parts.IsArray() {
			base = len(parts.Array())
		}
		return ci, base
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
	for i := len(arr) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(arr[i].Get("role").String()), "model") {
			return i
		}
	}
	if len(arr) == 0 {
		return 0
	}
	return len(arr) - 1
}

func prepareAntigravityGeminiReasoningReplayPayload(ctx context.Context, modelName string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, payload []byte) ([]byte, antigravityReasoningReplayScope, error) {
	if !antigravityUsesReasoningReplayCache(modelName) {
		return payload, antigravityReasoningReplayScope{}, nil
	}
	return applyAntigravityReasoningReplayCache(ctx, modelName, req, opts, payload)
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
	return internalcache.DeleteAntigravityReasoningReplayItemRequired(ctx, scope.modelName, scope.sessionKey)
}

func applyAntigravityReasoningReplayCache(ctx context.Context, modelName string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, payload []byte) ([]byte, antigravityReasoningReplayScope, error) {
	scope := antigravityReasoningReplayScopeFromRequest(ctx, modelName, req, opts, payload)
	if !scope.valid() {
		return payload, scope, nil
	}
	items, ok, err := internalcache.GetAntigravityReasoningReplayItemsRequired(ctx, scope.modelName, scope.sessionKey)
	if err != nil || !ok || len(items) == 0 {
		return payload, scope, err
	}
	items = filterAntigravityReasoningReplayItemsForRequest(payload, items)
	if len(items) == 0 {
		return payload, scope, nil
	}
	updated, okApply := insertAntigravityReasoningReplayItems(payload, items)
	if !okApply {
		return payload, scope, nil
	}
	return updated, scope, nil
}

func filterAntigravityReasoningReplayItemsForRequest(payload []byte, items [][]byte) [][]byte {
	existing := antigravityExistingToolCallKeys(payload)
	filtered := make([][]byte, 0, len(items))
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		switch strings.TrimSpace(itemResult.Get("type").String()) {
		case "function_call_part":
			keys := antigravityReplayToolCallKeys(itemResult)
			if len(keys) == 0 || antigravityAnyKeyExists(existing, keys) {
				continue
			}
			if !antigravityRequestHasMatchingFunctionResponse(payload, itemResult) {
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

func antigravityRequestHasMatchingFunctionResponse(payload []byte, itemResult gjson.Result) bool {
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	if callID == "" {
		return true
	}
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return false
	}
	for _, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for _, part := range parts.Array() {
			fr := part.Get("functionResponse")
			if fr.Exists() && strings.TrimSpace(fr.Get("id").String()) == callID {
				return true
			}
		}
	}
	return false
}

func antigravityRequestHasThoughtSignatureAt(payload []byte, itemResult gjson.Result) bool {
	ci := int(itemResult.Get("contentIndex").Int())
	pi := int(itemResult.Get("partIndex").Int())
	path := fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", ci, pi)
	return strings.TrimSpace(gjson.GetBytes(payload, path).String()) != ""
}

func insertAntigravityReasoningReplayItems(payload []byte, items [][]byte) ([]byte, bool) {
	out := payload
	changed := false
	for _, item := range items {
		itemResult := gjson.ParseBytes(item)
		switch strings.TrimSpace(itemResult.Get("type").String()) {
		case "thought_signature":
			ci := antigravityReasoningReplayResolveContentIndex(out, int(itemResult.Get("contentIndex").Int()))
			pi := int(itemResult.Get("partIndex").Int())
			sig := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
			if sig == "" {
				continue
			}
			path := fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", ci, pi)
			if strings.TrimSpace(gjson.GetBytes(out, path).String()) != "" {
				continue
			}
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
	ci := antigravityReasoningReplayResolveContentIndex(payload, int(itemResult.Get("contentIndex").Int()))
	pi := int(itemResult.Get("partIndex").Int())
	sig := strings.TrimSpace(itemResult.Get("thoughtSignature").String())
	pathSig := fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", ci, pi)
	out := payload
	changed := false
	if sig != "" && strings.TrimSpace(gjson.GetBytes(out, pathSig).String()) == "" {
		if updated, err := sjson.SetBytes(out, pathSig, sig); err == nil {
			out = updated
			changed = true
		}
	}
	name := strings.TrimSpace(itemResult.Get("name").String())
	args := itemResult.Get("args")
	callID := strings.TrimSpace(itemResult.Get("call_id").String())
	pathFC := fmt.Sprintf("request.contents.%d.parts.%d.functionCall", ci, pi)
	if !gjson.GetBytes(out, pathFC).Exists() && name != "" && args.Exists() {
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

type antigravityReasoningReplayAccumulator struct {
	scope          antigravityReasoningReplayScope
	requestPayload []byte
	items          [][]byte
	seenFC         map[string]bool
	contentIndex   int
	nextPartIndex  int
}

func newAntigravityReasoningReplayAccumulator(scope antigravityReasoningReplayScope, requestPayload []byte) *antigravityReasoningReplayAccumulator {
	if !scope.valid() {
		return nil
	}
	contentIndex, basePartIndex := antigravityReasoningReplayPendingModelContentIndex(requestPayload)
	return &antigravityReasoningReplayAccumulator{
		scope:          scope,
		requestPayload: append([]byte(nil), requestPayload...),
		seenFC:         make(map[string]bool),
		contentIndex:   contentIndex,
		nextPartIndex:  basePartIndex,
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
	parts := gjson.GetBytes(payload, "response.candidates.0.content.parts")
	if !parts.IsArray() {
		return
	}
	parts.ForEach(func(_, part gjson.Result) bool {
		pi := a.nextPartIndex
		a.nextPartIndex++
		sig := antigravityNativePartThoughtSignature(part)
		if fc := part.Get("functionCall"); fc.Exists() {
			keys := antigravityReplayToolCallKeysFromPart(fc)
			for _, k := range keys {
				if a.seenFC[k] {
					return true
				}
			}
			for _, k := range keys {
				a.seenFC[k] = true
			}
			item := buildAntigravityFunctionCallPartItem(a.contentIndex, pi, fc, sig)
			if len(item) > 0 {
				a.items = append(a.items, item)
			}
			return true
		}
		if sig != "" {
			item := buildAntigravityThoughtSignatureItem(a.contentIndex, pi, sig)
			a.items = append(a.items, item)
		}
		return true
	})
}

func buildAntigravityThoughtSignatureItem(contentIndex, partIndex int, signature string) []byte {
	return []byte(fmt.Sprintf(`{"type":"thought_signature","thoughtSignature":%q,"contentIndex":%d,"partIndex":%d}`,
		signature, contentIndex, partIndex))
}

func buildAntigravityFunctionCallPartItem(contentIndex, partIndex int, fc gjson.Result, signature string) []byte {
	item := map[string]any{
		"type":         "function_call_part",
		"contentIndex": contentIndex,
		"partIndex":    partIndex,
		"name":         fc.Get("name").String(),
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

func (a *antigravityReasoningReplayAccumulator) Flush(ctx context.Context) {
	if a == nil || !a.scope.valid() || len(a.items) == 0 {
		return
	}
	if !internalcache.CacheAntigravityReasoningReplayItemsBestEffort(ctx, a.scope.modelName, a.scope.sessionKey, a.items) {
		_ = internalcache.DeleteAntigravityReasoningReplayItemRequired(ctx, a.scope.modelName, a.scope.sessionKey)
	}
}

func cacheAntigravityReasoningReplayFromResponse(ctx context.Context, scope antigravityReasoningReplayScope, requestPayload, body []byte) {
	if !scope.valid() || len(body) == 0 {
		return
	}
	acc := newAntigravityReasoningReplayAccumulator(scope, requestPayload)
	acc.observeResponsePayload(body)
	acc.Flush(ctx)
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
