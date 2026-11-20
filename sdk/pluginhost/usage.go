package pluginhost

import (
	"context"

	internalpluginhost "github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// ErrUsageDeliveryPending requires sender retry without relinquishing ownership.
var ErrUsageDeliveryPending = internalpluginhost.ErrUsageDeliveryPending

// ErrUsageDeliveryUnavailable reports that a selected usage consumer failed.
var ErrUsageDeliveryUnavailable = internalpluginhost.ErrUsageDeliveryUnavailable

// HasUsagePlugins reports an advertised usage consumer, even if currently fused.
func (h *Host) HasUsagePlugins() bool {
	return h != nil && h.inner != nil && h.inner.HasUsagePlugins()
}

// HandleUsage returns nil only after synchronous callbacks complete (or no usage
// consumers exist). Pending replacement/replay and callback failures return errors;
// the external sender retains ownership and must retry. No record is queued here.
func (h *Host) HandleUsage(ctx context.Context, record pluginapi.UsageRecord) error {
	if h == nil || h.inner == nil {
		return nil
	}
	return h.inner.HandleUsage(ctx, record)
}
