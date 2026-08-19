package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (h *Host) callHostModelExecuteStream(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostModelExecutionRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host model execution stream request: %w", errUnmarshal)
	}
	if !req.Stream {
		return nil, fmt.Errorf("host.model.execute_stream requires stream=true")
	}
	executor := h.currentModelExecutor()
	if executor == nil {
		return nil, fmt.Errorf("host model executor is unavailable")
	}
	skipPluginID := h.callbackCallerPluginID(ctx, req.HostCallbackID)
	callbackCtx := h.resolveCallbackContext(req.HostCallbackID, ctx)
	if callbackCtx == nil {
		callbackCtx = context.Background()
	}
	// Detach request cancellation while preserving callback values; callback cleanup owns the model stream lifetime.
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(callbackCtx))
	// cancel transfers to the stream bridge on success below.
	_ = cancel
	stream, errMsg := executor.ExecuteModelStream(streamCtx, modelExecutionRequestFromPlugin(req.HostModelExecutionRequest, skipPluginID))
	if errMsg != nil {
		cancel()
		return nil, modelExecutionError(errMsg)
	}
	streamID := h.openModelStreamWithCancel(req.HostCallbackID, stream.Chunks, cancel)
	if streamID == "" {
		return nil, fmt.Errorf("host model stream bridge is unavailable")
	}
	if req.HostCallbackID != "" {
		h.addCallbackCleanup(req.HostCallbackID, func() {
			h.modelStreams.close(streamID)
		})
	}
	return marshalRPCResult(pluginapi.HostModelStreamResponse{
		StatusCode: stream.StatusCode,
		Headers:    cloneHeader(stream.Headers),
		StreamID:   streamID,
	})
}

func (h *Host) callHostModelStreamRead(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostModelStreamReadRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host model stream read request: %w", errUnmarshal)
	}
	if h == nil || h.modelStreams == nil {
		return nil, fmt.Errorf("host model stream bridge is unavailable")
	}
	chunk, done, errRead := h.modelStreams.read(ctx, req.StreamID)
	if errRead != nil {
		return nil, errRead
	}
	resp := pluginapi.HostModelStreamReadResponse{
		Payload: append([]byte(nil), chunk.Payload...),
		Done:    done,
	}
	if chunk.Err != nil {
		resp.Error = chunk.Err.Error()
		resp.Done = true
	}
	return marshalRPCResult(resp)
}

func (h *Host) callHostModelStreamClose(request []byte) ([]byte, error) {
	var req pluginapi.HostModelStreamCloseRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host model stream close request: %w", errUnmarshal)
	}
	if h != nil && h.modelStreams != nil {
		h.modelStreams.close(req.StreamID)
	}
	return marshalRPCResult(rpcEmptyResponse{})
}

// openModelStreamWithCancel registers the stream chunk channel and hands the
// cancel function over to the stream registry, which owns calling it when the
// stream is closed. When the registry is unavailable the cancel is consumed
// here so no context is leaked.
func (h *Host) openModelStreamWithCancel(callbackID string, chunks <-chan handlers.ModelExecutionChunk, cancel context.CancelFunc) string {
	if h == nil || h.modelStreams == nil {
		cancel()
		return ""
	}
	return h.modelStreams.open(callbackID, chunks, cancel)
}
