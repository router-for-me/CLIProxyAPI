package pluginhost

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type streamChunkInterceptorSessionRecord struct {
	record          capabilityRecord
	interceptor     pluginapi.StreamChunkInterceptor
	stateful        bool
	rpcMetadataSafe bool
	fuseEpoch       uint64
	initAttempted   bool
	initialized     bool
	disabled        bool
	cleanupPending  bool
	cleanupRequired bool
}

type streamChunkInterceptorSession struct {
	host                    *Host
	callMu                  sync.Mutex
	stateMu                 sync.Mutex
	records                 []streamChunkInterceptorSessionRecord
	releases                []func()
	skipID                  string
	closed                  bool
	omitHeavyFieldsNextCall bool
}

// OpenStreamChunkInterceptorSession captures the currently active stream
// interceptors. Stateful RPC interceptors are pinned for the stream lifetime;
// legacy RPC interceptors retain their existing cancellation and reload behavior.
func (h *Host) OpenStreamChunkInterceptorSession(skipPluginID string) pluginapi.StreamChunkInterceptorSession {
	if h == nil {
		return nil
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	h.mu.Lock()
	defer h.mu.Unlock()

	raw := h.snapshot.Load()
	snap, _ := raw.(*Snapshot)
	if snap == nil {
		return nil
	}
	records := make([]streamChunkInterceptorSessionRecord, 0, len(snap.records))
	releases := make([]func(), 0, len(snap.records))
	hasLegacy := false
	for _, record := range snap.records {
		if record.id == skipPluginID || !h.pluginIdentityCurrentLocked(record.id, record.path, record.version) {
			continue
		}
		if _, fused := h.fused[record.id]; fused {
			continue
		}
		fuseEpoch := h.fusedIdentities[makePluginIdentityKey(record.id, record.path, record.version)]
		if record.plugin.Capabilities.StreamChunkInterceptor == nil {
			continue
		}
		interceptor := record.plugin.Capabilities.StreamChunkInterceptor
		stateful := record.plugin.Capabilities.StreamChunkInterceptorStateful
		if !stateful {
			hasLegacy = true
			continue
		}
		rpcMetadataSafe := false
		if adapter, okAdapter := interceptor.(*rpcPluginAdapter); okAdapter {
			client, okClient := adapter.client.(*guardedPluginClient)
			if !okClient {
				continue
			}
			rpcMetadataSafe = true
			lease, errPin := client.Pin()
			if errPin != nil {
				continue
			}
			pinnedAdapter := *adapter
			pinnedAdapter.client = lease
			interceptor = &pinnedAdapter
			releases = append(releases, lease.Shutdown)
		}
		records = append(records, streamChunkInterceptorSessionRecord{
			record:          record,
			interceptor:     interceptor,
			stateful:        stateful,
			rpcMetadataSafe: rpcMetadataSafe,
			fuseEpoch:       fuseEpoch,
		})
	}
	if len(records) == 0 && !hasLegacy {
		for _, release := range releases {
			release()
		}
		return nil
	}
	return &streamChunkInterceptorSession{host: h, records: records, releases: releases, skipID: skipPluginID}
}

func (s *streamChunkInterceptorSession) CanOmitHeavyFields() bool {
	if s == nil {
		return false
	}
	s.callMu.Lock()
	defer s.callMu.Unlock()
	s.omitHeavyFieldsNextCall = false
	if s.isClosed() {
		return false
	}
	for i := range s.records {
		if s.records[i].disabled || !s.records[i].stateful || !s.records[i].initialized {
			return false
		}
	}
	if len(s.legacyRecords()) > 0 {
		return false
	}
	s.omitHeavyFieldsNextCall = len(s.records) > 0
	return s.omitHeavyFieldsNextCall
}

func (s *streamChunkInterceptorSession) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	passthrough := pluginapi.StreamChunkInterceptResponse{
		Headers: cloneHeader(req.ResponseHeaders),
		Body:    bytes.Clone(req.Body),
	}
	if s == nil || s.host == nil || s.isClosed() {
		return passthrough
	}
	s.callMu.Lock()
	defer s.callMu.Unlock()
	if s.isClosed() {
		return passthrough
	}
	includeLegacy := !s.omitHeavyFieldsNextCall
	s.omitHeavyFieldsNextCall = false

	current := pluginapi.StreamChunkInterceptResponse{
		Headers: cloneHeader(req.ResponseHeaders),
		Body:    bytes.Clone(req.Body),
	}
	for _, state := range s.callRecords(includeLegacy) {
		if s.isClosed() {
			return passthrough
		}
		if state.stateful && !state.disabled && s.host.isPinnedStreamInterceptorFused(state.record, state.fuseEpoch) {
			state.disabled = true
			if state.stateful && state.cleanupRequired {
				state.cleanupPending = true
			}
		}
		if state.disabled {
			if req.ChunkIndex != pluginapi.StreamChunkEndIndex || !state.cleanupPending {
				continue
			}
			state.cleanupPending = false
		}
		if req.ChunkIndex == pluginapi.StreamChunkEndIndex {
			if !state.stateful || !state.initAttempted {
				continue
			}
		} else if req.ChunkIndex >= 0 && current.DropChunk {
			continue
		}

		nextReq := req
		nextReq.RequestHeaders = cloneHeader(req.RequestHeaders)
		nextReq.ResponseHeaders = cloneHeader(current.Headers)
		nextReq.OriginalRequest = bytes.Clone(req.OriginalRequest)
		nextReq.RequestBody = bytes.Clone(req.RequestBody)
		nextReq.Body = bytes.Clone(current.Body)
		nextReq.HistoryChunks = cloneByteSlices(req.HistoryChunks)
		if state.rpcMetadataSafe {
			nextReq.Metadata = req.Metadata
		} else {
			nextReq.Metadata = cloneInterceptorMetadata(req.Metadata)
		}
		if !state.stateful {
			nextReq.StreamID = ""
		}
		if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex && state.stateful {
			state.initAttempted = true
		}

		wasInitialized := state.initialized
		var resp pluginapi.StreamChunkInterceptResponse
		var ok, panicked bool
		if state.stateful {
			resp, ok, panicked = s.host.callSessionStreamChunkInterceptor(ctx, state.record, state.interceptor, nextReq)
		} else {
			resp, ok = s.host.callStreamChunkInterceptor(ctx, state.record, state.interceptor, nextReq)
		}
		if s.isClosed() {
			return passthrough
		}
		if panicked {
			if req.ChunkIndex != pluginapi.StreamChunkEndIndex && state.stateful && (wasInitialized || state.cleanupRequired) {
				state.cleanupPending = true
			}
			state.disabled = true
		}
		if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex && state.stateful {
			state.initialized = ok
			if ok {
				state.cleanupRequired = true
			}
		}
		if !ok {
			continue
		}
		current.Headers = mergeHeaders(current.Headers, resp.Headers, resp.ClearHeaders)
		if len(resp.Body) > 0 {
			current.Body = bytes.Clone(resp.Body)
		}
		if resp.DropChunk && req.ChunkIndex >= 0 {
			current.DropChunk = true
		}
	}
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return passthrough
	}
	s.stateMu.Unlock()
	return current
}

