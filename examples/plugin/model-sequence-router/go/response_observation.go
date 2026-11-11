package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// observeStreamChunk records which conversation each provider response belongs
// to. The host delivers the client request on the header initialization call and
// delivers the response identifier later inside a payload chunk, so the request
// is held under its request identifier until a chunk names its response.
func (r *runtimeState) observeStreamChunk(req pluginapi.StreamChunkInterceptRequest) {
	cfg := r.loadedConfig()
	if cfg == nil || !cfg.Enabled {
		return
	}
	if cfg.aliasFor(req.RequestedModel) == nil {
		return
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		return
	}
	requestKey := continuationRequestKey{Generation: cfg.Generation, RequestID: requestID}
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex {
		_, identity := r.conversationState(identityInput{
			SourceFormat: req.SourceFormat,
			Body:         req.OriginalRequest,
			Metadata:     req.Metadata,
		}, cfg)
		r.chains.holdRequest(requestKey, identity, cfg.SessionTTL)
	} else {
		// Decode once so provenance, identity binding, and diagnostics observe the same events.
		payloads := responsePayloads(req.Body)
		r.observeResponseContext(cfg, req, payloads)
		r.recordReasoningOwners(cfg, req, payloads)
		r.bindResponseIdentity(cfg, payloads, r.chains.heldRequest(requestKey))
	}
}

// bindResponseIdentity names the conversation one provider response belongs to,
// reading the identifier from already-decoded payloads.
func (r *runtimeState) bindResponseIdentity(cfg *compiledConfig, payloads []map[string]any, identity conversationIdentity) {
	responseID := responseIDFromPayloads(payloads)
	if responseID == "" {
		return
	}
	r.chains.bindResponse(
		continuationResponseKey{Generation: cfg.Generation, ResponseID: responseID},
		identity,
		cfg.SessionTTL,
	)
}

// observeCompleteResponse records which conversation a non-streamed provider
// response belongs to. The host delivers the client request and the complete
// reply in one call, so the conversation binds without an intervening hold.
func (r *runtimeState) observeCompleteResponse(req pluginapi.ResponseInterceptRequest) {
	cfg := r.loadedConfig()
	if cfg == nil || !cfg.Enabled {
		return
	}
	if cfg.aliasFor(req.RequestedModel) == nil {
		return
	}
	_, identity := r.conversationState(identityInput{
		SourceFormat: req.SourceFormat,
		Body:         req.OriginalRequest,
		Metadata:     req.Metadata,
	}, cfg)
	r.bindResponseIdentity(cfg, responsePayloads(req.Body), identity)
}

// responsePayloads decodes raw JSON objects and server-sent event data lines.
func responsePayloads(chunk []byte) []map[string]any {
	payloads := make([]map[string]any, 0)
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		payload := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(line), []byte("data:")))
		if len(payload) == 0 || payload[0] != '{' {
			continue
		}
		var root map[string]any
		if errDecode := json.Unmarshal(payload, &root); errDecode != nil {
			continue
		}
		payloads = append(payloads, root)
	}
	return payloads
}

// responseIDFromPayloads reads the first response identifier in decoded events.
func responseIDFromPayloads(payloads []map[string]any) string {
	responseID := ""
	for index := 0; index < len(payloads) && responseID == ""; index++ {
		responseID = responseIDFromRoot(payloads[index])
	}
	return responseID
}

// responseIDFromRoot reads the response identifier of one decoded event, which
// either nests the response object under an event envelope or is that object.
func responseIDFromRoot(root map[string]any) string {
	identifier := ""
	if response, nested := root["response"].(map[string]any); nested {
		identifier, _ = response["id"].(string)
	} else if object, _ := root["object"].(string); object == "response" {
		identifier, _ = root["id"].(string)
	}
	return strings.TrimSpace(identifier)
}

// finishedOutputItems reads the output items one response event has finished. A
// finished item event carries the single item it completed, and a completed
// response carries its whole output array.
func finishedOutputItems(payload map[string]any) []any {
	var items []any
	switch stringJSONValue(payload["type"]) {
	case "response.output_item.done":
		item, isObject := payload["item"].(map[string]any)
		if !isObject {
			// an event framing no item object finished no output item
			break
		}
		items = []any{item}
	case "response.completed":
		response, isObject := payload["response"].(map[string]any)
		if !isObject {
			// an event framing no response object finished no output item
			break
		}
		items, _ = response["output"].([]any)
	default:
		// every other event names no finished output item
	}
	return items
}
