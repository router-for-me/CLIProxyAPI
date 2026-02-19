package responses

import (
	antigravitygemini "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/antigravity/gemini"
	geminiopenai "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/responses"
)

func ConvertOpenAIResponsesRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	rawJSON = geminiopenai.ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
	return antigravitygemini.ConvertGeminiRequestToAntigravity(modelName, rawJSON, stream)
}
