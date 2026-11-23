package pluginhost

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestExternalUsageMatchesLocalAdapter(t *testing.T) {
	var received []pluginapi.UsageRecord
	host := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(ctx context.Context, record pluginapi.UsageRecord) {
		if ctx.Err() != nil {
			t.Fatal("delivery inherited request cancellation")
		}
		received = append(received, record)
	})}}})
	if !host.HasUsagePlugins() {
		t.Fatal("active usage plugin not detected")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	(&usageAdapter{host: host, pluginID: "usage"}).HandleUsage(ctx, coreusage.Record{
		RequestID: "execution", TraceID: "trace", SelectionID: "selection", Provider: "codex", AuthID: "auth", AuthIndex: "auth", Model: "gpt", Stream: true,
		RequestedAt: time.Unix(123, 0), Latency: time.Second, Detail: coreusage.Detail{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
	})
	if len(received) != 1 {
		t.Fatalf("local deliveries=%d", len(received))
	}
	if errDelivery := host.HandleUsage(ctx, received[0]); errDelivery != nil {
		t.Fatal(errDelivery)
	}
	if len(received) != 2 || !reflect.DeepEqual(received[0], received[1]) {
		t.Fatalf("deliveries=%+v", received)
	}
	setHostSnapshotForTest(host, true)
	if host.HasUsagePlugins() {
		t.Fatal("unloaded usage plugin remained active")
	}
	if errDelivery := host.HandleUsage(ctx, received[0]); errDelivery != nil {
		t.Fatal(errDelivery)
	}
	if len(received) != 2 {
		t.Fatal("unloaded plugin received feedback")
	}
}

func TestExternalUsageIsolatesPluginOwnedCopies(t *testing.T) {
	count := int64(3)
	record := pluginapi.UsageRecord{ResponseHeaders: http.Header{"X-Test": {"original"}}, Detail: pluginapi.UsageDetail{ImageInputTokens: &count}}
	observed := false
	host := newHostWithRecords(
		capabilityRecord{id: "first", priority: 10, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(_ context.Context, usage pluginapi.UsageRecord) {
			usage.ResponseHeaders.Set("X-Test", "changed")
			*usage.Detail.ImageInputTokens = 99
		})}}},
		capabilityRecord{id: "second", priority: 1, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(_ context.Context, usage pluginapi.UsageRecord) {
			observed = true
			if usage.ResponseHeaders.Get("X-Test") != "original" || *usage.Detail.ImageInputTokens != 3 {
				t.Fatal("one plugin changed another plugin's feedback")
			}
		})}}},
	)
	if errDelivery := host.HandleUsage(t.Context(), record); errDelivery != nil {
		t.Fatal(errDelivery)
	}
	if !observed || count != 3 || record.ResponseHeaders.Get("X-Test") != "original" {
		t.Fatal("external feedback changed its input record")
	}
}
