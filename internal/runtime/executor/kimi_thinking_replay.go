package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

type kimiThinkingReplayScope struct {
	modelFamily   string
	sessionKey    string
	snapshot      internalcache.KimiThinkingReplaySnapshot
	cacheReady    bool
	replayApplied bool
}

func (s kimiThinkingReplayScope) valid() bool {
	return strings.TrimSpace(s.modelFamily) != "" && strings.TrimSpace(s.sessionKey) != ""
}

func kimiThinkingReplayModelFamily(model string) string {
	baseModel := thinking.ParseSuffix(strings.TrimSpace(model)).ModelName
	normalized := normalizeKimiUpstreamModel(baseModel)
	switch normalized {
	case "k3", "k3-256k":
		return "k3"
	default:
		return normalized
	}
}

func kimiThinkingReplayScopeFromRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) kimiThinkingReplayScope {
	sessionKey := codexReasoningReplaySessionKey(ctx, sdktranslator.FormatClaude, req, opts, req.Payload)
	sessionKey = xaiReasoningReplayIsolateSessionKey(ctx, sessionKey)
	return kimiThinkingReplayScope{
		modelFamily: kimiThinkingReplayModelFamily(req.Model),
		sessionKey:  sessionKey,
	}
}

func prepareKimiThinkingReplayRequest(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Request, kimiThinkingReplayScope) {
	scope := kimiThinkingReplayScopeFromRequest(ctx, req, opts)
	if !scope.valid() {
		return req, scope
	}
	content, snapshot, found, errGet := internalcache.GetKimiThinkingReplayWithSnapshotRequired(ctx, scope.modelFamily, scope.sessionKey)
	scope.snapshot = snapshot
	scope.cacheReady = errGet == nil
	if errGet != nil {
		log.Warnf("kimi thinking replay cache read failed: %v", errGet)
		return req, scope
	}
	if !found {
		return req, scope
	}
	updated, restored := restoreKimiThinkingReplayContent(req.Payload, content)
	if restored {
		req.Payload = updated
		scope.replayApplied = true
	}
	return req, scope
}

func cacheKimiThinkingReplayResponse(ctx context.Context, scope kimiThinkingReplayScope, response []byte) {
	if !scope.valid() || !scope.cacheReady {
		return
	}
	content := gjson.GetBytes(response, "content")
	if !content.IsArray() {
		return
	}
	cacheKimiThinkingReplayContent(ctx, scope, []byte(content.Raw))
}

func cacheKimiThinkingReplayContent(ctx context.Context, scope kimiThinkingReplayScope, content []byte) {
	if !scope.valid() || !scope.cacheReady {
		return
	}
	if kimiThinkingReplayContentIsReplayable(content) {
		if _, errReplace := internalcache.ReplaceKimiThinkingReplayIfUnchanged(ctx, scope.modelFamily, scope.sessionKey, scope.snapshot, content); errReplace != nil {
			log.Warnf("kimi thinking replay cache replace failed: %v", errReplace)
		}
		return
	}
	clearKimiThinkingReplayContent(ctx, scope)
}

func shouldClearKimiThinkingReplayAfterError(err error) bool {
	if err == nil {
		return false
	}
	var upstreamStatus statusErr
	if !errors.As(err, &upstreamStatus) {
		return false
	}
	statusCode := upstreamStatus.StatusCode()
	return statusCode == 400 || statusCode == 422
}

func clearKimiThinkingReplayContent(ctx context.Context, scope kimiThinkingReplayScope) {
	if !scope.valid() || !scope.cacheReady {
		return
	}
	if _, errDelete := internalcache.DeleteKimiThinkingReplayIfUnchanged(ctx, scope.modelFamily, scope.sessionKey, scope.snapshot); errDelete != nil {
		log.Warnf("kimi thinking replay cache delete failed: %v", errDelete)
	}
}

func kimiThinkingReplayContentIsReplayable(content []byte) bool {
	root := gjson.ParseBytes(content)
	if !root.IsArray() {
		return false
	}
	hasSignedThinking := false
	hasToolUse := false
	for _, part := range root.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "thinking":
			if strings.TrimSpace(part.Get("signature").String()) != "" {
				hasSignedThinking = true
			}
		case "tool_use":
			if strings.TrimSpace(part.Get("id").String()) != "" {
				hasToolUse = true
			}
		}
	}
	return hasSignedThinking && hasToolUse
}

