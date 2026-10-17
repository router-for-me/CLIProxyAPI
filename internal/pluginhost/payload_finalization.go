package pluginhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type hostPayloadFinalizationKey struct{}

type hostPayloadFinalizationContext struct {
	cfg      *config.Config
	executor string
	model    string
	original []byte
	req      coreexecutor.Request
	opts     coreexecutor.Options

	mu      sync.Mutex
	results map[hostPayloadFinalizationCacheKey]hostPayloadFinalizationResult
}

type hostPayloadFinalizationCacheKey struct {
	protocol string
	digest   [sha256.Size]byte
}

type hostPayloadFinalizationResult struct {
	input  []byte
	output []byte
}

type rpcHostPayloadFinalizeRequest struct {
	HostCallbackID string `json:"host_callback_id,omitempty"`
	Protocol       string `json:"protocol"`
	Body           []byte `json:"body"`
}

type rpcHostPayloadFinalizeResponse struct {
	Body []byte `json:"body"`
}

func newHostPayloadFinalizationContext(cfg *config.Config, executor string, prepared preparedExecutorCall) *hostPayloadFinalizationContext {
	model := strings.TrimSpace(thinking.ParseSuffix(prepared.req.Model).ModelName)
	if model == "" {
		model = strings.TrimSpace(prepared.req.Model)
	}
	req := coreexecutor.Request{
		Model:    prepared.req.Model,
		Payload:  bytes.Clone(prepared.req.Payload),
		Format:   prepared.req.Format,
		Metadata: cloneAnyMap(prepared.req.Metadata),
	}
	opts := coreexecutor.Options{
		Headers:         cloneHeader(prepared.opts.Headers),
		OriginalRequest: bytes.Clone(prepared.opts.OriginalRequest),
		SourceFormat:    prepared.inputRequested,
		ResponseFormat:  prepared.opts.ResponseFormat,
		Metadata:        cloneAnyMap(prepared.opts.Metadata),
	}
	return &hostPayloadFinalizationContext{
		cfg:      cfg,
		executor: strings.TrimSpace(executor),
		model:    model,
		original: bytes.Clone(prepared.req.Payload),
		req:      req,
		opts:     opts,
		results:  make(map[hostPayloadFinalizationCacheKey]hostPayloadFinalizationResult),
	}
}

func withHostPayloadFinalization(ctx context.Context, state *hostPayloadFinalizationContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, hostPayloadFinalizationKey{}, state)
}

func hostPayloadFinalizationFromContext(ctx context.Context) (*hostPayloadFinalizationContext, bool) {
	if ctx == nil {
		return nil, false
	}
	state, ok := ctx.Value(hostPayloadFinalizationKey{}).(*hostPayloadFinalizationContext)
	return state, ok && state != nil
}

func (s *hostPayloadFinalizationContext) finalize(protocol string, body []byte) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("host payload finalization context is unavailable")
	}
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		return nil, fmt.Errorf("host payload finalization protocol is required")
	}
	input := bytes.Clone(body)
	key := hostPayloadFinalizationCacheKey{protocol: protocol, digest: sha256.Sum256(input)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, ok := s.results[key]; ok {
		if !bytes.Equal(input, result.input) {
			return nil, fmt.Errorf("host payload finalization request digest collision")
		}
		return bytes.Clone(result.output), nil
	}
	finalize := helps.NewPayloadFinalizer(s.cfg, s.executor, s.model, protocol, "", s.original, s.req, s.opts)
	output := bytes.Clone(finalize(input))
	s.results[key] = hostPayloadFinalizationResult{input: input, output: bytes.Clone(output)}
	return output, nil
}

func (h *Host) callHostPayloadFinalize(ctx context.Context, request []byte) ([]byte, error) {
	var req rpcHostPayloadFinalizeRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host payload finalization request: %w", errUnmarshal)
	}
	callbackCtx, errResolve := h.resolvePayloadFinalizationCallback(req.HostCallbackID, ctx)
	if errResolve != nil {
		return nil, errResolve
	}
	if errContext := callbackCtx.Err(); errContext != nil {
		return nil, errContext
	}
	state, ok := hostPayloadFinalizationFromContext(callbackCtx)
	if !ok {
		return nil, fmt.Errorf("host payload finalization is unavailable for this callback")
	}
	body, errFinalize := state.finalize(req.Protocol, req.Body)
	if errFinalize != nil {
		return nil, errFinalize
	}
	return marshalRPCResult(rpcHostPayloadFinalizeResponse{Body: body})
}

func (h *Host) resolvePayloadFinalizationCallback(callbackID string, caller context.Context) (context.Context, error) {
	callbackID = strings.TrimSpace(callbackID)
	if h == nil || callbackID == "" {
		return nil, fmt.Errorf("host payload finalization requires an active executor callback")
	}
	callbackCtx, pluginID, instance, ok := h.lookupCallbackContext(callbackID)
	if !ok {
		return nil, fmt.Errorf("host payload finalization callback is unavailable")
	}
	if callerPluginID := hostCallbackPluginIDFromContext(caller); callerPluginID != "" && callerPluginID != pluginID {
		return nil, fmt.Errorf("host payload finalization callback belongs to another plugin")
	}
	if callerInstance := hostCallbackInstanceFromContext(caller); callerInstance != nil && callerInstance != instance {
		return nil, fmt.Errorf("host payload finalization callback belongs to another plugin instance")
	}
	if callbackCtx == nil {
		return nil, fmt.Errorf("host payload finalization callback context is unavailable")
	}
	return callbackCtx, nil
}
