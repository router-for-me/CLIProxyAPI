// Package openai provides request translation for OpenAI Chat Completions
// passthrough compatibility.
package chat_completions

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/sjson"
)

// ConvertOpenAIRequestToOpenAI converts an OpenAI Chat Completions request (raw JSON)
// into an OpenAI-compatible request JSON.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in OpenAI-compatible API format
func ConvertOpenAIRequestToOpenAI(modelName string, inputRawJSON []byte, _ bool) []byte {
	inputRawJSON = util.NormalizeOpenAIChatRequestJSON(inputRawJSON)
	updatedJSON, err := sjson.SetBytes(inputRawJSON, "model", modelName)
	if err != nil {
		return inputRawJSON
	}
	return updatedJSON
}
