package responses

import (
	"bytes"
	"context"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertCodexResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).

func ConvertCodexResponseToOpenAIResponses(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	originalEvent := rawJSON
	sse := bytes.HasPrefix(rawJSON, []byte("data:"))
	if sse {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}
	updated := setResponsesModel(rawJSON, modelName, originalRequestRawJSON, requestRawJSON)
	bridge := responsesBridge(param)
	if bridge == nil {
		// Native Codex never opts in, even when configuration supplies the bridge schema.
		if bytes.Equal(updated, rawJSON) {
			return [][]byte{originalEvent}
		}
		if sse {
			updated = append([]byte("data: "), updated...)
		}
		return [][]byte{updated}
	}
	outputs, _ := bridge.Transform(updated)
	if sse {
		for i := range outputs {
			outputs[i] = append([]byte("data: "), outputs[i]...)
		}
	}
	return outputs
}

// Only an executor-owned param can enable bridging on this shared translator.
func responsesBridge(param *any) *translatorcommon.ApplyPatchResponsesBridge {
	if param != nil {
		bridge, _ := (*param).(*translatorcommon.ApplyPatchResponsesBridge)
		return bridge
	}
	return nil
}

func setResponsesModel(rawJSON []byte, modelName string, originalRequestRawJSON, requestRawJSON []byte) []byte {
	eventType := gjson.GetBytes(rawJSON, "type").String()
	if eventType != "response.created" && eventType != "response.in_progress" {
		return rawJSON
	}
	if gjson.GetBytes(rawJSON, "response.model").Exists() {
		return rawJSON
	}

	requestModelName := translatorcommon.RequestModelName(originalRequestRawJSON, requestRawJSON)
	if requestModelName == "" {
		requestModelName = modelName
	}
	if requestModelName == "" {
		return rawJSON
	}

	updated, errSet := sjson.SetBytes(rawJSON, "response.model", requestModelName)
	if errSet != nil {
		return rawJSON
	}
	return updated
}

// ConvertCodexResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertCodexResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
	bridge := responsesBridge(param)
	converted := rawJSON
	if bridge != nil {
		var errTransform error
		converted, errTransform = bridge.TransformNonStream(rawJSON)
		if errTransform != nil {
			return nil
		}
	}
	rootResult := gjson.ParseBytes(converted)
	// Verify this is a terminal response event.
	responseType := rootResult.Get("type").String()
	if responseType == "" && rootResult.Get("output").IsArray() {
		return converted
	}
	if responseType != "response.completed" && responseType != "response.incomplete" {
		return []byte{}
	}
	responseResult := rootResult.Get("response")
	return []byte(responseResult.Raw)
}
