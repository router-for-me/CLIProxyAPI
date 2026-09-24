package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type hostHTTPOperationKey struct {
	pluginID    string
	operationID string
}

type hostHTTPOperation struct {
	ctx              context.Context
	cancel           context.CancelFunc
	instance         *hostCallbackInstance
	callbackID       string
	stopCancel       func() bool
	stopScopeCleanup func()
	cleanup          func()
	started          bool
}

type hostHTTPOperationHandle struct {
	ctx         context.Context
	cancel      context.CancelFunc
	finish      func()
	pluginID    string
	instance    *hostCallbackInstance
	operationID string
	entry       *hostHTTPOperation
}

type hostHTTPOperationBridge struct {
	next       atomic.Uint64
	mu         sync.Mutex
	operations map[hostHTTPOperationKey]*hostHTTPOperation
}

func newHostHTTPOperationBridge() *hostHTTPOperationBridge {
	return &hostHTTPOperationBridge{operations: make(map[hostHTTPOperationKey]*hostHTTPOperation)}
}

func (b *hostHTTPOperationBridge) open(pluginID string, instance *hostCallbackInstance, callbackID string, parent context.Context) (string, *hostHTTPOperation, bool) {
	return b.openWithClaimState(pluginID, instance, callbackID, parent, false)
}

func (b *hostHTTPOperationBridge) openClaimed(pluginID string, instance *hostCallbackInstance, callbackID string, parent context.Context) (string, *hostHTTPOperation, bool) {
	return b.openWithClaimState(pluginID, instance, callbackID, parent, true)
}

func (b *hostHTTPOperationBridge) openWithClaimState(pluginID string, instance *hostCallbackInstance, callbackID string, parent context.Context, started bool) (string, *hostHTTPOperation, bool) {
	if b == nil {
		return "", nil, false
	}
	if parent == nil {
		parent = context.Background()
	}
	operationID := strconv.FormatUint(b.next.Add(1), 10)
	operationCtx, cancel := context.WithCancel(parent)
	key := hostHTTPOperationKey{pluginID: strings.TrimSpace(pluginID), operationID: operationID}
	entry := &hostHTTPOperation{ctx: operationCtx, cancel: cancel, instance: instance, callbackID: strings.TrimSpace(callbackID), started: started}
	b.mu.Lock()
	if instance != nil && instance.closed.Load() {
		b.mu.Unlock()
		cancel()
		return "", nil, false
	}
	if _, exists := b.operations[key]; exists {
		b.mu.Unlock()
		cancel()
		return "", nil, false
	}
	b.operations[key] = entry
	entry.stopCancel = context.AfterFunc(operationCtx, func() {
		b.cancel(key.pluginID, instance, key.operationID)
	})
	b.mu.Unlock()
	return operationID, entry, true
}

func (b *hostHTTPOperationBridge) claim(pluginID string, instance *hostCallbackInstance, operationID, callbackID string) (*hostHTTPOperation, bool) {
	if b == nil || strings.TrimSpace(operationID) == "" {
		return nil, false
	}
	key := hostHTTPOperationKey{
		pluginID:    strings.TrimSpace(pluginID),
		operationID: strings.TrimSpace(operationID),
	}
	b.mu.Lock()
	entry, exists := b.operations[key]
	if !exists || entry.started || entry.instance != instance || entry.callbackID != strings.TrimSpace(callbackID) {
		b.mu.Unlock()
		return nil, false
	}
	entry.started = true
	b.mu.Unlock()
	return entry, true
}

func (b *hostHTTPOperationBridge) setCleanup(pluginID, operationID string, entry *hostHTTPOperation, cleanup func()) bool {
	if cleanup == nil {
		return false
	}
	if b == nil {
		cleanup()
		return false
	}
	key := hostHTTPOperationKey{
		pluginID:    strings.TrimSpace(pluginID),
		operationID: strings.TrimSpace(operationID),
	}
	b.mu.Lock()
	current, exists := b.operations[key]
	if exists && current == entry && current.cleanup == nil && entry.ctx.Err() == nil {
		current.cleanup = cleanup
		b.mu.Unlock()
		return true
	}
	b.mu.Unlock()
	cleanup()
	return false
}

func (b *hostHTTPOperationBridge) setScopeCleanup(pluginID, operationID string, entry *hostHTTPOperation, stop func()) bool {
	if b == nil || entry == nil || stop == nil {
		if stop != nil {
			stop()
		}
		return false
	}
	key := hostHTTPOperationKey{
		pluginID:    strings.TrimSpace(pluginID),
		operationID: strings.TrimSpace(operationID),
	}
	b.mu.Lock()
	if b.operations[key] == entry && entry.stopScopeCleanup == nil {
		entry.stopScopeCleanup = stop
		b.mu.Unlock()
		return true
	}
	b.mu.Unlock()
	stop()
	return false
}

