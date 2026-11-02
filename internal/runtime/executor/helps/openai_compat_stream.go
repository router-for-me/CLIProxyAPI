package helps

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type OpenAICompatStreamState struct {
	done             bool
	thinkingByChoice map[int]*thinkingTagState
	lastID           string
	lastModel        string
	created          int64
	sawReasoning     bool
	sawVisible       bool
	sawFinish        bool
}

type thinkingTagState struct {
	inThinking bool
	pending    string
}

var thinkingOpenTags = []string{"<thinking>", "<think>"}
var thinkingCloseTags = []string{"</thinking>", "</think>"}

// NewOpenAICompatStreamState creates state for one upstream SSE stream.
func NewOpenAICompatStreamState() *OpenAICompatStreamState {
	return &OpenAICompatStreamState{thinkingByChoice: make(map[int]*thinkingTagState)}
}

func NormalizeOpenAICompatStreamPayload(rawJSON []byte, state *OpenAICompatStreamState) []byte {
	if state == nil || !gjson.ValidBytes(rawJSON) {
		return rawJSON
	}
	if state.thinkingByChoice == nil {
		state.thinkingByChoice = make(map[int]*thinkingTagState)
	}
	root := gjson.ParseBytes(rawJSON)
	if id := root.Get("id").String(); id != "" {
		state.lastID = id
	}
	if model := root.Get("model").String(); model != "" {
		state.lastModel = model
	}
	if created := root.Get("created"); created.Exists() {
		state.created = created.Int()
	}
	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() {
		return rawJSON
	}
	out := rawJSON
	choices.ForEach(func(_, choice gjson.Result) bool {
		if finishReason := choice.Get("finish_reason"); finishReason.String() != "" {
			state.sawFinish = true
		}
		index := int(choice.Get("index").Int())
		delta := choice.Get("delta")
		for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
			if delta.Get(field).String() != "" {
				state.sawReasoning = true
				break
			}
		}
		content := delta.Get("content")
		if !content.Exists() || content.Type != gjson.String {
			return true
		}
		tagState := state.thinkingByChoice[index]
		if tagState == nil {
			tagState = &thinkingTagState{}
			state.thinkingByChoice[index] = tagState
		}
		visible, reasoning, changed := consumeThinkingTags(content.String(), tagState)
		if reasoning != "" {
			state.sawReasoning = true
		}
		if visible != "" {
			state.sawVisible = true
		}
		if !changed {
			if content.String() != "" {
				state.sawVisible = true
			}
			return true
		}
		contentPath := fmt.Sprintf("choices.%d.delta.content", index)
		out, _ = sjson.SetBytes(out, contentPath, visible)
		if reasoning != "" {
			reasoningPath := fmt.Sprintf("choices.%d.delta.reasoning_content", index)
			existing := gjson.GetBytes(out, reasoningPath).String()
			if existing != "" {
				reasoning = existing + reasoning
			}
			out, _ = sjson.SetBytes(out, reasoningPath, reasoning)
		}
		return true
	})
	return out
}

func SyntheticOpenAICompatReasoningFinishChunk(state *OpenAICompatStreamState) []byte {
	if state == nil || !state.sawReasoning || state.sawVisible || state.sawFinish {
		return nil
	}
	state.sawFinish = true
	chunk := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	chunk, _ = sjson.SetBytes(chunk, "id", state.lastID)
	chunk, _ = sjson.SetBytes(chunk, "created", state.created)
	chunk, _ = sjson.SetBytes(chunk, "model", state.lastModel)
	return chunk
}

func NormalizeOpenAICompatNonStreamPayload(rawJSON []byte, state *OpenAICompatStreamState) []byte {
	if state == nil || !gjson.ValidBytes(rawJSON) {
		return rawJSON
	}
	choices := gjson.GetBytes(rawJSON, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return rawJSON
	}
	out := rawJSON
	choices.ForEach(func(_, choice gjson.Result) bool {
		index := int(choice.Get("index").Int())
		content := choice.Get("message.content")
		if !content.Exists() || content.Type != gjson.String {
			return true
		}
		tagState := &thinkingTagState{}
		visible, reasoning, changed := consumeThinkingTags(content.String(), tagState)
		if !changed {
			return true
		}
		out, _ = sjson.SetBytes(out, fmt.Sprintf("choices.%d.message.content", index), visible)
		if reasoning != "" {
			path := fmt.Sprintf("choices.%d.message.reasoning_content", index)
			existing := gjson.GetBytes(out, path).String()
			if existing != "" {
				reasoning = existing + reasoning
			}
			out, _ = sjson.SetBytes(out, path, reasoning)
		}
		return true
	})
	return out
}

func consumeThinkingTags(input string, state *thinkingTagState) (visible, reasoning string, changed bool) {
	if state == nil {
		return input, "", false
	}
	text := state.pending + input
	state.pending = ""
	var visibleBuilder, reasoningBuilder strings.Builder
	for len(text) > 0 {
		if state.inThinking {
			if tag, index := findTag(text, thinkingCloseTags); index >= 0 {
				reasoningBuilder.WriteString(text[:index])
				text = text[index+len(tag):]
				state.inThinking = false
				changed = true
				continue
			}
			if prefix := trailingTagPrefix(text, thinkingCloseTags); prefix != "" {
				reasoningBuilder.WriteString(text[:len(text)-len(prefix)])
				state.pending = prefix
				changed = true
				break
			}
			reasoningBuilder.WriteString(text)
			changed = true
			break
		}

		if tag, index := findTag(text, thinkingOpenTags); index >= 0 {
			visibleBuilder.WriteString(text[:index])
			text = text[index+len(tag):]
			state.inThinking = true
			changed = true
			continue
		}
		if prefix := trailingTagPrefix(text, thinkingOpenTags); prefix != "" {
			visibleBuilder.WriteString(text[:len(text)-len(prefix)])
			state.pending = prefix
			changed = true
			break
		}
		visibleBuilder.WriteString(text)
		break
	}
	return visibleBuilder.String(), reasoningBuilder.String(), changed
}

func findTag(text string, tags []string) (string, int) {
	bestTag, bestIndex := "", -1
	for _, tag := range tags {
		if index := strings.Index(text, tag); index >= 0 && (bestIndex < 0 || index < bestIndex) {
			bestTag, bestIndex = tag, index
		}
	}
	return bestTag, bestIndex
}

func trailingTagPrefix(text string, tags []string) string {
	for length := len(text); length > 0; length-- {
		suffix := text[len(text)-length:]
		for _, tag := range tags {
			if strings.HasPrefix(tag, suffix) && suffix != tag {
				return suffix
			}
		}
	}
	return ""
}
