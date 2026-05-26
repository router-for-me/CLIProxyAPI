package executor

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

const chatCompletionsCeilingSafetyMargin = 0.90

func chatCompletionsInputTokenCeiling(modelID string) int {
	m := registry.LookupStaticModelInfo(modelID)
	if m == nil {
		return 0
	}
	if m.InputTokenLimit > 0 {
		return m.InputTokenLimit
	}
	if m.ContextLength > 0 && m.MaxCompletionTokens > 0 {
		return m.ContextLength - m.MaxCompletionTokens
	}
	return 0
}

// repairOrphanedToolMessages removes role:tool messages whose tool_call_id
// has no matching tool_calls[].id in any preceding assistant message.
// This fixes payloads where the caller (e.g. Cursor) truncated the assistant
// turn but kept the tool response.
func repairOrphanedToolMessages(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	items := messages.Array()
	if len(items) == 0 {
		return body
	}

	knownCallIDs := make(map[string]struct{})
	for _, msg := range items {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.IsArray() {
			continue
		}
		for _, tc := range toolCalls.Array() {
			if id := strings.TrimSpace(tc.Get("id").String()); id != "" {
				knownCallIDs[id] = struct{}{}
			}
		}
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	var orphaned int
	for _, msg := range items {
		if msg.Get("role").String() == "tool" {
			tcID := strings.TrimSpace(msg.Get("tool_call_id").String())
			if tcID != "" {
				if _, ok := knownCallIDs[tcID]; !ok {
					orphaned++
					continue
				}
			}
		}
		if !first {
			buf.WriteByte(',')
		}
		buf.WriteString(msg.Raw)
		first = false
	}
	buf.WriteByte(']')

	if orphaned == 0 {
		return body
	}

	repaired, err := sjson.SetRawBytes(body, "messages", buf.Bytes())
	if err != nil {
		log.Warnf("chat completions orphan repair: failed to rebuild messages: %v", err)
		return body
	}
	log.Infof("chat completions orphan repair: dropped %d orphaned tool messages", orphaned)
	return repaired
}

type chatTurn struct {
	startIdx int
	endIdx   int
	tokens   int64
	pinned   bool
}

// trimChatCompletionsIfNeeded drops oldest non-pinned message turns when the
// payload exceeds the model's input token ceiling (with a 10% safety margin).
// After trimming it re-runs orphan repair in case the trim created new orphans.
func trimChatCompletionsIfNeeded(cfg *config.Config, body []byte, baseModel string) []byte {
	if !autoTrimEnabled(cfg) {
		return body
	}

	body = repairOrphanedToolMessages(body)

	ceiling := chatCompletionsInputTokenCeiling(baseModel)
	if ceiling <= 0 {
		return body
	}
	safeCeiling := int64(float64(ceiling) * chatCompletionsCeilingSafetyMargin)
	if safeCeiling <= 0 {
		return body
	}
	if int64(len(body)) < safeCeiling {
		return body
	}

	enc, err := cachedTokenizerForCodexModel(baseModel)
	if err != nil {
		return body
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	items := messages.Array()
	if len(items) <= 1 {
		return body
	}

	turns := groupChatTurns(items, enc)
	if len(turns) <= 1 {
		return body
	}

	var totalTokens int64
	for _, t := range turns {
		totalTokens += t.tokens
	}
	if totalTokens <= safeCeiling {
		return body
	}

	target := totalTokens - safeCeiling
	var shed int64
	dropCount := 0
	for dropCount < len(turns) && shed < target {
		if turns[dropCount].pinned {
			dropCount++
			continue
		}
		shed += turns[dropCount].tokens
		dropCount++
	}
	if shed == 0 {
		return body
	}

	dropped := make(map[int]struct{})
	for i := 0; i < dropCount; i++ {
		if turns[i].pinned {
			continue
		}
		for j := turns[i].startIdx; j < turns[i].endIdx; j++ {
			dropped[j] = struct{}{}
		}
	}

	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	for i, msg := range items {
		if _, ok := dropped[i]; ok {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		buf.WriteString(msg.Raw)
		first = false
	}
	buf.WriteByte(']')

	trimmed, err := sjson.SetRawBytes(body, "messages", buf.Bytes())
	if err != nil {
		log.Warnf("chat completions trim: failed to rebuild messages: %v", err)
		return body
	}

	droppedTurns := 0
	for i := 0; i < dropCount; i++ {
		if !turns[i].pinned {
			droppedTurns++
		}
	}
	log.Infof("chat completions trimmed: dropped %d/%d turns (%d tokens), %d -> ~%d tokens (ceiling %d, safe %d)",
		droppedTurns, len(turns), shed, totalTokens, totalTokens-shed, ceiling, safeCeiling)

	trimmed = repairOrphanedToolMessages(trimmed)
	return trimmed
}

// groupChatTurns groups messages into logical turns. A turn is:
//   - a system message (pinned, never dropped)
//   - the first user message (pinned)
//   - the last user message (pinned - carries the current instruction)
//   - the last assistant+tool turn (pinned - carries the most recent reasoning)
//   - an assistant message + its subsequent tool messages
//   - a standalone user message
func groupChatTurns(items []gjson.Result, enc tokenizer.Codec) []chatTurn {
	var turns []chatTurn
	firstUserSeen := false
	i := 0
	for i < len(items) {
		role := items[i].Get("role").String()
		start := i

		switch role {
		case "system":
			i++
			turns = append(turns, chatTurn{
				startIdx: start,
				endIdx:   i,
				tokens:   countChatMessageTokens(enc, items[start]),
				pinned:   true,
			})
		case "user":
			i++
			pin := !firstUserSeen
			firstUserSeen = true
			turns = append(turns, chatTurn{
				startIdx: start,
				endIdx:   i,
				tokens:   countChatMessageTokens(enc, items[start]),
				pinned:   pin,
			})
		case "assistant":
			i++
			for i < len(items) && items[i].Get("role").String() == "tool" {
				i++
			}
			var tokens int64
			for j := start; j < i; j++ {
				tokens += countChatMessageTokens(enc, items[j])
			}
			turns = append(turns, chatTurn{
				startIdx: start,
				endIdx:   i,
				tokens:   tokens,
				pinned:   false,
			})
		default:
			i++
			turns = append(turns, chatTurn{
				startIdx: start,
				endIdx:   i,
				tokens:   countChatMessageTokens(enc, items[start]),
				pinned:   false,
			})
		}
	}

	// pin the last user turn and the last assistant turn to preserve
	// the current instruction and the most recent model reasoning
	lastUser := -1
	lastAssistant := -1
	for idx := len(turns) - 1; idx >= 0; idx-- {
		if lastUser >= 0 && lastAssistant >= 0 {
			break
		}
		role := items[turns[idx].startIdx].Get("role").String()
		if lastUser < 0 && role == "user" {
			lastUser = idx
		}
		if lastAssistant < 0 && role == "assistant" {
			lastAssistant = idx
		}
	}
	if lastUser >= 0 {
		turns[lastUser].pinned = true
	}
	if lastAssistant >= 0 {
		turns[lastAssistant].pinned = true
	}

	return turns
}

func countChatMessageTokens(enc tokenizer.Codec, msg gjson.Result) int64 {
	var segments []string

	if content := msg.Get("content"); content.Exists() {
		switch {
		case content.IsArray():
			for _, part := range content.Array() {
				if text := strings.TrimSpace(part.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		case content.Type == gjson.String:
			if text := strings.TrimSpace(content.String()); text != "" {
				segments = append(segments, text)
			}
		}
	}

	if toolCalls := msg.Get("tool_calls"); toolCalls.IsArray() {
		for _, tc := range toolCalls.Array() {
			if name := strings.TrimSpace(tc.Get("function.name").String()); name != "" {
				segments = append(segments, name)
			}
			if args := strings.TrimSpace(tc.Get("function.arguments").String()); args != "" {
				segments = append(segments, args)
			}
		}
	}

	if len(segments) == 0 {
		return 0
	}
	text := strings.Join(segments, "\n")
	count, err := enc.Count(text)
	if err != nil {
		return int64(len(text) / 4)
	}
	return int64(count)
}
