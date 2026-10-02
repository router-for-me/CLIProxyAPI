package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"golang.org/x/net/context"
)

type responsesPluginRouteHost struct {
	executeCalls int
	streamCalls  int
	lastStream   bool
	routeStream  bool
	execPayload  []byte
}

func (h *responsesPluginRouteHost) HasModelRouters() bool { return true }

func (h *responsesPluginRouteHost) RouteModel(_ context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	h.routeStream = req.Stream
	return pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetExecutor,
		Target:     "commandcode",
	}, true
}

func (h *responsesPluginRouteHost) ExecutePluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	h.executeCalls++
	payload := h.execPayload
	if len(payload) == 0 {
		payload = []byte(`{"id":"resp_1","object":"response"}`)
	}
	return coreexecutor.Response{Payload: payload}, nil
}

func (h *responsesPluginRouteHost) ExecutePluginExecutorStream(_ context.Context, _ string, _ coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	h.streamCalls++
	h.lastStream = opts.Stream
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (h *responsesPluginRouteHost) CountPluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func newResponsesPluginRouteContext(t *testing.T, body string) (*OpenAIResponsesAPIHandler, *responsesPluginRouteHost, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	host := &responsesPluginRouteHost{}
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	base.SetModelRouterHost(host)
	handler := NewOpenAIResponsesAPIHandler(base)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	return handler, host, ctx
}

func TestResponsesStreamTrueReachesPluginExecutorStream(t *testing.T) {
	handler, host, ctx := newResponsesPluginRouteContext(t, `{"model":"commandcode/deepseek/deepseek-v4.1-flash","input":[{"role":"user","type":"message","content":"Write me a poem"}],"stream":true}`)
	handler.Responses(ctx)
	if host.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", host.streamCalls)
	}
	if host.executeCalls != 0 {
		t.Fatalf("non-stream calls = %d, want 0", host.executeCalls)
	}
	if !host.lastStream || !host.routeStream {
		t.Fatalf("stream flags = executor %v route %v, want both true", host.lastStream, host.routeStream)
	}
}

func TestResponsesWithoutStreamParsesPluginUsage(t *testing.T) {
	plugin := &responsesUsageCapture{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin("test-responses-plugin-route-usage", plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin("test-responses-plugin-route-usage", responsesUsageNop{})
	})

	handler, host, ctx := newResponsesPluginRouteContext(t, `{"model":"commandcode/deepseek/deepseek-v4.1-flash","input":[{"role":"user","type":"message","content":"Write me a poem"}]}`)
	host.execPayload = []byte(`{"id":"resp_1","object":"response","service_tier":"default","usage":{"input_tokens":34,"output_tokens":499,"total_tokens":533}}`)
	handler.Responses(ctx)
	if host.executeCalls != 1 || host.streamCalls != 0 {
		t.Fatalf("calls = execute %d stream %d, want 1/0", host.executeCalls, host.streamCalls)
	}
	if host.routeStream {
		t.Fatal("router saw stream=true for a body without stream")
	}

	select {
	case record := <-plugin.records:
		if record.Provider != "commandcode" {
			t.Fatalf("provider = %q, want commandcode", record.Provider)
		}
		if record.Stream {
			t.Fatal("usage record stream = true, want false")
		}
		if record.Detail.InputTokens != 34 || record.Detail.OutputTokens != 499 || record.Detail.TotalTokens != 533 {
			t.Fatalf("usage = %+v, want input=34 output=499 total=533", record.Detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

type responsesUsageCapture struct {
	records chan usage.Record
}

func (p *responsesUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider != "commandcode" {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

type responsesUsageNop struct{}

func (responsesUsageNop) HandleUsage(context.Context, usage.Record) {}
