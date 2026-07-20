package helps

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

const (
	claudeUsageEmitStep        = int64(64)
	claudeThinkingTokenQuantum = int64(64)
	claudeUsageTailTarget      = 256
	claudeUsageTailLimit       = 512
	// Local Codex traces produced roughly 31-55 reasoning tokens per second once
	// generation started. A warmup plus 24 tokens per second keeps the in-flight
	// estimate conservative while still advancing during an otherwise silent item.
	claudeReasoningEstimateWarmup  = 5 * time.Second
	claudeReasoningTokensPerSecond = int64(24)
)

// ClaudeUsageSnapshot is a cumulative Claude Messages usage update.
type ClaudeUsageSnapshot struct {
	InputTokens    int64
	OutputTokens   int64
	ThinkingTokens int64
}

// ClaudeThinkingTokenCountEmitter produces the per-block cumulative
// estimated_tokens values used by Anthropic's thinking-token-count streaming beta.
type ClaudeThinkingTokenCountEmitter struct {
	enabled               bool
	thinkingBlockOpen     bool
	thinkingBlockIndex    int64
	thinkingBlockBase     int64
	emittedThinkingTokens int64
}

type claudeRollingTokenEstimator struct {
	encoder   tokenizer.Codec
	tail      string
	finalized int64
	estimated int64
}

type ClaudeStreamUsageEstimator struct {
	model                   string
	visible                 claudeRollingTokenEstimator
	reasoningSummary        claudeRollingTokenEstimator
	reasoningCipherByItem   map[string]int64
	inputTokens             int64
	estimatedOutputTokens   int64
	estimatedThinkingTokens int64
	lastEmitted             ClaudeUsageSnapshot
	started                 bool
	completed               bool
	startedAt               time.Time
	reasoningEndedAt        time.Time
}

func NewClaudeStreamUsageEstimator(model string, inputTokens ...int64) (*ClaudeStreamUsageEstimator, error) {
	encoder, err := TokenizerForModel(model)
	if err != nil {
		return nil, fmt.Errorf("create Claude stream usage tokenizer: %w", err)
	}
	estimatedInput := int64(0)
	if len(inputTokens) > 0 && inputTokens[0] > 0 {
		estimatedInput = inputTokens[0]
	}
	return &ClaudeStreamUsageEstimator{
		model:                 model,
		visible:               claudeRollingTokenEstimator{encoder: encoder},
		reasoningSummary:      claudeRollingTokenEstimator{encoder: encoder},
		reasoningCipherByItem: make(map[string]int64),
		inputTokens:           estimatedInput,
	}, nil
}

func NewClaudeThinkingTokenCountEmitter(enabled bool) *ClaudeThinkingTokenCountEmitter {
	return &ClaudeThinkingTokenCountEmitter{enabled: enabled}
}

func (e *ClaudeThinkingTokenCountEmitter) ObserveTranslatedChunks(chunks [][]byte) {
	if e == nil || !e.enabled {
		return
	}
	for _, chunk := range chunks {
		for _, line := range strings.Split(string(chunk), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			switch event.Get("type").String() {
			case "content_block_start":
				if event.Get("content_block.type").String() == "thinking" {
					e.thinkingBlockOpen = true
					e.thinkingBlockIndex = event.Get("index").Int()
					e.thinkingBlockBase = e.emittedThinkingTokens
				}
			case "content_block_stop":
				if e.thinkingBlockOpen && event.Get("index").Int() == e.thinkingBlockIndex {
					e.thinkingBlockOpen = false
				}
			}
		}
	}
}

func (e *ClaudeThinkingTokenCountEmitter) Event(snapshot ClaudeUsageSnapshot) []byte {
	if e == nil || !e.enabled || !e.thinkingBlockOpen {
		return nil
	}
	available := snapshot.ThinkingTokens - e.emittedThinkingTokens
	increment := available / claudeThinkingTokenQuantum * claudeThinkingTokenQuantum
	if increment <= 0 {
		return nil
	}
	e.emittedThinkingTokens += increment
	blockEstimate := e.emittedThinkingTokens - e.thinkingBlockBase
	return []byte(fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":%d,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"\",\"estimated_tokens\":%d}}\n\n", e.thinkingBlockIndex, blockEstimate))
}

