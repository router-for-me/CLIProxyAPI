package openai

// ConvertOpenAIRequestToCodeArts passes through the OpenAI-format request payload.
// Actual conversion to CodeArts format happens in the executor (buildCodeArtsPayload),
// following the same pattern as Kiro's translator.
func ConvertOpenAIRequestToCodeArts(model string, rawJSON []byte, stream bool) []byte {
	return rawJSON
}
