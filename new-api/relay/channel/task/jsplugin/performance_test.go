package jsplugin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func benchmarkAlibaba(b *testing.B) (*pluginruntime.LoadedPlugin, *atomic.Int64) {
	b.Helper()
	source, err := plugins.Source("alibaba")
	require.NoError(b, err)
	initializations := &atomic.Int64{}
	plugin, err := pluginruntime.CompilePlugin(source+"\n"+`console.log("benchmark_runtime_initialized");`, pluginruntime.Options{
		Log: func(message string) {
			if strings.HasSuffix(message, "benchmark_runtime_initialized") {
				initializations.Add(1)
			}
		},
	})
	require.NoError(b, err)
	return plugin, initializations
}

func BenchmarkTaskPluginRuntime(b *testing.B) {
	for _, name := range []string{"HasExport", "MissingExport", "HookCall", "ParallelHookCall"} {
		b.Run(name, func(b *testing.B) {
			plugin, initializations := benchmarkAlibaba(b)
			ctx := context.Background()
			input := map[string]any{"model": "wan2.5-i2v-preview", "upstreamModel": "wan2.5-i2v-preview", "requestBody": map[string]any{
				"prompt": "animate the landscape", "image": "https://cdn.example/image.png", "duration": 5, "size": "720P",
			}}
			before := initializations.Load()
			b.ReportAllocs()
			if name == "ParallelHookCall" {
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if _, err := plugin.Engine.Call(ctx, "extractUsage", input); err != nil {
							b.Error(err)
							return
						}
					}
				})
			} else {
				for b.Loop() {
					var err error
					switch name {
					case "HasExport":
						_, err = plugin.Engine.HasExport(ctx, "extractUsage")
					case "MissingExport":
						_, err = plugin.Engine.HasExport(ctx, "absentHook")
					default:
						_, err = plugin.Engine.Call(ctx, "extractUsage", input)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			}
			b.ReportMetric(float64(initializations.Load()-before)/float64(b.N), "runtime_inits/op")
		})
	}
}

// Run with -benchtime=50x: GC is excluded so this measures the first hook after
// reclamation, not the runtime.GC operation or an artificial throughput workload.
func BenchmarkTaskPluginAfterGC(b *testing.B) {
	plugin, initializations := benchmarkAlibaba(b)
	input := map[string]any{"model": "wan2.5-i2v-preview", "upstreamModel": "wan2.5-i2v-preview", "requestBody": map[string]any{
		"prompt": "animate the landscape", "image": "https://cdn.example/image.png", "duration": 5, "size": "720P",
	}}
	before := initializations.Load()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		runtime.GC()
		runtime.GC()
		b.StartTimer()
		if _, err := plugin.Engine.Call(b.Context(), "extractUsage", input); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(initializations.Load()-before)/float64(b.N), "runtime_inits/op")
}

func BenchmarkTaskPluginSubmit(b *testing.B) {
	for _, size := range []int{4 << 10, 1 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			plugin, initializations := benchmarkAlibaba(b)
			const modelName = "wan2.5-i2v-preview"
			wire, err := common.Marshal(map[string]any{
				"model": modelName, "duration": 5, "size": "720P",
				"input": []any{map[string]any{"type": "input_text", "text": "animate the landscape"}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("A", size)}},
			})
			require.NoError(b, err)
			before := initializations.Load()
			b.SetBytes(int64(len(wire)))
			b.ReportAllocs()
			for b.Loop() {
				var body any
				if err := common.Unmarshal(wire, &body); err != nil {
					b.Fatal(err)
				}
				protocol := pluginruntime.ProtocolRequestContext{
					RouteRequestContext: pluginruntime.RouteRequestContext{Path: "/v1/responses", Method: http.MethodPost, Body: map[string]any{"kind": "json", "value": body}},
					Protocol:            "openai_responses", Operation: "create", Model: modelName,
				}
				// The middleware decoder runs before channel selection; the adaptor
				// decodes again after the executing provider has been pinned.
				decoded, err := plugin.Engine.CallPath(b.Context(), "protocols", []string{"openai_responses", "decodeRequest"}, protocol.JSValue())
				if err != nil {
					b.Fatal(err)
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				c.Set("task_request", decoded.(map[string]any)["requestBody"])
				c.Set(pluginruntime.ContextKeyRouteRequest, protocol.RouteRequestContext)
				c.Set(pluginruntime.ContextKeyProtocolRequest, protocol)
				c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{Plugin: plugin, Protocol: protocol.Protocol, Model: modelName})
				info := &relaycommon.RelayInfo{OriginModelName: modelName, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: modelName, ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "benchmark-task"}}
				adaptor := New(plugin)
				adaptor.Init(info)
				if taskErr := adaptor.ValidateRequestAndSetAction(c, info); taskErr != nil {
					b.Fatal(taskErr)
				}
				if _, err := adaptor.ExtractUsageFactsValidated(c, info); err != nil {
					b.Fatal(err)
				}
				upstream, err := adaptor.BuildRequestBody(c, info)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, upstream); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(initializations.Load()-before)/float64(b.N), "runtime_inits/op")
		})
	}
}

func BenchmarkTaskPluginSSE(b *testing.B) {
	for _, tc := range []struct {
		name              string
		events, textBytes int
	}{{"small_state", 32, 48}, {"growing_state", 64, 8 << 10}} {
		b.Run(tc.name, func(b *testing.B) {
			plugin, initializations := benchmarkAlibaba(b)
			var stream strings.Builder
			for index := range tc.events {
				choice := map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"text": strings.Repeat("a", tc.textBytes)}}}}
				if index == tc.events-1 {
					choice["finish_reason"] = "stop"
				}
				frame, err := common.Marshal(map[string]any{"request_id": "benchmark-sse", "output": map[string]any{"choices": []any{choice}}, "usage": map[string]any{"image_count": 1}})
				require.NoError(b, err)
				stream.WriteString("data: ")
				stream.Write(frame)
				stream.WriteString("\n\n")
			}
			wire := stream.String()
			input := map[string]any{"model": "wan2.6-image"}
			before := initializations.Load()
			b.SetBytes(int64(len(wire)))
			b.ReportAllocs()
			for b.Loop() {
				response := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
				if _, err := New(plugin).readSubmitEvents(b.Context(), response, input); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(initializations.Load()-before)/float64(b.N), "runtime_inits/op")
		})
	}
}
