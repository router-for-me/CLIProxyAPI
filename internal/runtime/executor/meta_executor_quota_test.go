package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Meta sends the subscription usage after response.completed (recorded 2026-10-08).
const metaSubscriptionUsageSSE = "event: response.output_item.done\n" +
	"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"item_0\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}}\n\n" +
	"event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"muse-spark-1.3\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n" +
	"event: response.subscription_usage\n" +
	"data: {\"subscription\":{\"tier\":\"tier-1\",\"weekly\":{\"resets_at\":1791763200,\"used_percent\":5},\"window\":{\"resets_at\":1791465557,\"used_percent\":12,\"window_duration_mins\":300}},\"type\":\"response.subscription_usage\"}\n\n"

func newMetaSubscriptionUsageServer(t *testing.T) *cliproxyauth.Auth {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(metaSubscriptionUsageSSE))
	}))
	t.Cleanup(server.Close)
	return &cliproxyauth.Auth{
		Provider:   "meta",
		Attributes: map[string]string{"api_key": "meta-token", "base_url": server.URL},
	}
}

func assertMetaSubscriptionUsageObserved(t *testing.T, ctx context.Context) {
	t.Helper()
	headers := logging.GetResponseHeaders(ctx)
	if headers.Get(helps.MetaQuotaWindowUsedHeader) != "12" ||
		headers.Get(helps.MetaQuotaWindowMinutesHeader) != "300" ||
		headers.Get(helps.MetaQuotaWeeklyResetHeader) != "1791763200" ||
		headers.Get(helps.MetaQuotaTierHeader) != "tier-1" {
		t.Fatalf("observed response headers = %#v, want the Meta subscription usage", headers)
	}
}

func TestMetaExecutorStreamRecordsAndForwardsSubscriptionUsage(t *testing.T) {
	auth := newMetaSubscriptionUsageServer(t)
	ctx := logging.WithResponseHeadersHolder(context.Background())
	result, err := NewMetaExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var forwarded strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		forwarded.Write(chunk.Payload)
		forwarded.WriteByte('\n')
	}
	// The Muse CLI reads the event after response.completed, so the client must still get it.
	if !strings.Contains(forwarded.String(), `"type":"response.subscription_usage"`) {
		t.Fatalf("forwarded stream lacks the subscription usage event:\n%s", forwarded.String())
	}
	assertMetaSubscriptionUsageObserved(t, ctx)
}

func TestMetaExecutorExecuteRecordsSubscriptionUsage(t *testing.T) {
	auth := newMetaSubscriptionUsageServer(t)
	ctx := logging.WithResponseHeadersHolder(context.Background())
	resp, err := NewMetaExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-spark-1.3",
		Payload: []byte(`{"model":"muse-spark-1.3","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(string(resp.Payload), "resp_1") {
		t.Fatalf("Execute() payload = %s, want the completed response", resp.Payload)
	}
	assertMetaSubscriptionUsageObserved(t, ctx)
}
