package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type executionPlan struct {
	backend routeBackend
	model   string
}

func buildExecutionPlans(cfg pluginConfig, req pluginapi.ModelRouteRequest) []executionPlan {
	return buildExecutionPlansInternal(cfg, req, true)
}

func buildExecutionPlansForExecute(cfg pluginConfig, req pluginapi.ModelRouteRequest) []executionPlan {
	route := strings.TrimSpace(cfg.Route)
	if isFallbackRoute(route) {
		return buildExecutionPlansInternal(cfg, req, false)
	}
	return executionPlansForExecuteRoute(cfg, req, route)
}

// executionPlansForExecuteRoute builds plans for plugin executor without requiring
// ModelRouteRequest.AvailableProviders (host does not pass it on executor.execute_stream).
func executionPlansForExecuteRoute(cfg pluginConfig, req pluginapi.ModelRouteRequest, route string) []executionPlan {
	backend := routeBackend(strings.TrimSpace(route))
	if !backendRunnableLenient(backend, cfg, req) {
		return nil
	}
	var plans []executionPlan
	switch backend {
	case backendAntigravityGoogle:
		model := resolveAntigravityWebSearchTargetModel(cfg.AntigravityModel, req.RequestedModel)
		if model == "" {
			return nil
		}
		plans = append(plans, executionPlan{backend: backend, model: model})
	case backendCodexWebSearch:
		plans = append(plans, executionPlan{backend: backend, model: resolveCodexWebSearchTargetModel(cfg.CodexModel)})
	case backendXAIWebSearch:
		plans = append(plans, executionPlan{backend: backend, model: resolveXAIWebSearchTargetModel(cfg.XAIModel)})
	case backendTavily:
		if !newTavilyClient(cfg.TavilyAPIKeys).available() {
			return nil
		}
		plans = append(plans, executionPlan{backend: backend})
	default:
		return nil
	}
	return plans
}

func buildExecutionPlansInternal(cfg pluginConfig, req pluginapi.ModelRouteRequest, requireProviders bool) []executionPlan {
	var plans []executionPlan
	for _, backend := range defaultWebSearchFallbackChain() {
		if requireProviders {
			if _, ok := tryRouteBackend(backend, cfg, req); !ok {
				continue
			}
		} else if !backendRunnableLenient(backend, cfg, req) {
			continue
		}
		switch backend {
		case backendAntigravityGoogle:
			plans = append(plans, executionPlan{
				backend: backend,
				model:   resolveAntigravityWebSearchTargetModel(cfg.AntigravityModel, req.RequestedModel),
			})
		case backendCodexWebSearch:
			plans = append(plans, executionPlan{
				backend: backend,
				model:   resolveCodexWebSearchTargetModel(cfg.CodexModel),
			})
		case backendXAIWebSearch:
			plans = append(plans, executionPlan{
				backend: backend,
				model:   resolveXAIWebSearchTargetModel(cfg.XAIModel),
			})
		case backendTavily:
			plans = append(plans, executionPlan{backend: backend})
		default:
			continue
		}
	}
	return plans
}

func backendRunnableLenient(backend routeBackend, cfg pluginConfig, req pluginapi.ModelRouteRequest) bool {
	switch backend {
	case backendTavily:
		return newTavilyClient(cfg.TavilyAPIKeys).available()
	case backendAntigravityGoogle:
		return resolveAntigravityWebSearchTargetModel(cfg.AntigravityModel, req.RequestedModel) != ""
	case backendCodexWebSearch, backendXAIWebSearch:
		return true
	default:
		return false
	}
}

func executionPlansForRoute(cfg pluginConfig, req pluginapi.ModelRouteRequest, route string) []executionPlan {
	if isFallbackRoute(route) {
		return buildExecutionPlans(cfg, req)
	}
	backend := routeBackend(strings.TrimSpace(route))
	if _, ok := tryRouteBackend(backend, cfg, req); !ok {
		return nil
	}
	var plans []executionPlan
	for _, b := range []routeBackend{backend} {
		if !backendRunnableLenient(b, cfg, req) {
			continue
		}
		switch b {
		case backendAntigravityGoogle:
			plans = append(plans, executionPlan{backend: b, model: resolveAntigravityWebSearchTargetModel(cfg.AntigravityModel, req.RequestedModel)})
		case backendCodexWebSearch:
			plans = append(plans, executionPlan{backend: b, model: resolveCodexWebSearchTargetModel(cfg.CodexModel)})
		case backendXAIWebSearch:
			plans = append(plans, executionPlan{backend: b, model: resolveXAIWebSearchTargetModel(cfg.XAIModel)})
		case backendTavily:
			plans = append(plans, executionPlan{backend: b})
		}
	}
	return plans
}

func claudeRequestBody(exec pluginapi.ExecutorRequest) []byte {
	if len(exec.OriginalRequest) > 0 {
		return exec.OriginalRequest
	}
	return exec.Payload
}

