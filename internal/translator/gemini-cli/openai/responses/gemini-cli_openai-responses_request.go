package responses

import (
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini-cli/gemini"
	. "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
)

func ConvertOpenAIResponsesRequestToGeminiCLI(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	rawJSON, err := ConvertOpenAIResponsesRequestToGemini(modelName, inputRawJSON, stream)
	if err != nil {
		return nil, err
	}
	return ConvertGeminiRequestToGeminiCLI(modelName, rawJSON, stream)
}
