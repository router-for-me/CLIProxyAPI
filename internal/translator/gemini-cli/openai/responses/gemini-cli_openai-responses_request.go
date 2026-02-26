package responses

import (
	. "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/translator/gemini-cli/gemini"
	. "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/translator/gemini/openai/responses"
)

func ConvertOpenAIResponsesRequestToGeminiCLI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	rawJSON = ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
	return ConvertGeminiRequestToGeminiCLI(modelName, rawJSON, stream)
}