func ClaudeApplyMessageStartUsage(chunks [][]byte, snapshot ClaudeUsageSnapshot) bool {
	if snapshot.InputTokens <= 0 {
		return false
	}
	patched := false
	for i := range chunks {
		chunk := chunks[i]
		searchFrom := 0
		for searchFrom < len(chunk) {
			markerOffset := bytes.Index(chunk[searchFrom:], []byte("data:"))
			if markerOffset < 0 {
				break
			}
			lineStart := searchFrom + markerOffset + len("data:")
			lineEnd := len(chunk)
			if newlineOffset := bytes.IndexByte(chunk[lineStart:], '\n'); newlineOffset >= 0 {
				lineEnd = lineStart + newlineOffset
			}
			dataStart := lineStart
			for dataStart < lineEnd && (chunk[dataStart] == ' ' || chunk[dataStart] == '\t') {
				dataStart++
			}
			dataEnd := lineEnd
			for dataEnd > dataStart && (chunk[dataEnd-1] == ' ' || chunk[dataEnd-1] == '\t' || chunk[dataEnd-1] == '\r') {
				dataEnd--
			}
			data := chunk[dataStart:dataEnd]
			if gjson.GetBytes(data, "type").String() != "message_start" {
				searchFrom = lineEnd + 1
				continue
			}
			updated, errSet := sjson.SetBytes(bytes.Clone(data), "message.usage.input_tokens", snapshot.InputTokens)
			if errSet != nil {
				break
			}
			patchedChunk := make([]byte, 0, len(chunk)+len(updated)-len(data))
			patchedChunk = append(patchedChunk, chunk[:dataStart]...)
			patchedChunk = append(patchedChunk, updated...)
			patchedChunk = append(patchedChunk, chunk[dataEnd:]...)
			chunks[i] = patchedChunk
			patched = true
			break
		}
	}
	return patched
}

func (e *ClaudeStreamUsageEstimator) ObserveCodexEvent(payload []byte) (ClaudeUsageSnapshot, bool) {
	return e.observeCodexEventAt(payload, time.Now())
}

func (e *ClaudeStreamUsageEstimator) observeCodexEventAt(payload []byte, now time.Time) (ClaudeUsageSnapshot, bool) {
	if e == nil || e.visible.encoder == nil || len(payload) == 0 {
		return ClaudeUsageSnapshot{}, false
	}
	event := gjson.ParseBytes(payload)
	eventType := event.Get("type").String()
	if eventType == "response.created" {
		e.started = true
		e.completed = false
		e.startedAt = now
		e.reasoningEndedAt = time.Time{}
		if model := strings.TrimSpace(event.Get("response.model").String()); model != "" {
			e.model = model
		}
		snapshot := e.snapshot()
		if snapshot.InputTokens > 0 {
			e.lastEmitted = snapshot
			return snapshot, true
		}
		return snapshot, false
	}
	if !e.started {
		return ClaudeUsageSnapshot{}, false
	}
	if eventType == "response.completed" || eventType == "response.incomplete" {
		e.completed = true
		return e.snapshot(), false
	}

	switch eventType {
	case "response.output_text.delta", "response.function_call_arguments.delta":
		e.markReasoningEnded(now)
		e.visible.append(event.Get("delta").String())
	case "response.reasoning_summary_text.delta":
		e.reasoningSummary.append(event.Get("delta").String())
	case "response.output_item.added", "response.output_item.done":
		e.observeReasoningCipher(event)
		if eventType == "response.output_item.done" && event.Get("item.type").String() == "reasoning" {
			e.markReasoningEnded(now)
		}
	}

	e.updateEstimate(now)
	snapshot := e.snapshot()
	force := false
	switch eventType {
	case "response.content_part.done", "response.reasoning_summary_part.done", "response.function_call_arguments.done", "response.output_item.done":
		force = true
	}
	if snapshot == e.lastEmitted {
		return snapshot, false
	}
	if !force && snapshot.OutputTokens-e.lastEmitted.OutputTokens < claudeUsageEmitStep {
		return snapshot, false
	}
	e.lastEmitted = snapshot
	return snapshot, true
}

// ObserveTime advances the conservative live reasoning estimate while Codex is
// generating a long reasoning item without emitting content deltas. Exact usage
// from response.completed remains authoritative.
func (e *ClaudeStreamUsageEstimator) ObserveTime(now time.Time) (ClaudeUsageSnapshot, bool) {
	if e == nil || !e.started || e.completed {
		return ClaudeUsageSnapshot{}, false
	}
	e.updateEstimate(now)
	snapshot := e.snapshot()
	if snapshot == e.lastEmitted || snapshot.OutputTokens-e.lastEmitted.OutputTokens < claudeUsageEmitStep {
		return snapshot, false
	}
	e.lastEmitted = snapshot
	return snapshot, true
}

func (e *ClaudeStreamUsageEstimator) observeReasoningCipher(event gjson.Result) {
	item := event.Get("item")
	if item.Get("type").String() != "reasoning" {
		return
	}
	encrypted := item.Get("encrypted_content").String()
	if encrypted == "" {
		return
	}
	decoded, errDecode := base64.URLEncoding.DecodeString(encrypted)
	if errDecode != nil {
		decoded, errDecode = base64.RawURLEncoding.DecodeString(strings.TrimRight(encrypted, "="))
	}
	if errDecode != nil || len(decoded) == 0 {
		return
	}
	itemID := item.Get("id").String()
	if itemID == "" {
		itemID = event.Get("output_index").String()
	}
	if itemID == "" {
		return
	}
	decodedLength := int64(len(decoded))
	if decodedLength > e.reasoningCipherByItem[itemID] {
		e.reasoningCipherByItem[itemID] = decodedLength
	}
}

