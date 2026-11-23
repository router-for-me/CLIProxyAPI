package pluginhost

import (
	"context"
	"errors"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// ErrUsageDeliveryPending leaves ownership with the external sender while the
// plugin is being replaced or replaying local usage. No external record is queued.
var ErrUsageDeliveryPending = errors.New("external usage delivery pending")

// ErrUsageDeliveryUnavailable reports a consumer lost before delivery completed.
var ErrUsageDeliveryUnavailable = errors.New("external usage consumer unavailable")

// HasUsagePlugins reports an advertised usage consumer, including fused plugins.
// A fused consumer or stale snapshot must not turn required delivery into a no-op.
func (h *Host) HasUsagePlugins() bool {
	if h == nil {
		return false
	}
	for _, record := range h.Snapshot().records {
		if record.plugin.Capabilities.UsagePlugin != nil {
			return true
		}
	}
	return false
}

// HandleUsage confirms synchronous external delivery. Unlike the local usage
// adapter, it never transfers external ownership to an in-memory replay queue.
// A non-nil error requires retry; consumers may see the same execution again
// if an earlier consumer completed before a later consumer failed.
func (h *Host) HandleUsage(ctx context.Context, usage pluginapi.UsageRecord) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	for _, record := range h.Snapshot().records {
		if record.plugin.Capabilities.UsagePlugin != nil {
			delivery := usage
			delivery.ResponseHeaders = cloneHeader(usage.ResponseHeaders)
			delivery.Detail.CacheCreation5mTokens = coreusage.CloneInt64Ptr(usage.Detail.CacheCreation5mTokens)
			delivery.Detail.CacheCreation1hTokens = coreusage.CloneInt64Ptr(usage.Detail.CacheCreation1hTokens)
			delivery.Detail.TextInputTokens = coreusage.CloneInt64Ptr(usage.Detail.TextInputTokens)
			delivery.Detail.ImageInputTokens = coreusage.CloneInt64Ptr(usage.Detail.ImageInputTokens)
			delivery.Detail.AudioInputTokens = coreusage.CloneInt64Ptr(usage.Detail.AudioInputTokens)
			delivery.Detail.VideoInputTokens = coreusage.CloneInt64Ptr(usage.Detail.VideoInputTokens)
			delivery.Detail.FileInputTokens = coreusage.CloneInt64Ptr(usage.Detail.FileInputTokens)
			delivery.Detail.CachedTextTokens = coreusage.CloneInt64Ptr(usage.Detail.CachedTextTokens)
			delivery.Detail.CachedImageTokens = coreusage.CloneInt64Ptr(usage.Detail.CachedImageTokens)
			delivery.Detail.CachedAudioTokens = coreusage.CloneInt64Ptr(usage.Detail.CachedAudioTokens)
			delivery.Detail.TextOutputTokens = coreusage.CloneInt64Ptr(usage.Detail.TextOutputTokens)
			delivery.Detail.ImageOutputTokens = coreusage.CloneInt64Ptr(usage.Detail.ImageOutputTokens)
			delivery.Detail.AudioOutputTokens = coreusage.CloneInt64Ptr(usage.Detail.AudioOutputTokens)
			if errDelivery := h.deliverExternalUsageRecord(ctx, record.id, delivery); errDelivery != nil {
				return errDelivery
			}
		}
	}
	return nil
}

func (h *Host) deliverExternalUsageRecord(ctx context.Context, pluginID string, record pluginapi.UsageRecord) (err error) {
	state := h.usageStateForPlugin(pluginID)
	if state == nil {
		return ErrUsageDeliveryUnavailable
	}
	state.mu.Lock()
	if state.closed || state.quiescing || state.replaying || state.asyncDraining || len(state.pending) > 0 {
		state.mu.Unlock()
		return ErrUsageDeliveryPending
	}
	plugin := h.currentUsagePlugin(pluginID)
	if plugin == nil {
		state.mu.Unlock()
		return ErrUsageDeliveryUnavailable
	}
	state.inFlight++
	state.mu.Unlock()
	defer func() {
		// This is the plugin panic isolation boundary, not recovery to success.
		// Unlike local asynchronous delivery, the external owner needs an error.
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "UsagePlugin.HandleUsage", recovered)
			err = ErrUsageDeliveryUnavailable
		}
		state.mu.Lock()
		state.inFlight--
		if state.inFlight == 0 {
			state.cond.Broadcast()
		}
		state.mu.Unlock()
	}()
	if rpcAdapter, ok := plugin.(*rpcPluginAdapter); ok {
		return rpcAdapter.deliverUsage(ctx, record)
	}
	plugin.HandleUsage(ctx, record)
	return nil
}