func (s *streamChunkInterceptorSession) callRecords(includeLegacy bool) []*streamChunkInterceptorSessionRecord {
	if s == nil {
		return nil
	}
	records := make([]*streamChunkInterceptorSessionRecord, 0, len(s.records))
	for i := range s.records {
		records = append(records, &s.records[i])
	}
	if includeLegacy {
		legacy := s.legacyRecords()
		for i := range legacy {
			records = append(records, &legacy[i])
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].record.priority == records[j].record.priority {
			return records[i].record.id < records[j].record.id
		}
		return records[i].record.priority > records[j].record.priority
	})
	return records
}

func (s *streamChunkInterceptorSession) legacyRecords() []streamChunkInterceptorSessionRecord {
	if s == nil || s.host == nil {
		return nil
	}
	pinnedIDs := make(map[string]struct{}, len(s.records))
	for i := range s.records {
		pinnedIDs[s.records[i].record.id] = struct{}{}
	}
	records := make([]streamChunkInterceptorSessionRecord, 0)
	for _, record := range s.host.activeRecords() {
		interceptor := record.plugin.Capabilities.StreamChunkInterceptor
		if record.id == s.skipID || interceptor == nil || record.plugin.Capabilities.StreamChunkInterceptorStateful || s.host.isPluginFused(record.id) {
			continue
		}
		if _, pinned := pinnedIDs[record.id]; pinned {
			continue
		}
		_, rpcMetadataSafe := interceptor.(*rpcPluginAdapter)
		records = append(records, streamChunkInterceptorSessionRecord{
			record:          record,
			interceptor:     interceptor,
			rpcMetadataSafe: rpcMetadataSafe,
		})
	}
	return records
}

func (s *streamChunkInterceptorSession) Close() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return
	}
	s.closed = true
	releases := s.releases
	s.releases = nil
	s.stateMu.Unlock()
	if len(releases) == 0 {
		return
	}
	go func() {
		for _, release := range releases {
			release()
		}
	}()
}

func (s *streamChunkInterceptorSession) isClosed() bool {
	s.stateMu.Lock()
	closed := s.closed
	s.stateMu.Unlock()
	return closed
}

func (h *Host) callSessionStreamChunkInterceptor(ctx context.Context, record capabilityRecord, interceptor pluginapi.StreamChunkInterceptor, req pluginapi.StreamChunkInterceptRequest) (out pluginapi.StreamChunkInterceptResponse, ok bool, panicked bool) {
	if h == nil || interceptor == nil {
		return pluginapi.StreamChunkInterceptResponse{}, false, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record, "StreamChunkInterceptor.InterceptStreamChunk", recovered)
			out = pluginapi.StreamChunkInterceptResponse{}
			ok = false
			panicked = true
		}
	}()
	resp, errIntercept := interceptor.InterceptStreamChunk(ctx, req)
	if errIntercept != nil {
		log.Warnf("pluginhost: stream chunk interceptor session %s failed: %v", record.id, errIntercept)
		return pluginapi.StreamChunkInterceptResponse{}, false, false
	}
	return resp, true, false
}

func (h *Host) isPinnedStreamInterceptorFused(record capabilityRecord, openedAtFuseEpoch uint64) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pluginIdentityCurrentLocked(record.id, record.path, record.version) {
		if _, fused := h.fused[record.id]; fused {
			return true
		}
	}
	return h.fusedIdentities[makePluginIdentityKey(record.id, record.path, record.version)] > openedAtFuseEpoch
}
