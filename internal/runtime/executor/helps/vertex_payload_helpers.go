package helps

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StripVertexOpenAIResponsesToolCallIDs removes OpenAI Responses call IDs that
// Vertex rejects in Gemini functionCall/functionResponse payloads.
func StripVertexOpenAIResponsesToolCallIDs(payload []byte, sourceFormat string) []byte {
	if !strings.EqualFold(strings.TrimSpace(sourceFormat), "openai-response") {
		return payload
	}

	contents := gjson.GetBytes(payload, "contents")
	if !contents.IsArray() {
		return payload
	}

	out := payload
	for contentIndex, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for partIndex, part := range parts.Array() {
			if part.Get("functionCall.id").Exists() {
				if updated, errDelete := sjson.DeleteBytes(out, fmt.Sprintf("contents.%d.parts.%d.functionCall.id", contentIndex, partIndex)); errDelete == nil {
					out = updated
				}
			}
			if part.Get("functionResponse.id").Exists() {
				if updated, errDelete := sjson.DeleteBytes(out, fmt.Sprintf("contents.%d.parts.%d.functionResponse.id", contentIndex, partIndex)); errDelete == nil {
					out = updated
				}
			}
		}
	}
	return out
}
