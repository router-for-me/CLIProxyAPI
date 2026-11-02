package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (h *Host) callHostModelExecuteStream(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostModelExecutionRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host model execution stream request: %w", errUnmarshal)
	}
	if !req.Stream {
		return nil, fmt.Errorf("host.model.execute_stream requires stream=true")
	}
	if errProxy := validateHostModelProxy(req.ProxyURL); errProxy != nil {
		return nil, errProxy
	}
	executor := h.currentModelExecutor()
	if executor == nil {
		return nil, fmt.Errorf("host model executor is unavailable")
	}
	if h == nil || h.modelStreams == nil {
		return nil, fmt.Errorf("host model stream bridge is unavailable")
	}
	callerPluginID := hostCallbackPluginIDFromContext(ctx)
	callerInstance := hostCallbackInstanceFromContext(ctx)
	var callbackPluginID string
	var callbackInstance *hostCallbackInstance
	if req.HostCallbackID != "" {
		var exists bool
		if _, callbackPluginID, callbackInstance, exists = h.lookupCallbackContext(req.HostCallbackID); exists {
			if callerPluginID != "" && callbackPluginID != "" && callerPluginID != callbackPluginID {
				return nil, fmt.Errorf("host callback ID does not belong to the calling plugin")
			}
			if callerInstance != nil && callbackInstance != nil && callerInstance != callbackInstance {
				return nil, fmt.Errorf("host callback ID does not belong to the calling plugin instance")
			}
		}
	}
	skipPluginID := h.callbackCallerPluginID(ctx, req.HostCallbackID)
	callbackCtx := h.resolveCallbackContext(req.HostCallbackID, ctx)
	if callbackCtx == nil {
		callbackCtx = context.Background()
	}
	// Detach request cancellation while preserving callback values; callback cleanup owns the model stream lifetime.
	streamCtx, cancel := newStreamContext(context.WithoutCancel(callbackCtx))
	inflightID, okBegin := h.modelStreams.begin(callerPluginID, callbackPluginID, callerInstance, callbackInstance, cancel)
	if !okBegin {
		return nil, fmt.Errorf("host plugin callback instance is closed")
	}
	stream, errMsg := executor.ExecuteModelStream(streamCtx, modelExecutionRequestFromPlugin(req.HostModelExecutionRequest, skipPluginID))
	if errMsg != nil {
		h.modelStreams.abort(inflightID)
		return nil, modelExecutionError(errMsg)
	}
	streamID := h.modelStreams.complete(inflightID, callerPluginID, callerInstance, req.HostCallbackID, stream.Chunks, cancel)
	if streamID == "" {
		return nil, fmt.Errorf("host model stream closed during startup")
	}
	if req.HostCallbackID != "" {
		stopScopeCleanup, attached := h.addCallbackCleanupHandle(req.HostCallbackID, func() {
			h.modelStreams.close(callerPluginID, callerInstance, streamID)
		})
		if !attached || !h.modelStreams.setScopeCleanup(callerPluginID, callerInstance, streamID, stopScopeCleanup) {
			h.modelStreams.close(callerPluginID, callerInstance, streamID)
			return nil, fmt.Errorf("host callback context closed while opening model stream")
		}
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
	chunk, done, errRead := h.modelStreams.read(ctx, hostCallbackPluginIDFromContext(ctx), hostCallbackInstanceFromContext(ctx), req.StreamID)
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

func (h *Host) callHostModelStreamClose(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostModelStreamCloseRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host model stream close request: %w", errUnmarshal)
	}
	if h != nil && h.modelStreams != nil {
		h.modelStreams.close(hostCallbackPluginIDFromContext(ctx), hostCallbackInstanceFromContext(ctx), req.StreamID)
	}
	return marshalRPCResult(rpcEmptyResponse{})
}
