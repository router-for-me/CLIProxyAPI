package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StripVertexOpenAIResponsesToolCallIDs removes OpenAI Responses call IDs that
// Vertex rejects in Gemini functionCall/functionResponse payloads.
func StripVertexOpenAIResponsesToolCallIDs(payload []byte, sourceFormat string) []byte {
	if !strings.EqualFold(strings.TrimSpace(sourceFormat), "openai-response") {
		return payload
	}

	contents := util.GetGJSONBytesNoCopy(payload, "contents")
	if !contents.IsArray() || !vertexContentsHaveToolCallIDs(contents) {
		return payload
	}

	contentsChanged := false
	contentItems := make([][]byte, 0, int(contents.Get("#").Int()))
	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			contentItems = append(contentItems, []byte(content.Raw))
			return true
		}

		partsChanged := false
		partItems := make([][]byte, 0, int(parts.Get("#").Int()))
		parts.ForEach(func(_, part gjson.Result) bool {
			partJSON := []byte(part.Raw)
			for _, path := range []string{"functionCall.id", "functionResponse.id"} {
				if !part.Get(path).Exists() {
					continue
				}
				updated, errDelete := sjson.DeleteBytes(partJSON, path)
				if errDelete == nil {
					partJSON = updated
					partsChanged = true
				}
			}
			partItems = append(partItems, partJSON)
			return true
		})

		contentJSON := []byte(content.Raw)
		if partsChanged {
			updated, errSet := sjson.SetRawBytes(contentJSON, "parts", JoinRawJSONArray(partItems))
			if errSet == nil {
				contentJSON = updated
				contentsChanged = true
			}
		}
		contentItems = append(contentItems, contentJSON)
		return true
	})
	if !contentsChanged {
		return payload
	}

	updated, errSet := sjson.SetRawBytes(payload, "contents", JoinRawJSONArray(contentItems))
	if errSet != nil {
		return payload
	}
	return updated
}

func vertexContentsHaveToolCallIDs(contents gjson.Result) bool {
	hasIDs := false
	contents.ForEach(func(_, content gjson.Result) bool {
		parts := content.Get("parts")
		if !parts.IsArray() {
			return true
		}
		parts.ForEach(func(_, part gjson.Result) bool {
			hasIDs = part.Get("functionCall.id").Exists() || part.Get("functionResponse.id").Exists()
			return !hasIDs
		})
		return !hasIDs
	})
	return hasIDs
}
