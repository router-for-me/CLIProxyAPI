package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type rpcHostContextDoneRequest struct {
	HostCallbackID string `json:"host_callback_id"`
}

type rpcHostContextDoneResponse struct {
	// Done is true when the call's context ended (the client went away or
	// the request timed out) and false when the call returned first.
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

// callHostContextDone blocks until the context behind a live host callback
// ID ends or the plugin call that opened it returns. A plugin call runs
// synchronously in C, so the host cannot interrupt it; a plugin that blocks
// inside a call (a scheduler holding a pick) calls this from another thread
// to learn that its caller is gone.
func (h *Host) callHostContextDone(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostContextDoneRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host context done request: %w", errUnmarshal)
	}
	id := strings.TrimSpace(req.HostCallbackID)
	if id == "" {
		return nil, fmt.Errorf("host callback id is required")
	}
	callCtx, pluginID, instance, ok := h.lookupCallbackContext(id)
	if !ok {
		// The call already returned.
		return marshalRPCResult(rpcHostContextDoneResponse{})
	}
	if caller := hostCallbackPluginIDFromContext(ctx); caller != "" && pluginID != "" && caller != pluginID {
		return nil, fmt.Errorf("host callback id does not belong to plugin %s", caller)
	}
	if caller := hostCallbackInstanceFromContext(ctx); caller != instance && (caller != nil || instance != nil) {
		return nil, fmt.Errorf("host callback id does not belong to the calling plugin instance")
	}
	returned := make(chan struct{})
	remove, _ := h.addCallbackCleanupHandle(id, func() { close(returned) })
	defer remove()
	select {
	case <-callCtx.Done():
		return marshalRPCResult(rpcHostContextDoneResponse{Done: true, Error: callCtx.Err().Error()})
	case <-returned:
		if errCtx := callCtx.Err(); errCtx != nil {
			return marshalRPCResult(rpcHostContextDoneResponse{Done: true, Error: errCtx.Error()})
		}
		return marshalRPCResult(rpcHostContextDoneResponse{})
	}
}
