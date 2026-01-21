package responses

import (
	"bytes"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/antigravity/gemini"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/responses"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
	hasWebSearchTool := false
	if tools := gjson.GetBytes(rawJSON, "tools"); tools.Exists() && tools.IsArray() {
		for _, tool := range tools.Array() {
			if tool.Get("type").String() == "web_search" {
				hasWebSearchTool = true
				break
			}
		}
	}
	rawJSON = ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
	if hasWebSearchTool {
		rawJSON, _ = sjson.SetBytes(rawJSON, "model", "gemini-2.5-flash")
		rawJSON, _ = sjson.SetBytes(rawJSON, "generationConfig.candidateCount", 1)
	}
	return ConvertGeminiRequestToAntigravity(modelName, rawJSON, stream)
}