func restoreKimiThinkingReplayContent(body, cachedContent []byte) ([]byte, bool) {
	cachedParts, cachedOK := kimiNonThinkingContentParts(gjson.ParseBytes(cachedContent))
	if !cachedOK {
		return body, false
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, false
	}
	messageItems := messages.Array()
	for index := len(messageItems) - 1; index >= 0; index-- {
		message := messageItems[index]
		if !strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "assistant") {
			continue
		}
		currentContent := message.Get("content")
		if kimiJSONEqual([]byte(currentContent.Raw), cachedContent) {
			return body, false
		}
		if kimiContentHasThinking(currentContent) {
			continue
		}
		currentParts, currentOK := kimiNonThinkingContentParts(currentContent)
		if !currentOK || !kimiCanonicalPartsEqual(currentParts, cachedParts) {
			continue
		}
		updated, errSet := sjson.SetRawBytes(body, fmt.Sprintf("messages.%d.content", index), cachedContent)
		if errSet != nil {
			return body, false
		}
		return updated, true
	}
	return body, false
}

func kimiContentHasThinking(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "thinking", "redacted_thinking":
			return true
		}
	}
	return false
}

func kimiNonThinkingContentParts(content gjson.Result) ([][]byte, bool) {
	if !content.IsArray() {
		return nil, false
	}
	parts := make([][]byte, 0, len(content.Array()))
	hasToolUse := false
	for _, part := range content.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "thinking", "redacted_thinking":
			continue
		case "tool_use":
			if strings.TrimSpace(part.Get("id").String()) == "" {
				return nil, false
			}
			hasToolUse = true
		}
		canonical, ok := kimiCanonicalJSON([]byte(part.Raw))
		if !ok {
			return nil, false
		}
		parts = append(parts, canonical)
	}
	return parts, hasToolUse
}

func kimiCanonicalPartsEqual(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !bytes.Equal(left[i], right[i]) {
			return false
		}
	}
	return true
}

func kimiJSONEqual(left, right []byte) bool {
	canonicalLeft, leftOK := kimiCanonicalJSON(left)
	canonicalRight, rightOK := kimiCanonicalJSON(right)
	return leftOK && rightOK && bytes.Equal(canonicalLeft, canonicalRight)
}

func kimiCanonicalJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, false
	}
	canonical, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, false
	}
	return canonical, true
}

type kimiThinkingReplayStreamBlock struct {
	raw                  []byte
	text                 strings.Builder
	thinking             strings.Builder
	signature            strings.Builder
	input                strings.Builder
	textInitialized      bool
	thinkingInitialized  bool
	signatureInitialized bool
	hasInputDelta        bool
	finished             bool
}

type kimiThinkingReplayStreamAccumulator struct {
	blocks        map[int]*kimiThinkingReplayStreamBlock
	observed      bool
	complete      bool
	upstreamError bool
	abandoned     bool
	bytesUsed     int
}

func newKimiThinkingReplayStreamAccumulator() *kimiThinkingReplayStreamAccumulator {
	return &kimiThinkingReplayStreamAccumulator{blocks: make(map[int]*kimiThinkingReplayStreamBlock)}
}

func (a *kimiThinkingReplayStreamAccumulator) observe(chunk []byte) {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if !gjson.ValidBytes(payload) {
			a.abandon()
			continue
		}
		root := gjson.ParseBytes(payload)
		switch root.Get("type").String() {
		case "message_start":
			a.observed = true
		case "content_block_start":
			if !a.abandoned {
				a.observeBlockStart(root)
			}
		case "content_block_delta":
			if !a.abandoned {
				a.observeBlockDelta(root)
			}
		case "content_block_stop":
			if !a.abandoned {
				a.finishBlock(int(root.Get("index").Int()))
			}
		case "message_stop":
			a.complete = true
		case "error":
			a.upstreamError = true
			a.abandon()
		}
	}
}

func (a *kimiThinkingReplayStreamAccumulator) observeBlockStart(root gjson.Result) {
	index := int(root.Get("index").Int())
	block := root.Get("content_block")
	if !block.IsObject() || len(a.blocks) >= internalcache.KimiThinkingReplayCacheMaxBlocksPerEntry {
		a.abandon()
		return
	}
	if _, exists := a.blocks[index]; exists {
		a.abandon()
		return
	}
	raw := []byte(block.Raw)
	if !a.reserveBytes(len(raw)) {
		return
	}
	a.blocks[index] = &kimiThinkingReplayStreamBlock{raw: append([]byte(nil), raw...)}
}