func (b *hostHTTPOperationBridge) finish(pluginID, operationID string, entry *hostHTTPOperation) {
	var stopCancel func() bool
	var stopScopeCleanup func()
	if b != nil && entry != nil && strings.TrimSpace(operationID) != "" {
		key := hostHTTPOperationKey{
			pluginID:    strings.TrimSpace(pluginID),
			operationID: strings.TrimSpace(operationID),
		}
		b.mu.Lock()
		if b.operations[key] == entry {
			delete(b.operations, key)
		}
		stopCancel = entry.stopCancel
		stopScopeCleanup = entry.stopScopeCleanup
		b.mu.Unlock()
	}
	if stopCancel != nil {
		stopCancel()
	}
	if stopScopeCleanup != nil {
		stopScopeCleanup()
	}
	if entry != nil && entry.cancel != nil {
		entry.cancel()
	}
}

func (b *hostHTTPOperationBridge) cancel(pluginID string, instance *hostCallbackInstance, operationID string) {
	if b == nil || strings.TrimSpace(operationID) == "" {
		return
	}
	key := hostHTTPOperationKey{
		pluginID:    strings.TrimSpace(pluginID),
		operationID: strings.TrimSpace(operationID),
	}
	b.mu.Lock()
	entry := b.operations[key]
	if entry != nil && entry.instance == instance {
		delete(b.operations, key)
	} else {
		entry = nil
	}
	b.mu.Unlock()
	cancelHostHTTPOperation(entry)
}

func (b *hostHTTPOperationBridge) closeInstance(pluginID string, instance *hostCallbackInstance) {
	if instance == nil {
		b.cancelPlugin(pluginID)
		return
	}
	instance.closed.Store(true)
	if b == nil {
		return
	}
	b.cancelMatching(pluginID, instance, false)
}

func (b *hostHTTPOperationBridge) cancelPlugin(pluginID string) {
	if b == nil || strings.TrimSpace(pluginID) == "" {
		return
	}
	b.cancelMatching(pluginID, nil, false)
}

func (b *hostHTTPOperationBridge) cancelAll() {
	if b == nil {
		return
	}
	b.cancelMatching("", nil, true)
}

func (b *hostHTTPOperationBridge) cancelMatching(pluginID string, instance *hostCallbackInstance, all bool) {
	pluginID = strings.TrimSpace(pluginID)
	b.mu.Lock()
	entries := make([]*hostHTTPOperation, 0)
	for key, entry := range b.operations {
		if !all && (key.pluginID != pluginID || (instance != nil && entry.instance != instance)) {
			continue
		}
		if all && entry.instance != nil {
			entry.instance.closed.Store(true)
		}
		delete(b.operations, key)
		entries = append(entries, entry)
	}
	b.mu.Unlock()
	for _, entry := range entries {
		cancelHostHTTPOperation(entry)
	}
}

