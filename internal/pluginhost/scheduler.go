package pluginhost

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func (h *Host) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	record := h.schedulerRecord()
	if record == nil {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}

	resp, handled, errPick := h.callScheduler(ctx, *record, req)
	if errPick != nil || !handled {
		return resp, handled, errPick
	}
	if !resp.Handled {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}

	resp, valid, reason := normalizeSchedulerResponse(resp, req)
	if !valid {
		log.WithField("plugin_id", record.id).Warnf("pluginhost: scheduler returned invalid response: %s", reason)
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	return resp, true, nil
}

func (h *Host) HasScheduler() bool {
	return h.schedulerRecord() != nil
}

func (h *Host) schedulerRecord() *capabilityRecord {
	if h == nil {
		return nil
	}
	for _, record := range h.Snapshot().records {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.Scheduler == nil {
			continue
		}
		copyRecord := record
		return &copyRecord
	}
	return nil
}

func (h *Host) callScheduler(ctx context.Context, record capabilityRecord, req pluginapi.SchedulerPickRequest) (resp pluginapi.SchedulerPickResponse, handled bool, err error) {
	scheduler := record.plugin.Capabilities.Scheduler
	if h == nil || scheduler == nil || h.isPluginFused(record.id) {
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

	req.Plugin = record.meta
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

	hasAuthID := resp.AuthID != ""
	hasDelegate := resp.DelegateBuiltin != ""
	if !hasAuthID && !hasDelegate {
		return pluginapi.SchedulerPickResponse{}, false, "missing auth id or delegate"
	}
	if hasAuthID {
		if !schedulerCandidateExists(req.Candidates, resp.AuthID) {
			return pluginapi.SchedulerPickResponse{}, false, "unknown auth id"
		}
		return resp, true, ""
	}
	if !validSchedulerBuiltin(resp.DelegateBuiltin) {
		return pluginapi.SchedulerPickResponse{}, false, "unknown delegate"
	}
	return resp, true, ""
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
