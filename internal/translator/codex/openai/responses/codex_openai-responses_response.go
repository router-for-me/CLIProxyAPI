package responses

import (
	"bytes"
	"context"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/tidwall/gjson"
)

func codexSSEDataChunk(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+8)
	out = append(out, "data: "...)
	out = append(out, payload...)
	out = append(out, '\n', '\n')
	return out
}

func codexEnsureSSEFrame(chunk []byte) []byte {
	trimmedRight := bytes.TrimRight(chunk, "\r\n")
	out := bytes.Clone(trimmedRight)
	out = append(out, '\n', '\n')
	return out
}

// ConvertCodexResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).

func ConvertCodexResponseToOpenAIResponses(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) [][]byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 {
		return [][]byte{}
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) ||
		bytes.HasPrefix(trimmed, []byte(":")) ||
		bytes.HasPrefix(trimmed, []byte("id:")) ||
		bytes.HasPrefix(trimmed, []byte("retry:")) {
		return [][]byte{codexEnsureSSEFrame(rawJSON)}
	}
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		payload := bytes.TrimSpace(rawJSON[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return [][]byte{}
		}
		eventType := gjson.GetBytes(payload, "type").String()
		if eventType == "" {
			return [][]byte{codexSSEDataChunk(payload)}
		}
		return [][]byte{translatorcommon.SSEEventData(eventType, payload)}
	}
	if eventType := gjson.GetBytes(trimmed, "type").String(); eventType != "" {
		return [][]byte{translatorcommon.SSEEventData(eventType, trimmed)}
	}
	return [][]byte{codexSSEDataChunk(trimmed)}
}

// ConvertCodexResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertCodexResponseToOpenAIResponsesNonStream(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) []byte {
	rootResult := gjson.ParseBytes(rawJSON)
	// Verify this is a response.completed event
	if rootResult.Get("type").String() != "response.completed" {
		return []byte{}
	}
	responseResult := rootResult.Get("response")
	return []byte(responseResult.Raw)
}
