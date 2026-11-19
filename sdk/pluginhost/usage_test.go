package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestExternalUsagePublicSurface(t *testing.T) {
	var sink interface {
		HasUsagePlugins() bool
		HandleUsage(context.Context, pluginapi.UsageRecord) error
	} = (*Host)(nil)
	if sink.HasUsagePlugins() {
		t.Fatal("nil host has usage plugins")
	}
	if errDelivery := sink.HandleUsage(t.Context(), pluginapi.UsageRecord{}); errDelivery != nil {
		t.Fatal(errDelivery)
	}
	host := New()
	if host.HasUsagePlugins() {
		t.Fatal("empty host has usage plugins")
	}
	if errDelivery := host.HandleUsage(t.Context(), pluginapi.UsageRecord{}); errDelivery != nil {
		t.Fatal(errDelivery)
	}
}
