package responses

import (
	"bytes"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini-cli/gemini"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/responses"
)

func ConvertOpenAIResponsesRequestToGeminiCLI(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
	rawJSON = ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
	return ConvertGeminiRequestToGeminiCLI(modelName, rawJSON, stream)
}