func (e *ClaudeStreamUsageEstimator) updateEstimate(now time.Time) {
	cipherEstimate := estimateClaudeReasoningTokensFromCipher(e.model, e.reasoningCipherByItem)
	thinkingEstimate := e.reasoningSummary.estimated
	if cipherEstimate > thinkingEstimate {
		thinkingEstimate = cipherEstimate
	}
	if elapsedEstimate := e.estimateReasoningTokensFromElapsed(now); elapsedEstimate > thinkingEstimate {
		thinkingEstimate = elapsedEstimate
	}
	if thinkingEstimate > e.estimatedThinkingTokens {
		e.estimatedThinkingTokens = thinkingEstimate
	}
	outputEstimate := e.visible.estimated + e.estimatedThinkingTokens
	if outputEstimate > e.estimatedOutputTokens {
		e.estimatedOutputTokens = outputEstimate
	}
}

func (e *ClaudeStreamUsageEstimator) markReasoningEnded(now time.Time) {
	if e == nil || !e.reasoningEndedAt.IsZero() {
		return
	}
	e.reasoningEndedAt = now
}

func (e *ClaudeStreamUsageEstimator) estimateReasoningTokensFromElapsed(now time.Time) int64 {
	if e == nil || e.startedAt.IsZero() {
		return 0
	}
	end := now
	if !e.reasoningEndedAt.IsZero() && e.reasoningEndedAt.Before(end) {
		end = e.reasoningEndedAt
	}
	elapsed := end.Sub(e.startedAt) - claudeReasoningEstimateWarmup
	if elapsed <= 0 {
		return 0
	}
	return int64(elapsed) * claudeReasoningTokensPerSecond / int64(time.Second)
}

func (e *ClaudeStreamUsageEstimator) snapshot() ClaudeUsageSnapshot {
	return ClaudeUsageSnapshot{
		InputTokens:    e.inputTokens,
		OutputTokens:   e.estimatedOutputTokens,
		ThinkingTokens: e.estimatedThinkingTokens,
	}
}

func (e *claudeRollingTokenEstimator) append(delta string) {
	if e == nil || e.encoder == nil || delta == "" {
		return
	}
	combined := e.tail + delta
	if len(combined) > claudeUsageTailLimit {
		cut := len(combined) - claudeUsageTailTarget
		for cut < len(combined) && !utf8.RuneStart(combined[cut]) {
			cut++
		}
		newTail := combined[cut:]
		combinedTokens, errCombined := e.encoder.Count(combined)
		tailTokens, errTail := e.encoder.Count(newTail)
		if errCombined == nil && errTail == nil {
			settled := int64(combinedTokens - tailTokens)
			if settled > 0 {
				e.finalized += settled
			}
			e.tail = newTail
		} else {
			e.tail = combined
		}
	} else {
		e.tail = combined
	}
	tailTokens, errTail := e.encoder.Count(e.tail)
	if errTail != nil {
		return
	}
	estimate := e.finalized + int64(tailTokens)
	if estimate > e.estimated {
		e.estimated = estimate
	}
}

func estimateClaudeReasoningTokensFromCipher(model string, cipherByItem map[string]int64) int64 {
	if len(cipherByItem) == 0 {
		return 0
	}
	// Codex exposes exact reasoning_tokens only in the terminal usage object. During
	// generation, the opaque encrypted_content length provides a conservative progress
	// signal at each reasoning-item boundary. The terminal Claude usage event remains
	// authoritative and replaces this estimate when the response completes.
	isSol := strings.Contains(strings.ToLower(strings.TrimSpace(model)), "sol")
	estimate := int64(0)
	for _, decodedLength := range cipherByItem {
		itemEstimate := int64(0)
		if decodedLength < 850 {
			itemEstimate = (decodedLength - 625) / 8
		} else {
			itemEstimate = decodedLength*5/12 - 243 - 48
		}
		if itemEstimate < 0 {
			itemEstimate = 0
		}
		if isSol {
			// Sol reasoning capsules are roughly twice the size per token observed for
			// Luna and Terra, so keep its live estimate on the conservative side.
			itemEstimate = itemEstimate * 9 / 20
		} else if decodedLength >= 850 {
			itemEstimate = itemEstimate * 9 / 10
		}
		estimate += itemEstimate
	}
	return estimate
}

func ClaudeCumulativeUsageEvent(snapshot ClaudeUsageSnapshot) []byte {
	if snapshot.InputTokens <= 0 && snapshot.OutputTokens <= 0 {
		return nil
	}
	return []byte(fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"context_management\":null,\"delta\":{\"container\":null,\"stop_details\":null,\"stop_reason\":null,\"stop_sequence\":null},\"usage\":{\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0,\"input_tokens\":null,\"iterations\":null,\"output_tokens\":%d,\"output_tokens_details\":{\"thinking_tokens\":%d},\"server_tool_use\":{\"web_fetch_requests\":0,\"web_search_requests\":0}}}\n\n", snapshot.OutputTokens, snapshot.ThinkingTokens))
}

func claudeSSEEventData(payload []byte) []byte {
	trimmed := strings.TrimSpace(string(payload))
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return payload
}