func runWebSearchWithExecutionFallback(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID string) ([]byte, http.Header, error) {
	cfg := loadedConfig()
	req := pluginapi.ModelRouteRequest{
		SourceFormat:       "claude",
		RequestedModel:     strings.TrimSpace(exec.Model),
		Body:               claudeRequestBody(exec),
		AvailableProviders: availableProvidersFromMetadata(exec.Metadata),
	}
	return runOrderedExecutionPlans(ctx, exec, hostCallbackID, cfg, buildExecutionPlansForExecute(cfg, req), false)
}

// runWebSearchStreamWithExecutionFallback buffers the full host stream (non-streaming RPC path only).
func runWebSearchStreamWithExecutionFallback(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID string) ([]byte, http.Header, error) {
	cfg := loadedConfig()
	req := pluginapi.ModelRouteRequest{
		SourceFormat:       "claude",
		RequestedModel:     strings.TrimSpace(exec.Model),
		Body:               claudeRequestBody(exec),
		AvailableProviders: availableProvidersFromMetadata(exec.Metadata),
	}
	return runOrderedExecutionPlans(ctx, exec, hostCallbackID, cfg, buildExecutionPlansForExecute(cfg, req), true)
}

func runOrderedExecutionPlans(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID string, cfg pluginConfig, plans []executionPlan, stream bool) ([]byte, http.Header, error) {
	if len(plans) == 0 {
		return nil, nil, fmt.Errorf("web search execution: no backend available")
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
			var payload []byte
			var headers http.Header
			var errRun error
			if stream {
				payload, headers, errRun = runTavilyClaudeStreamWithClient(ctx, exec, newTavilyClient(cfg.TavilyAPIKeys))
			} else {
				payload, headers, errRun = runTavilyClaudeWithClient(ctx, exec, newTavilyClient(cfg.TavilyAPIKeys))
			}
			if errRun != nil {
				lastErr = errRun
				continue
			}
			recordBackendSuccess(backend)
			return payload, headers, nil
		default:
			payload, status, errRun := hostModelExecuteClaude(ctx, hostCallbackID, plan.model, body, stream)
			if errRun != nil {
				lastErr = errRun
				if isRetryableHTTPStatus(hostHTTPStatusFromError(errRun)) {
					recordBackendFailure(backend)
				}
				continue
			}
			if isRetryableHTTPStatus(status) {
				recordBackendFailure(backend)
				lastErr = fmt.Errorf("host model status %d", status)
				continue
			}
			recordBackendSuccess(backend)
			headers := http.Header{"Content-Type": []string{"application/json"}}
			if stream {
				headers = http.Header{"Content-Type": []string{"text/event-stream"}}
			}
			return payload, headers, nil
		}
	}
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, fmt.Errorf("web search execution: all backends failed")
}

func availableProvidersFromMetadata(meta map[string]any) []string {
	if meta == nil {
		return nil
	}
	raw, ok := meta["available_providers"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, okItem := item.(string); okItem {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func hostModelExecuteClaude(ctx context.Context, hostCallbackID, execModel string, body []byte, stream bool) ([]byte, int, error) {
	if stream {
		return hostModelStreamClaude(ctx, hostCallbackID, execModel, body)
	}
	raw, errCall := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "claude",
			ExitProtocol:  "claude",
			Model:         execModel,
			Stream:        false,
			Body:          body,
		},
		HostCallbackID: hostCallbackID,
	})
	if errCall != nil {
		return nil, hostHTTPStatusFromError(errCall), errCall
	}
	var resp pluginapi.HostModelExecutionResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, 0, errDecode
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("host model status %d", resp.StatusCode)
	}
	return resp.Body, resp.StatusCode, nil
}

func hostModelStreamClaude(ctx context.Context, hostCallbackID, execModel string, body []byte) ([]byte, int, error) {
	raw, errCall := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "claude",
			ExitProtocol:  "claude",
			Model:         execModel,
			Stream:        true,
			Body:          body,
		},
		HostCallbackID: hostCallbackID,
	})
	if errCall != nil {
		return nil, hostHTTPStatusFromError(errCall), errCall
	}
	var resp pluginapi.HostModelStreamResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, 0, errDecode
	}
	if resp.StatusCode >= 400 {
		_ = closeHostModelStream(resp.StreamID)
		return nil, resp.StatusCode, fmt.Errorf("host model status %d", resp.StatusCode)
	}
	if strings.TrimSpace(resp.StreamID) == "" {
		return nil, 0, fmt.Errorf("host model stream: empty stream_id")
	}
	defer func() { _ = closeHostModelStream(resp.StreamID) }()

	var buf bytes.Buffer
	for {
		chunkRaw, errRead := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: resp.StreamID})
		if errRead != nil {
			return nil, hostHTTPStatusFromError(errRead), errRead
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if errDecode := json.Unmarshal(chunkRaw, &chunk); errDecode != nil {
			return nil, 0, errDecode
		}
		if chunk.Error != "" {
			code := hostHTTPStatusFromError(fmt.Errorf("%s", chunk.Error))
			return nil, code, fmt.Errorf("%s", chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			buf.Write(chunk.Payload)
		}
		if chunk.Done {
			break
		}
	}
	return buf.Bytes(), http.StatusOK, nil
}

func closeHostModelStream(streamID string) error {
	_, errCall := callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return errCall
}