func cancelHostHTTPOperation(entry *hostHTTPOperation) {
	if entry == nil {
		return
	}
	if entry.stopCancel != nil {
		entry.stopCancel()
	}
	if entry.stopScopeCleanup != nil {
		entry.stopScopeCleanup()
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.cleanup != nil {
		entry.cleanup()
	}
}

func (h *Host) closeHostHTTPCallbackInstance(pluginID string, instance *hostCallbackInstance) {
	if instance != nil {
		instance.closed.Store(true)
	}
	if h == nil {
		return
	}
	if h.httpOperations != nil {
		h.httpOperations.closeInstance(pluginID, instance)
	}
	if h.httpStreams != nil {
		h.httpStreams.closeInstance(pluginID, instance)
	}
}

func (h *Host) closeHostHTTPPluginResources(pluginID string, instance *hostCallbackInstance) {
	h.closeHostHTTPCallbackInstance(pluginID, instance)
	if h == nil {
		return
	}
	if h.httpOperations != nil {
		h.httpOperations.cancelPlugin(pluginID)
	}
	if h.httpStreams != nil {
		h.httpStreams.closePlugin(pluginID)
	}
}

func (h *Host) openHostHTTPOperation(ctx context.Context, callbackID string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if h == nil || h.httpOperations == nil {
		return "", fmt.Errorf("host http operation bridge is unavailable")
	}
	callbackID = strings.TrimSpace(callbackID)
	pluginID := hostCallbackPluginIDFromContext(ctx)
	instance := hostCallbackInstanceFromContext(ctx)
	parent := ctx
	if callbackID != "" {
		var callbackPluginID string
		var callbackInstance *hostCallbackInstance
		var exists bool
		parent, callbackPluginID, callbackInstance, exists = h.lookupCallbackContext(callbackID)
		if !exists {
			return "", fmt.Errorf("host callback ID is not open")
		}
		if pluginID != "" && callbackPluginID != pluginID {
			return "", fmt.Errorf("host callback ID does not belong to the calling plugin")
		}
		if pluginID == "" {
			pluginID = callbackPluginID
		}
		if instance != callbackInstance && (instance != nil || callbackInstance != nil) {
			return "", fmt.Errorf("host callback ID does not belong to the calling plugin instance")
		}
		if instance == nil {
			instance = callbackInstance
		}
	}
	operationID, _, errOpen := h.createHostHTTPOperation(pluginID, instance, callbackID, parent, false)
	if errOpen != nil {
		return "", errOpen
	}
	return operationID, nil
}

func (h *Host) createHostHTTPOperation(pluginID string, instance *hostCallbackInstance, callbackID string, parent context.Context, claimed bool) (string, *hostHTTPOperation, error) {
	if h == nil || h.httpOperations == nil {
		return "", nil, fmt.Errorf("host http operation bridge is unavailable")
	}
	var operationID string
	var entry *hostHTTPOperation
	var opened bool
	if claimed {
		operationID, entry, opened = h.httpOperations.openClaimed(pluginID, instance, callbackID, parent)
	} else {
		operationID, entry, opened = h.httpOperations.open(pluginID, instance, callbackID, parent)
	}
	if !opened {
		return "", nil, fmt.Errorf("host http operation bridge is unavailable")
	}
	if callbackID != "" {
		stopScopeCleanup, attached := h.addCallbackCleanupHandle(callbackID, func() {
			h.httpOperations.cancel(pluginID, instance, operationID)
		})
		if !attached || !h.httpOperations.setScopeCleanup(pluginID, operationID, entry, stopScopeCleanup) {
			h.httpOperations.cancel(pluginID, instance, operationID)
			return "", nil, fmt.Errorf("host callback context closed while opening HTTP operation")
		}
	}
	return operationID, entry, nil
}

func (h *Host) acquireHostHTTPOperation(ctx context.Context, callbackID, operationID string) (hostHTTPOperationHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationID = strings.TrimSpace(operationID)
	callbackID = strings.TrimSpace(callbackID)
	pluginID := hostCallbackPluginIDFromContext(ctx)
	instance := hostCallbackInstanceFromContext(ctx)
	if operationID == "" {
		parent := ctx
		if callbackID != "" {
			var callbackPluginID string
			var callbackInstance *hostCallbackInstance
			var exists bool
			parent, callbackPluginID, callbackInstance, exists = h.lookupCallbackContext(callbackID)
			if !exists {
				return hostHTTPOperationHandle{}, fmt.Errorf("host callback ID is not open")
			}
			if pluginID != "" && callbackPluginID != pluginID {
				return hostHTTPOperationHandle{}, fmt.Errorf("host callback ID does not belong to the calling plugin")
			}
			if pluginID == "" {
				pluginID = callbackPluginID
			}
			if instance != callbackInstance && (instance != nil || callbackInstance != nil) {
				return hostHTTPOperationHandle{}, fmt.Errorf("host callback ID does not belong to the calling plugin instance")
			}
			if instance == nil {
				instance = callbackInstance
			}
		}
		operationID, entry, errOpen := h.createHostHTTPOperation(pluginID, instance, callbackID, parent, true)
		if errOpen != nil {
			return hostHTTPOperationHandle{}, errOpen
		}
		finish := func() {
			h.httpOperations.finish(pluginID, operationID, entry)
		}
		return hostHTTPOperationHandle{
			ctx:         entry.ctx,
			cancel:      entry.cancel,
			finish:      finish,
			pluginID:    pluginID,
			instance:    instance,
			operationID: operationID,
			entry:       entry,
		}, nil
	}
	if h == nil || h.httpOperations == nil {
		return hostHTTPOperationHandle{}, fmt.Errorf("host http operation bridge is unavailable")
	}
	if pluginID == "" || instance == nil {
		_, callbackPluginID, callbackInstance, _ := h.lookupCallbackContext(callbackID)
		if pluginID == "" {
			pluginID = callbackPluginID
		}
		if instance == nil {
			instance = callbackInstance
		}
	}
	entry, claimed := h.httpOperations.claim(pluginID, instance, operationID, callbackID)
	if !claimed {
		return hostHTTPOperationHandle{}, fmt.Errorf("host http operation %q is not open", operationID)
	}
	finish := func() {
		h.httpOperations.finish(pluginID, operationID, entry)
	}
	return hostHTTPOperationHandle{
		ctx:         entry.ctx,
		cancel:      entry.cancel,
		finish:      finish,
		pluginID:    pluginID,
		operationID: operationID,
		instance:    instance,
		entry:       entry,
	}, nil
}
