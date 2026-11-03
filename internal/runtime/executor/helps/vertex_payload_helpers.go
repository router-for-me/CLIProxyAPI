package helps

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// StripVertexOpenAIResponsesToolCallIDs removes OpenAI Responses call IDs that
// Vertex rejects in Gemini functionCall/functionResponse payloads.
func StripVertexOpenAIResponsesToolCallIDs(payload []byte, sourceFormat string) []byte {
	if len(payload) == 0 || !strings.EqualFold(strings.TrimSpace(sourceFormat), "openai-response") {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !StripVertexOpenAIResponsesToolCallIDsDoc(doc, sourceFormat) {
		return payload
	}
	return doc.Bytes()
}

// StripVertexOpenAIResponsesToolCallIDsDoc removes OpenAI Responses call IDs that
// Vertex rejects in Gemini functionCall/functionResponse payloads in place on doc.
func StripVertexOpenAIResponsesToolCallIDsDoc(doc *ojson.Doc, sourceFormat string) bool {
	if doc == nil || doc.Len() == 0 || !strings.EqualFold(strings.TrimSpace(sourceFormat), "openai-response") {
		return false
	}

	contents := doc.Get("contents")
	if !contents.IsArray() {
		return false
	}

	changed := false
	for cIdx, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for pIdx, part := range parts.Array() {
			for _, field := range []string{"functionCall.id", "functionResponse.id"} {
				if !part.Get(field).Exists() {
					continue
				}
				if errDel := doc.Delete(fmt.Sprintf("contents.%d.parts.%d.%s", cIdx, pIdx, field)); errDel == nil {
					changed = true
				}
			}
		}
	}
	return changed
}
