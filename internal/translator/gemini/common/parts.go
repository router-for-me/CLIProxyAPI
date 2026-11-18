package common

import "github.com/MoeForge/moego/ojson"

// HasUnsupportedPartPayload keeps omitted payloads from being mistaken for
// detached signatures belonging to a neighboring supported part.
func HasUnsupportedPartPayload(part ojson.Result, supportedFields ...string) bool {
	unsupported := false
	part.ForEach(func(key, _ ojson.Result) bool {
		name := key.String()
		switch name {
		case "text", "functionCall", "function_call", "functionResponse", "function_response",
			"inlineData", "inline_data", "fileData", "file_data",
			"executableCode", "executable_code", "codeExecutionResult", "code_execution_result",
			"toolCall", "tool_call", "toolResponse", "tool_response",
			"audioTranscription", "audio_transcription":
			// Only semantic payload fields can create an unsupported boundary.
		default:
			// Metadata such as partMetadata must not hide supported content.
			return true
		}
		for _, field := range supportedFields {
			if name == field {
				return true
			}
		}
		unsupported = true
		return false
	})
	return unsupported
}
