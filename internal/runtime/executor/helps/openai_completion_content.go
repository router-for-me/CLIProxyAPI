package helps

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeOpenAICompletionContent converts typed text and thinking blocks to
// Chat Completions fields before response translation. Unrecognized blocks are
// left intact so normalization cannot discard other content types.
func NormalizeOpenAICompletionContent(payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return payload
	}
	result := payload
	for i, choice := range gjson.GetBytes(payload, "choices").Array() {
		for _, field := range []string{"message", "delta"} {
			message := choice.Get(field)
			content := message.Get("content")
			if !content.IsArray() || len(content.Array()) == 0 {
				continue
			}
			text, thinking, ok := openAICompletionBlockText(content)
			if !ok {
				continue
			}
			reasoning := message.Get("reasoning_content")
			if !reasoning.Exists() || reasoning.Type == gjson.Null {
				reasoning = message.Get("reasoning")
			}
			if thinking != "" && reasoning.Exists() && reasoning.Type != gjson.String && reasoning.Type != gjson.Null {
				continue
			}
			path := "choices." + strconv.Itoa(i) + "." + field
			updated, err := sjson.SetBytes(result, path+".content", text)
			if err != nil {
				return payload
			}
			if thinking != "" {
				updated, err = sjson.SetBytes(updated, path+".reasoning_content", reasoning.String()+thinking)
				if err != nil {
					return payload
				}
			}
			result = updated
		}
	}
	return result
}

func openAICompletionBlockText(content gjson.Result) (string, string, bool) {
	var text, thinking strings.Builder
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "text":
			value := block.Get("text")
			if value.Type != gjson.String {
				return "", "", false
			}
			text.WriteString(value.String())
		case "thinking":
			parts := block.Get("thinking")
			if !parts.IsArray() {
				return "", "", false
			}
			for _, part := range parts.Array() {
				value := part.Get("text")
				if part.Get("type").String() != "text" || value.Type != gjson.String {
					return "", "", false
				}
				thinking.WriteString(value.String())
			}
		default:
			return "", "", false
		}
	}
	return text.String(), thinking.String(), true
}
