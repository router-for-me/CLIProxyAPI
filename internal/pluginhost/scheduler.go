package pluginhost

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func (h *Host) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	records := h.schedulerRecords()
	if len(records) == 0 {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}

	remainingCandidates := req.Candidates
	var accumulatedExcluded []string
	var excludedSet map[string]struct{}

	for _, record := range records {
		offeredCandidates := remainingCandidates
		if !schedulerWantsAcrossPriorities(record.plugin.Capabilities) {
			offeredCandidates = highestPrioritySchedulerCandidates(remainingCandidates)
		}
		subReq := cloneSchedulerPickRequest(req, offeredCandidates)

		resp, called, errPick := h.callScheduler(ctx, record, subReq)
		if errPick != nil {
			return resp, true, errPick
		}
		if !called {
			continue
		}
		if !resp.Handled {
			excluded := normalizeExcludedCandidates(resp.ExcludeCandidates)
			if len(excluded) > 0 {
				accumulatedExcluded, excludedSet = appendExcludedCandidates(accumulatedExcluded, excludedSet, excluded)
				remainingCandidates = filterSchedulerCandidates(remainingCandidates, excludedSet)
				if len(remainingCandidates) == 0 {
					return pluginapi.SchedulerPickResponse{ExcludeCandidates: accumulatedExcluded}, false, nil
				}
			}
			continue
		}

		normalized, valid, reason := normalizeSchedulerResponse(resp, subReq)
		if !valid {
			log.WithField("plugin_id", record.id).Warnf("pluginhost: scheduler returned invalid response: %s", reason)
			continue
		}
		if !normalized.Reject && normalized.AuthID == "" && normalized.DelegateBuiltin != "" && len(accumulatedExcluded) > 0 {
			mergedExcluded, _ := appendExcludedCandidates(accumulatedExcluded, excludedSet, normalized.ExcludeCandidates)
			normalized.ExcludeCandidates = mergedExcluded
		}
		return normalized, true, nil
	}

	if len(accumulatedExcluded) > 0 {
		return pluginapi.SchedulerPickResponse{ExcludeCandidates: accumulatedExcluded}, false, nil
	}
	return pluginapi.SchedulerPickResponse{}, false, nil
}

func (h *Host) HasScheduler() bool {
	return h.schedulerRecord() != nil
}

func (h *Host) SchedulerWantsAcrossPriorities() bool {
	if h == nil {
		return false
	}
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.Scheduler == nil {
			continue
		}
		if schedulerWantsAcrossPriorities(record.plugin.Capabilities) {
			return true
		}
	}
	return false
}

// SchedulerWantsAllPriorityCandidates reports whether PickAuth needs candidates across all
// priority tiers, either because a scheduler opted into across-priorities selection or because
// multiple active schedulers may chain across excluded tiers.
func (h *Host) SchedulerWantsAllPriorityCandidates() bool {
	if h == nil {
		return false
	}
	activeCount := 0
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.Scheduler == nil {
			continue
		}
		if schedulerWantsAcrossPriorities(record.plugin.Capabilities) {
			return true
		}
		activeCount++
		if activeCount > 1 {
			return true
		}
	}
	return false
}

// HasSchedulerContinuity reports whether the active scheduler plugin provides SchedulerContinuity.
func (h *Host) HasSchedulerContinuity() bool {
	return h.schedulerContinuityRecord() != nil
}

// EvaluateSchedulerContinuity invokes the active scheduler plugin's Continuity callback.
func (h *Host) EvaluateSchedulerContinuity(ctx context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
	record := h.schedulerContinuityRecord()
	if record == nil {
		return pluginapi.SchedulerContinuityResponse{}, false, nil
	}
	return h.callSchedulerContinuity(ctx, *record, req)
}

func (h *Host) schedulerContinuityRecord() *capabilityRecord {
	record := h.schedulerRecord()
	if record == nil || record.plugin.Capabilities.SchedulerContinuity == nil {
		return nil
	}
	return record
}

func (h *Host) callSchedulerContinuity(ctx context.Context, record capabilityRecord, req pluginapi.SchedulerContinuityRequest) (resp pluginapi.SchedulerContinuityResponse, handled bool, err error) {
	continuity := record.plugin.Capabilities.SchedulerContinuity
	if h == nil || continuity == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.SchedulerContinuityResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "SchedulerContinuity.Continuity", recovered)
			resp = pluginapi.SchedulerContinuityResponse{}
			handled = false
			err = nil
		}
	}()

	req.Plugin = record.meta
	resp, errEval := continuity.Continuity(ctx, req)
	if errEval != nil {
		log.WithField("plugin_id", record.id).WithError(errEval).Warn("pluginhost: scheduler continuity evaluation failed")
		return pluginapi.SchedulerContinuityResponse{}, true, errEval
	}
	resp.Decision = pluginapi.SchedulerContinuityDecision(strings.ToLower(strings.TrimSpace(string(resp.Decision))))
	resp.Reason = strings.TrimSpace(resp.Reason)
	return resp, true, nil
}

func (h *Host) schedulerRecord() *capabilityRecord {
	if h == nil {
		return nil
	}
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.Scheduler == nil {
			continue
		}
		copyRecord := record
		return &copyRecord
	}
	return nil
}

