package main

import (
	"crypto/sha256"
	"fmt"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const routeReason = "per_conversation_sequence"

func (r *runtimeState) route(req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	return r.routeWithCallback(req, "")
}

func (r *runtimeState) routeWithCallback(req pluginapi.ModelRouteRequest, hostCallbackID string) pluginapi.ModelRouteResponse {
	cfg := r.loadedConfig()
	if cfg == nil || !cfg.Enabled {
		return pluginapi.ModelRouteResponse{Handled: false}
	}
	requested := strings.TrimSpace(req.RequestedModel)
	requestedBase, requestedSuffix, _ := parseSupportedEffortSuffix(requested)
	alias := cfg.ByLookup[normalizedAliasKey(requestedBase)]
	if alias == nil {
		return pluginapi.ModelRouteResponse{Handled: false}
	}
	available := make(map[string]struct{}, len(req.AvailableProviders))
	for _, provider := range req.AvailableProviders {
		if key := strings.ToLower(strings.TrimSpace(provider)); key != "" {
			available[key] = struct{}{}
		}
	}
	sessionID := coreauth.ExtractSessionID(req.Headers, req.Body, req.Metadata)
	var (
		selected compiledTarget
		index    int
		ok       bool
	)
	if sessionID == "" {
		selected, index, ok = selectStateless(alias.Sequence, available)
		r.log("debug", "model-sequence-router: stateless first-target routing", map[string]any{
			"alias": alias.Alias,
		}, hostCallbackID)
	} else {
		key := cursorKey{Generation: cfg.Generation, Alias: alias.LookupKey, SessionID: sessionID}
		selected, index, ok = r.cursors.selectTarget(key, alias.Sequence, available, cfg.SessionTTL, alias.RandomStart)
	}
	if !ok {
		providers := uniqueProviders(alias.Sequence)
		r.log("warn", "model-sequence-router: all configured providers unavailable", map[string]any{
			"alias": alias.Alias, "providers": providers,
		}, hostCallbackID)
		return pluginapi.ModelRouteResponse{Handled: false}
	}
	targetModel := selected.effectiveModel(requestedSuffix)

	// A conversation cursor reserved and moved one sequence position; stateless routing moves none.
	advanced := sessionID != ""
	fields := map[string]any{
		"alias": alias.Alias, "sequence_index": index, "provider": selected.Provider,
		"model": targetModel, "advanced": advanced, "random_start": alias.RandomStart,
		"requested_effort": requestedSuffix,
	}
	if sessionID != "" {
		fields["session_hash"] = shortSessionHash(sessionID)
	}
	if cfg.Diagnostics.Enabled {
		observation := inspectRequest(req.Body, r.fingerprintSalt)
		opaqueContinuation := observation.HasPreviousID || observation.HasConversationID || observation.HasContainer
		fields["event"] = "route"
		fields["source_format"] = req.SourceFormat
		fields["stream"] = req.Stream
		fields["input_kind"] = observation.InputKind
		fields["has_tool_result"] = observation.HasToolResult
		fields["has_previous_response_id"] = observation.HasPreviousID
		fields["has_conversation_id"] = observation.HasConversationID
		fields["has_hosted_container"] = observation.HasContainer
		fields["opaque_continuation"] = opaqueContinuation
		fields["portable_history"] = len(observation.HistoryItems) > 0 && !opaqueContinuation
		fields["thinking_signature_count"] = observation.ThinkingSignatures
		fields["encrypted_reasoning_count"] = observation.EncryptedReasoning
		fields["system_fingerprint"] = observation.SystemFingerprint
		fields["tools_fingerprint"] = observation.ToolsFingerprint
		fields["history_fingerprint"] = observation.HistoryFingerprint
		fields["history_items"] = len(observation.HistoryItems)
		if sessionID != "" {
			key := laneObservationKey{
				Generation: cfg.Generation,
				Alias:      alias.LookupKey,
				SessionID:  sessionID,
				Provider:   selected.Provider,
				Model:      targetModel,
			}
			for name, value := range r.observations.observe(key, observation, cfg.SessionTTL) {
				fields[name] = value
			}
		}
	}
	r.log("debug", routeSummary(alias.Alias, index, requestedSuffix), fields, hostCallbackID)
	return pluginapi.ModelRouteResponse{
		Handled:     true,
		TargetKind:  pluginapi.ModelRouteTargetProvider,
		Target:      selected.Provider,
		TargetModel: targetModel,
		Reason:      routeReason,
	}
}

// routeSummary renders the alias, the sequence position, and the caller's
// requested effort into the log message. A host prints the message verbatim
// while selecting only a subset of the structured fields, so carrying these
// values in the message keeps a route decision readable on any host.
func routeSummary(alias string, index int, requestedSuffix string) string {
	var requested string
	if requestedSuffix == "" {
		requested = "unset"
	} else {
		requested = requestedSuffix
	}
	return fmt.Sprintf("model-sequence-router: selected target alias=%s position=%d requested=%s", alias, index, requested)
}

func uniqueProviders(sequence []compiledTarget) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, target := range sequence {
		if _, exists := seen[target.Provider]; exists {
			continue
		}
		seen[target.Provider] = struct{}{}
		out = append(out, target.Provider)
	}
	return out
}

func shortSessionHash(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("%x", sum[:4])
}
