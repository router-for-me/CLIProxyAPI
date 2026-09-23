package main

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// reasoningItemShape describes fields without retaining their content.
type reasoningItemShape struct {
	Index  int               `json:"index"`
	IDHash string            `json:"id_hash,omitempty"`
	Fields map[string]string `json:"fields"`
	Status string            `json:"status"`
}

// reasoningCollectionShape describes reasoning items within one payload array.
type reasoningCollectionShape struct {
	ItemCount int                  `json:"item_count"`
	Reasoning []reasoningItemShape `json:"reasoning"`
}

// observeRequestContext reports the replay input before provider translation.
// The after-credential hook runs even when an upstream rejects the request
// before a response stream exists.
func (r *runtimeState) observeRequestContext(req pluginapi.RequestInterceptRequest) {
	cfg := r.loadedConfig()
	if cfg == nil || !cfg.Enabled || cfg.aliasFor(req.RequestedModel) == nil || req.ToFormat == "" {
		return
	}
	fields := map[string]any{
		"event": "request_context", "version": pluginVersion,
		"request_id": req.RequestID, "trace_id": req.TraceID,
		"alias": req.RequestedModel, "model": req.Model,
		"source_format": req.SourceFormat, "target_format": req.ToFormat,
		"stage": "before_provider_translation",
	}
	var root map[string]any
	if errDecode := json.Unmarshal(req.Body, &root); errDecode != nil {
		fields["payload_state"] = "invalid_json"
	} else {
		fields["input_type"] = fmt.Sprintf("%T", root["input"])
		fields["input"] = inspectReasoningCollection(root["input"])
		fields["previous_response_hash"] = shortValueHash(stringJSONValue(root["previous_response_id"]))
	}
	r.logContextShape(fields)
}

// observeResponseContext reports the item shapes emitted at stream boundaries.
// The router observes these objects without changing the downstream transcript.
func (r *runtimeState) observeResponseContext(req pluginapi.StreamChunkInterceptRequest, payloads []map[string]any) {
	for _, payload := range payloads {
		event := stringJSONValue(payload["type"])
		if event != "response.output_item.added" && event != "response.output_item.done" && event != "response.completed" && event != "response.done" {
			continue
		}
		fields := map[string]any{
			"event": "response_context", "version": pluginVersion,
			"request_id": req.RequestID, "alias": req.RequestedModel,
			"model": req.Model, "stage": event, "chunk_index": req.ChunkIndex,
		}
		if event == "response.output_item.added" || event == "response.output_item.done" {
			item, ok := payload["item"].(map[string]any)
			if !ok || stringJSONValue(item["type"]) != "reasoning" {
				continue
			}
			fields["output"] = inspectReasoningCollection([]any{item})
		} else {
			response, ok := payload["response"].(map[string]any)
			if !ok {
				continue
			}
			fields["response_hash"] = shortValueHash(stringJSONValue(response["id"]))
			fields["output"] = inspectReasoningCollection(response["output"])
		}
		r.logContextShape(fields)
	}
}

// inspectReasoningCollection records array positions and field types only.
func inspectReasoningCollection(value any) reasoningCollectionShape {
	items, _ := value.([]any)
	shape := reasoningCollectionShape{ItemCount: len(items), Reasoning: []reasoningItemShape{}}
	for index, value := range items {
		item, ok := value.(map[string]any)
		if !ok || stringJSONValue(item["type"]) != "reasoning" {
			continue
		}
		fields := make(map[string]string, len(item))
		for name, value := range item {
			fields[name] = fmt.Sprintf("%T", value)
		}
		shape.Reasoning = append(shape.Reasoning, reasoningItemShape{
			Index: index, IDHash: shortValueHash(stringJSONValue(item["id"])),
			Fields: fields, Status: diagnosticStatus(item),
		})
	}
	return shape
}

// diagnosticStatus bounds status values so arbitrary payload text is never logged.
func diagnosticStatus(item map[string]any) string {
	value, exists := item["status"]
	if !exists {
		return "absent"
	}
	if value == nil {
		return "null"
	}
	status, ok := value.(string)
	if !ok {
		return "non_string"
	}
	switch status {
	case "in_progress", "completed", "incomplete":
		return status
	default:
		return "other"
	}
}

// logContextShape embeds structured diagnostics in the journal-visible message.
// Some hosts render the message without rendering arbitrary structured fields.
func (r *runtimeState) logContextShape(fields map[string]any) {
	raw, errMarshal := json.Marshal(fields)
	if errMarshal != nil {
		r.log("error", "model-sequence-router: context diagnostic encoding failed", map[string]any{"event": "context_diagnostic_error"}, "")
	} else {
		r.log("debug", "model-sequence-router: context "+string(raw), fields, "")
	}
}