func (h *Host) schedulerRecords() []capabilityRecord {
	if h == nil {
		return nil
	}
	active := h.activeRecords()
	if len(active) == 0 {
		return nil
	}
	out := make([]capabilityRecord, 0, len(active))
	for _, record := range active {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.Scheduler == nil {
			continue
		}
		out = append(out, record)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (h *Host) callScheduler(ctx context.Context, record capabilityRecord, req pluginapi.SchedulerPickRequest) (resp pluginapi.SchedulerPickResponse, handled bool, err error) {
	scheduler := record.plugin.Capabilities.Scheduler
	if h == nil || scheduler == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "Scheduler.Pick", recovered)
			resp = pluginapi.SchedulerPickResponse{}
			handled = false
			err = nil
		}
	}()

	req.Plugin = clonePluginMetadata(record.meta)
	resp, errPick := scheduler.Pick(ctx, req)
	if errPick != nil {
		log.WithField("plugin_id", record.id).WithError(errPick).Warn("pluginhost: scheduler rejected auth pick")
		return pluginapi.SchedulerPickResponse{}, true, errPick
	}
	return resp, true, nil
}

func normalizeSchedulerResponse(resp pluginapi.SchedulerPickResponse, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, string) {
	resp.AuthID = strings.TrimSpace(resp.AuthID)
	resp.DelegateBuiltin = strings.TrimSpace(resp.DelegateBuiltin)
	resp.RejectCode = strings.TrimSpace(resp.RejectCode)
	resp.RejectReason = strings.TrimSpace(resp.RejectReason)
	resp.ExcludeCandidates = normalizeExcludedCandidates(resp.ExcludeCandidates)

	if resp.Reject {
		if resp.RejectCode == "" {
			resp.RejectCode = "auth_unavailable"
		}
		if resp.RejectReason == "" {
			resp.RejectReason = "scheduler rejected candidate selection"
		}
		resp.ExcludeCandidates = nil
		return resp, true, ""
	}

	hasAuthID := resp.AuthID != ""
	hasDelegate := resp.DelegateBuiltin != ""
	if !hasAuthID && !hasDelegate {
		return pluginapi.SchedulerPickResponse{}, false, "missing auth id or delegate"
	}
	if hasAuthID {
		if !schedulerCandidateExists(req.Candidates, resp.AuthID) {
			return pluginapi.SchedulerPickResponse{}, false, "unknown auth id"
		}
		resp.ExcludeCandidates = nil
		return resp, true, ""
	}
	if !validSchedulerBuiltin(resp.DelegateBuiltin) {
		return pluginapi.SchedulerPickResponse{}, false, "unknown delegate"
	}
	return resp, true, ""
}

func normalizeExcludedCandidates(candidates []string) []string {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func schedulerCandidateExists(candidates []pluginapi.SchedulerAuthCandidate, authID string) bool {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.ID) == authID {
			return true
		}
	}
	return false
}

func validSchedulerBuiltin(delegate string) bool {
	switch delegate {
	case pluginapi.SchedulerBuiltinRoundRobin, pluginapi.SchedulerBuiltinFillFirst:
		return true
	default:
		return false
	}
}

func appendExcludedCandidates(existing []string, seen map[string]struct{}, additions []string) ([]string, map[string]struct{}) {
	if len(additions) == 0 {
		return existing, seen
	}
	if seen == nil {
		seen = make(map[string]struct{}, len(existing)+len(additions))
		for _, id := range existing {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, len(existing), len(existing)+len(additions))
	copy(out, existing)
	for _, id := range additions {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, seen
}

func filterSchedulerCandidates(candidates []pluginapi.SchedulerAuthCandidate, excluded map[string]struct{}) []pluginapi.SchedulerAuthCandidate {
	if len(candidates) == 0 || len(excluded) == 0 {
		return candidates
	}
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, skip := excluded[strings.TrimSpace(candidate.ID)]; skip {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func highestPrioritySchedulerCandidates(candidates []pluginapi.SchedulerAuthCandidate) []pluginapi.SchedulerAuthCandidate {
	if len(candidates) <= 1 {
		return candidates
	}
	bestPriority := candidates[0].Priority
	bestCount := 1
	for _, candidate := range candidates[1:] {
		switch {
		case candidate.Priority > bestPriority:
			bestPriority = candidate.Priority
			bestCount = 1
		case candidate.Priority == bestPriority:
			bestCount++
		}
	}
	if bestCount == len(candidates) {
		return candidates
	}
	highest := make([]pluginapi.SchedulerAuthCandidate, 0, bestCount)
	for _, candidate := range candidates {
		if candidate.Priority == bestPriority {
			highest = append(highest, candidate)
		}
	}
	return highest
}

func cloneSchedulerPickRequest(req pluginapi.SchedulerPickRequest, candidates []pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerPickRequest {
	req.Providers = cloneStringSlice(req.Providers)
	req.Options = pluginapi.SchedulerOptions{
		Headers:  cloneSchedulerHeaders(req.Options.Headers),
		Metadata: cloneInterceptorMetadata(req.Options.Metadata),
	}
	req.Candidates = cloneSchedulerAuthCandidates(candidates)
	return req
}

func cloneSchedulerHeaders(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = cloneStringSlice(values)
	}
	return out
}

func cloneSchedulerAuthCandidates(in []pluginapi.SchedulerAuthCandidate) []pluginapi.SchedulerAuthCandidate {
	if len(in) == 0 {
		return nil
	}
	out := make([]pluginapi.SchedulerAuthCandidate, len(in))
	for i, candidate := range in {
		candidate.Attributes = cloneStringMap(candidate.Attributes)
		candidate.Metadata = cloneInterceptorMetadata(candidate.Metadata)
		out[i] = candidate
	}
	return out
}
