package chat_completions

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func init() {
	translator.Register("openai", "openai-response", convertChatRequestToResponsesAPI,
		interfaces.TranslateResponse{Stream: ConvertCodexResponseToOpenAI, NonStream: convertResponsesAPIToChat})
}

// Reuse the existing Responses tool/history translation without the Codex-only
// omission of API sampling parameters and output limits.
func convertChatRequestToResponsesAPI(model string, raw []byte, stream bool) ([]byte, error) {
	body, err := ConvertOpenAIRequestToCodex(model, raw, stream)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{"temperature", "top_p", "metadata", "user", "safety_identifier", "prompt_cache_key", "prompt_cache_retention"} {
		if value := gjson.GetBytes(raw, field); value.Exists() {
			body, _ = sjson.SetRawBytes(body, field, []byte(value.Raw))
		}
	}
	for _, field := range []string{"max_completion_tokens", "max_tokens"} {
		if value := gjson.GetBytes(raw, field); value.Exists() {
			body, _ = sjson.SetRawBytes(body, "max_output_tokens", []byte(value.Raw))
			break
		}
	}
	return body, nil
}

func convertResponsesAPIToChat(ctx context.Context, model string, original, request, raw []byte, param *any) []byte {
	if gjson.GetBytes(raw, "object").String() == "response" {
		event := []byte(`{"type":"response.completed"}`)
		if gjson.GetBytes(raw, "status").String() == "incomplete" {
			event, _ = sjson.SetBytes(event, "type", "response.incomplete")
		}
		event, _ = sjson.SetRawBytes(event, "response", raw)
		raw = event
	}
	return ConvertCodexResponseToOpenAINonStream(ctx, model, original, request, raw, param)
}
