package main

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const routeReason = "per_conversation_sequence"

// routeDecision carries the resolved facts of one route.
type routeDecision struct {
	Config       *compiledConfig
	Alias        *compiledAlias
	SourceFormat string
	Stream       bool
	Observation  requestObservation
	Identity     conversationIdentity
	Selection    selectionResult
	TargetModel  string
}

func (r *runtimeState) route(req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	return r.routeWithCallback(req, "")
}

// routeWithCallback parses the request once, resolves conversation and turn
// identity, selects a sequence position, and emits either a provider target or the
// self target that carries the unavailable signal.
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
	observation := inspectRequest(req.Body, r.fingerprintSalt)
	decision := routeDecision{
		Config:       cfg,
		Alias:        alias,
		SourceFormat: req.SourceFormat,
		Stream:       req.Stream,
		Observation:  observation,
		Identity:     newConversationIdentity(req),
	}
	decision.Selection = r.selectSequencePosition(decision, available)
	switch decision.Selection.Outcome {
	case selectionExhausted:
		return r.unavailableTarget(decision, hostCallbackID)
	case selectionAdvanced, selectionReplayed, selectionStateless:
		decision.TargetModel = decision.Selection.Target.effectiveModel(requestedSuffix)
	default:
		panic(decision.Selection.Outcome)
	}
	r.logSkippedPositions(decision, hostCallbackID)

	// A cursor advances once per conversation state; a repeated state replays its
	// position and a request without identity moves no cursor at all.
	fields := map[string]any{
		"event": "route", "alias": alias.Alias, "sequence_index": decision.Selection.Index,
		"provider": decision.Selection.Target.Provider, "model": decision.TargetModel,
		"advanced":         decision.Selection.Outcome == selectionAdvanced,
		"outcome":          decision.Selection.Outcome.String(),
		"identity_source":  string(decision.Identity.source()),
		"skipped":          len(decision.Selection.Skipped),
		"random_start":     alias.RandomStart,
		"requested_effort": requestedSuffix,
	}
	if decision.Identity != "" {
		fields["session_hash"] = shortSessionHash(string(decision.Identity))
	}
	if cfg.Diagnostics.Enabled {
		for name, value := range r.diagnosticFields(decision) {
			fields[name] = value
		}
	}
	r.log("debug", routeSummary(alias.Alias, decision.Selection.Index, requestedSuffix), fields, hostCallbackID)
	return pluginapi.ModelRouteResponse{
		Handled:     true,
		TargetKind:  pluginapi.ModelRouteTargetProvider,
		Target:      decision.Selection.Target.Provider,
		TargetModel: decision.TargetModel,
		Reason:      routeReason,
	}
}

// selectSequencePosition reserves a position for an identified conversation and
// returns the first available position for a request that carries no identity.
func (r *runtimeState) selectSequencePosition(decision routeDecision, available map[string]struct{}) selectionResult {
	var selection selectionResult
	if decision.Identity == "" {
		selection = selectStateless(decision.Alias.Sequence, available)
	} else {
		selection = r.cursors.selectTarget(selectionRequest{
			Key:         cursorKey{Generation: decision.Config.Generation, Alias: decision.Alias.LookupKey, SessionID: string(decision.Identity)},
			Sequence:    decision.Alias.Sequence,
			Available:   available,
			Turn:        newTurnIdentity(decision.Observation),
			TTL:         decision.Config.SessionTTL,
			RandomStart: decision.Alias.RandomStart,
			ProbeLimit:  decision.Config.probeLimit(decision.Alias.Sequence),
		})
	}
	return selection
}

// logSkippedPositions emits one notice per sequence position passed over because
// that position's provider was unavailable.
func (r *runtimeState) logSkippedPositions(decision routeDecision, hostCallbackID string) {
	for _, skipped := range decision.Selection.Skipped {
		r.log("warn", "model-sequence-router: skipped unavailable sequence position", map[string]any{
			"event": "skip", "alias": decision.Alias.Alias,
			"sequence_index": skipped.Index, "provider": skipped.Provider,
		}, hostCallbackID)
	}
}

// unavailableTarget answers an exhausted probe. The skip policy declines so the
// host falls through to lower-priority routing. The error policy targets this
// plugin's own executor, which answers with a retryable status while the
// conversation holds its sequence position.
func (r *runtimeState) unavailableTarget(decision routeDecision, hostCallbackID string) pluginapi.ModelRouteResponse {
	fields := map[string]any{
		"event": "route", "alias": decision.Alias.Alias,
		"outcome":         decision.Selection.Outcome.String(),
		"identity_source": string(decision.Identity.source()),
		"providers":       uniqueProviders(decision.Alias.Sequence),
	}
	response := pluginapi.ModelRouteResponse{Handled: false}
	switch decision.Config.UnavailableProvider {
	case unavailableError:
		// An identified conversation probes one position, so its single record
		// names the position it kept. A stateless request records every position
		// it read, and its first record names the position a retry re-enters on.
		kept := decision.Selection.Skipped[0]
		fields["sequence_index"] = kept.Index
		fields["provider"] = kept.Provider
		r.log("warn", "model-sequence-router: reporting unavailable provider and keeping sequence position", fields, hostCallbackID)
		response = pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
			Reason:     unavailableCode,
		}
	case unavailableSkip:
		r.logSkippedPositions(decision, hostCallbackID)
		r.log("warn", "model-sequence-router: all configured providers unavailable", fields, hostCallbackID)
	default:
		panic(decision.Config.UnavailableProvider)
	}
	return response
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
