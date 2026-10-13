package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func emitPluginStreamChunk(streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return fmt.Errorf("plugin stream id is required")
	}
	_, errCall := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
	return errCall
}

func closePluginStream(streamID, errMsg string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = callHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{
		StreamID: streamID,
		Error:    strings.TrimSpace(errMsg),
	})
}

func looksLikeOpenAIResponsesSSE(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	s := string(payload)
	if strings.Contains(s, "event: message_start") {
		return false
	}
	return strings.Contains(s, "event: response.") ||
		strings.Contains(s, `"type":"response.`) ||
		strings.Contains(s, `"type": "response.`)
}

func runWebSearchStreamOrchestration(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID, pluginStreamID string) error {
	cfg := loadedConfig()
	req := pluginapi.ModelRouteRequest{
		SourceFormat:       "claude",
		RequestedModel:     strings.TrimSpace(exec.Model),
		Body:               claudeRequestBody(exec),
		AvailableProviders: availableProvidersFromMetadata(exec.Metadata),
	}
	return runOrderedExecutionPlansStream(ctx, exec, hostCallbackID, pluginStreamID, cfg, buildExecutionPlansForExecute(cfg, req))
}

func runOrderedExecutionPlansStream(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID, pluginStreamID string, cfg pluginConfig, plans []executionPlan) error {
	ctx = withHostHTTP(ctx, newHostHTTPBridge(hostCallbackID))
	if len(plans) == 0 {
		return fmt.Errorf("web search execution: no backend available")
	}
	backends := make([]routeBackend, 0, len(plans))
	for _, p := range plans {
		backends = append(backends, p.backend)
	}
	ordered := sortBackendsByPenalty(backends)
	planByBackend := make(map[routeBackend]executionPlan, len(plans))
	for _, p := range plans {
		planByBackend[p.backend] = p
	}

	body := claudeRequestBody(exec)
	var lastErr error
	for _, backend := range ordered {
		plan := planByBackend[backend]
		switch backend {
		case backendTavily:
			payload, _, errRun := runTavilyClaudeStreamWithClient(ctx, exec, newTavilyClient(cfg.TavilyAPIKeys))
			if errRun != nil {
				lastErr = errRun
				continue
			}
			if errEmit := emitPluginStreamChunk(pluginStreamID, payload); errEmit != nil {
				return errEmit
			}
			recordBackendSuccess(backend)
			return nil
		case backendKimiWebSearch:
			client := sharedKimiClient(cfg.KimiAPIKeys, cfg.KimiBaseURL)
			payload, _, errRun := runKimiClaudeStreamWithClient(ctx, exec, client)
			if errRun != nil {
				lastErr = errRun
				if isRetryableHTTPStatus(hostHTTPStatusFromError(errRun)) {
					recordBackendFailure(backend)
				}
				continue
			}
			if errEmit := emitPluginStreamChunk(pluginStreamID, payload); errEmit != nil {
				return errEmit
			}
			recordBackendSuccess(backend)
			return nil
		case backendKimiResponses:
			status, partial, errRun := hostModelStreamForwardKimiResponses(ctx, hostCallbackID, cfg, body, pluginStreamID)
			if errRun != nil {
				lastErr = errRun
				if isRetryableHTTPStatus(hostHTTPStatusFromError(errRun)) {
					recordBackendFailure(backend)
				}
				if partial {
					// Chunks of this message were already emitted on the
					// plugin stream; starting another backend here would
					// emit a second message_start and corrupt the stream.
					return errRun
				}
				continue
			}
			if isRetryableHTTPStatus(status) {
				recordBackendFailure(backend)
				lastErr = fmt.Errorf("host model status %d", status)
				if partial {
					return lastErr
				}
				continue
			}
			recordBackendSuccess(backend)
			return nil
		case backendKimiCoding:
			payload, _, errRun := runKimiCodingClaudeStream(ctx, exec, cfg, hostCallbackID)
			if errRun != nil {
				lastErr = errRun
				if isRetryableHTTPStatus(hostHTTPStatusFromError(errRun)) {
					recordBackendFailure(backend)
				}
				continue
			}
			if errEmit := emitPluginStreamChunk(pluginStreamID, payload); errEmit != nil {
				return errEmit
			}
			recordBackendSuccess(backend)
			return nil
		default:
			status, partial, errRun := hostModelStreamForwardClaude(ctx, hostCallbackID, plan.model, body, pluginStreamID)
			if errRun != nil {
				lastErr = errRun
				if isRetryableHTTPStatus(hostHTTPStatusFromError(errRun)) {
					recordBackendFailure(backend)
				}
				if partial {
					return errRun
				}
				continue
			}
			if isRetryableHTTPStatus(status) {
				recordBackendFailure(backend)
				lastErr = fmt.Errorf("host model status %d", status)
				if partial {
					return lastErr
				}
				continue
			}
			recordBackendSuccess(backend)
			return nil
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("web search execution: all backends failed")
}

func hostModelStreamForwardClaude(ctx context.Context, hostCallbackID, execModel string, body []byte, pluginStreamID string) (int, bool, error) {
	return hostModelStreamForwardRequest(ctx, hostCallbackID, pluginapi.HostModelExecutionRequest{
		EntryProtocol: "claude",
		ExitProtocol:  "claude",
		Model:         execModel,
		Stream:        true,
		Body:          body,
	}, pluginStreamID)
}

// hostModelStreamForwardRequest forwards the host model stream chunk by chunk
// to the plugin stream. The returned partial flag is true once any chunk has
// been emitted, so callers know a retry on the same stream ID would corrupt
// the client stream.
func hostModelStreamForwardRequest(ctx context.Context, hostCallbackID string, hreq pluginapi.HostModelExecutionRequest, pluginStreamID string) (int, bool, error) {
	hreq.Stream = true
	raw, errCall := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: hreq,
		HostCallbackID:            hostCallbackID,
	})
	if errCall != nil {
		return hostHTTPStatusFromError(errCall), false, errCall
	}
	var resp pluginapi.HostModelStreamResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return 0, false, errDecode
	}
	if resp.StatusCode >= 400 {
		_ = closeHostModelStream(resp.StreamID)
		return resp.StatusCode, false, fmt.Errorf("host model status %d", resp.StatusCode)
	}
	if strings.TrimSpace(resp.StreamID) == "" {
		return 0, false, fmt.Errorf("host model stream: empty stream_id")
	}
	defer func() { _ = closeHostModelStream(resp.StreamID) }()

	firstPayload := true
	partial := false
	for {
		chunkRaw, errRead := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: resp.StreamID})
		if errRead != nil {
			return hostHTTPStatusFromError(errRead), partial, errRead
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if errDecode := json.Unmarshal(chunkRaw, &chunk); errDecode != nil {
			return 0, partial, errDecode
		}
		if chunk.Error != "" {
			code := hostHTTPStatusFromError(fmt.Errorf("%s", chunk.Error))
			return code, partial, fmt.Errorf("%s", chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			if firstPayload && looksLikeOpenAIResponsesSSE(chunk.Payload) {
				return 0, partial, fmt.Errorf("host model stream returned OpenAI Responses SSE instead of Claude Messages SSE")
			}
			firstPayload = false
			partial = true
			if errEmit := emitPluginStreamChunk(pluginStreamID, bytes.Clone(chunk.Payload)); errEmit != nil {
				return 0, partial, errEmit
			}
		}
		if chunk.Done {
			break
		}
	}
	return http.StatusOK, partial, nil
}
