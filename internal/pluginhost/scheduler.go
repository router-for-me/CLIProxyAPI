package pluginhost

import (
	"context"
	"errors"
	"strings"
	"time"

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
		resp, subReq, called, errPick := h.pickFromSchedulerRecord(ctx, record, req, remainingCandidates)
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

func (h *Host) pickFromSchedulerRecord(ctx context.Context, record capabilityRecord, req pluginapi.SchedulerPickRequest, remainingCandidates []pluginapi.SchedulerAuthCandidate) (pluginapi.SchedulerPickResponse, pluginapi.SchedulerPickRequest, bool, error) {
	currentRecord := record
	if reloading, _, _ := h.schedulerReloadSnapshot(currentRecord.id); reloading {
		if !h.waitForSchedulerReload(ctx, currentRecord.id) {
			if ctx != nil && ctx.Err() != nil {
				return pluginapi.SchedulerPickResponse{}, pluginapi.SchedulerPickRequest{}, true, ctx.Err()
			}
			return pluginapi.SchedulerPickResponse{}, pluginapi.SchedulerPickRequest{}, false, nil
		}
		refreshed, okRefreshed := h.schedulerRecordByID(currentRecord.id)
		if !okRefreshed {
			return pluginapi.SchedulerPickResponse{}, pluginapi.SchedulerPickRequest{}, false, nil
		}
		currentRecord = refreshed
	}

	_, genBefore, _ := h.schedulerReloadSnapshot(currentRecord.id)
	subReq := buildSchedulerSubRequest(currentRecord, req, remainingCandidates)
	resp, called, errPick := h.callScheduler(ctx, currentRecord, subReq)
	if !h.shouldRetrySchedulerAfterReload(ctx, currentRecord, genBefore, resp, called, errPick, subReq) {
		return resp, subReq, called, errPick
	}

	if !h.waitForSchedulerReload(ctx, currentRecord.id) {
		if ctx != nil && ctx.Err() != nil {
			return pluginapi.SchedulerPickResponse{}, subReq, true, ctx.Err()
		}
		return resp, subReq, called, errPick
	}
	refreshed, okRefreshed := h.schedulerRecordByID(currentRecord.id)
	if !okRefreshed {
		return pluginapi.SchedulerPickResponse{}, subReq, false, nil
	}
	retrySubReq := buildSchedulerSubRequest(refreshed, req, remainingCandidates)
	retryResp, retryCalled, errRetry := h.callScheduler(ctx, refreshed, retrySubReq)
	return retryResp, retrySubReq, retryCalled, errRetry
}

func buildSchedulerSubRequest(record capabilityRecord, req pluginapi.SchedulerPickRequest, remainingCandidates []pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerPickRequest {
	offeredCandidates := remainingCandidates
	if !schedulerWantsAcrossPriorities(record.plugin.Capabilities) {
		offeredCandidates = highestPrioritySchedulerCandidates(remainingCandidates)
	}
	return cloneSchedulerPickRequest(req, offeredCandidates)
}

func (h *Host) shouldRetrySchedulerAfterReload(ctx context.Context, record capabilityRecord, genBefore uint64, resp pluginapi.SchedulerPickResponse, called bool, errPick error, subReq pluginapi.SchedulerPickRequest) bool {
	if h == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	reloading, genAfter, _ := h.schedulerReloadSnapshot(record.id)
	if !reloading && genAfter == genBefore && h.recordCurrent(record) {
		return false
	}
	if errPick != nil || !called || !resp.Handled || resp.Reject {
		return true
	}
	_, valid, _ := normalizeSchedulerResponse(resp, subReq)
	return !valid
}

func (h *Host) schedulerReloadSnapshot(pluginID string) (bool, uint64, <-chan struct{}) {
	if h == nil {
		return false, 0, nil
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return false, 0, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.schedulerReloads[pluginID]
	if state == nil {
		return false, 0, nil
	}
	return state.reloading, state.generation, state.done
}

func (h *Host) waitForSchedulerReload(ctx context.Context, pluginID string) bool {
	reloading, _, done := h.schedulerReloadSnapshot(pluginID)
	if !reloading || done == nil {
		return true
	}
	h.mu.Lock()
	timeout := h.schedulerReloadWaitTimeout
	waitHook := h.schedulerReloadWaitHook
	h.mu.Unlock()
	if timeout <= 0 {
		timeout = defaultSchedulerReloadWaitTimeout
	}
	if waitHook != nil {
		waitHook(pluginID)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	if ctx == nil {
		select {
		case <-done:
			return true
		case <-timer.C:
			return false
		}
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (h *Host) schedulerRecordByID(pluginID string) (capabilityRecord, bool) {
	if h == nil {
		return capabilityRecord{}, false
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return capabilityRecord{}, false
	}
	for _, record := range h.activeRecords() {
		if record.id != pluginID {
			continue
		}
		if h.isPluginFused(record.id) || record.plugin.Capabilities.Scheduler == nil {
			return capabilityRecord{}, false
		}
		return record, true
	}
	return capabilityRecord{}, false
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
	if ctx != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			log.WithField("plugin_id", record.id).WithError(errCtx).Debug("pluginhost: scheduler auth pick canceled")
			return pluginapi.SchedulerPickResponse{}, true, errCtx
		}
	}
	if errPick != nil {
		if errors.Is(errPick, context.Canceled) || errors.Is(errPick, context.DeadlineExceeded) {
			log.WithField("plugin_id", record.id).WithError(errPick).Debug("pluginhost: scheduler auth pick canceled")
		} else {
			log.WithField("plugin_id", record.id).WithError(errPick).Warn("pluginhost: scheduler rejected auth pick")
		}
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
