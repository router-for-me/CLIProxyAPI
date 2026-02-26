package responses

import (
	geminicligemini "github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/translator/gemini-cli/gemini"
	geminiopenai "github.com/kooshapari/cliproxyapi-plusplus/v6/pkg/llmproxy/translator/gemini/openai/responses"
)

func ConvertOpenAIResponsesRequestToGeminiCLI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	rawJSON = geminiopenai.ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
	return geminicligemini.ConvertGeminiRequestToGeminiCLI(modelName, rawJSON, stream)
}
