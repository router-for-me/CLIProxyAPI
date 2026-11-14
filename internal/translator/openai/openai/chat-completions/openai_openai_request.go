// Package openai provides request translation functionality for OpenAI to OpenAI API compatibility.
// It converts OpenAI Chat Completions requests into OpenAI-compatible JSON using gjson/sjson only.
package chat_completions

import (
	"github.com/MoeForge/moego/ojson"
)

// ConvertOpenAIRequestToOpenAI converts an OpenAI Chat Completions request (raw JSON)
// into a complete OpenAI request JSON. All JSON construction uses sjson and lookups use ojson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in OpenAI API format
func ConvertOpenAIRequestToOpenAI(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
	currentModel := ojson.GetBytesNoCopy(inputRawJSON, "model")
	if currentModel.Type == ojson.String && currentModel.String() == modelName {
		return inputRawJSON, nil
	}

	// Update the "model" field in the JSON payload with the provided modelName
	// The ojson.SetBytes function returns a new byte slice with the updated JSON.
	updatedJSON, err := ojson.SetBytes(inputRawJSON, "model", modelName)
	if err != nil {
		// If there's an error, return the original JSON or handle the error appropriately.
		// For now, we'll return the original, but in a real scenario, logging or a more robust error
		// handling mechanism would be needed.
		return inputRawJSON, nil
	}
	return updatedJSON, nil
}