func (a *kimiThinkingReplayStreamAccumulator) observeBlockDelta(root gjson.Result) {
	index := int(root.Get("index").Int())
	block, ok := a.blocks[index]
	if !ok {
		a.abandon()
		return
	}
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "text_delta":
		a.appendBlockText(block, &block.text, &block.textInitialized, "text", delta.Get("text").String())
	case "thinking_delta":
		a.appendBlockText(block, &block.thinking, &block.thinkingInitialized, "thinking", delta.Get("thinking").String())
	case "signature_delta":
		a.appendBlockText(block, &block.signature, &block.signatureInitialized, "signature", delta.Get("signature").String())
	case "input_json_delta":
		suffix := delta.Get("partial_json").String()
		if a.reserveBytes(len(suffix)) {
			block.input.WriteString(suffix)
			block.hasInputDelta = true
		}
	default:
		a.abandon()
	}
}

func (a *kimiThinkingReplayStreamAccumulator) appendBlockText(block *kimiThinkingReplayStreamBlock, builder *strings.Builder, initialized *bool, path, suffix string) {
	if !*initialized {
		initial := gjson.GetBytes(block.raw, path).String()
		if !a.reserveBytes(len(initial)) {
			return
		}
		builder.WriteString(initial)
		*initialized = true
	}
	if a.reserveBytes(len(suffix)) {
		builder.WriteString(suffix)
	}
}

func (a *kimiThinkingReplayStreamAccumulator) finishBlock(index int) {
	block, ok := a.blocks[index]
	if !ok {
		a.abandon()
		return
	}
	if block.hasInputDelta && !gjson.Valid(block.input.String()) {
		a.abandon()
		return
	}
	block.finished = true
}

func (a *kimiThinkingReplayStreamAccumulator) reserveBytes(count int) bool {
	if count < 0 || a.bytesUsed > internalcache.KimiThinkingReplayCacheMaxBytesPerEntry-count {
		a.abandon()
		return false
	}
	a.bytesUsed += count
	return true
}

func (a *kimiThinkingReplayStreamAccumulator) abandon() {
	a.abandoned = true
	a.blocks = nil
	a.bytesUsed = 0
}

func (a *kimiThinkingReplayStreamAccumulator) content() ([]byte, bool) {
	if !a.observed || !a.complete || a.upstreamError || a.abandoned {
		return nil, false
	}
	indexes := make([]int, 0, len(a.blocks))
	for index := range a.blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	parts := make([][]byte, 0, len(indexes))
	for _, index := range indexes {
		block := a.blocks[index]
		if !block.finished {
			a.abandon()
			return nil, false
		}
		raw := append([]byte(nil), block.raw...)
		var errSet error
		if block.textInitialized {
			raw, errSet = sjson.SetBytes(raw, "text", block.text.String())
		}
		if errSet == nil && block.thinkingInitialized {
			raw, errSet = sjson.SetBytes(raw, "thinking", block.thinking.String())
		}
		if errSet == nil && block.signatureInitialized {
			raw, errSet = sjson.SetBytes(raw, "signature", block.signature.String())
		}
		if errSet == nil && block.hasInputDelta {
			raw, errSet = sjson.SetRawBytes(raw, "input", []byte(block.input.String()))
		}
		if errSet != nil {
			a.abandon()
			return nil, false
		}
		parts = append(parts, raw)
	}
	content := helps.JoinRawJSONArray(parts)
	if len(content) > internalcache.KimiThinkingReplayCacheMaxBytesPerEntry {
		a.abandon()
		return nil, false
	}
	return content, true
}

func wrapKimiThinkingReplayStream(ctx context.Context, result *cliproxyexecutor.StreamResult, scope kimiThinkingReplayScope) *cliproxyexecutor.StreamResult {
	if result == nil || !scope.valid() {
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		accumulator := newKimiThinkingReplayStreamAccumulator()
		hasError := false
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				hasError = true
			} else {
				accumulator.observe(chunk.Payload)
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
		if hasError {
			return
		}
		if content, completed := accumulator.content(); completed {
			cacheKimiThinkingReplayContent(ctx, scope, content)
			return
		}
		if accumulator.upstreamError && scope.replayApplied {
			clearKimiThinkingReplayContent(ctx, scope)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers.Clone(), Chunks: out}
}
