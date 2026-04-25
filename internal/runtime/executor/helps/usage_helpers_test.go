package helps

import (
	"context"
	"fmt"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

type usageReporterCapturePlugin struct {
	provider string
	records  chan usage.Record
}

func (p *usageReporterCapturePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider != p.provider {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func installUsageReporterCapturePlugin(t *testing.T) *usageReporterCapturePlugin {
	t.Helper()
	plugin := &usageReporterCapturePlugin{
		provider: fmt.Sprintf("usage-reporter-test-%d", time.Now().UnixNano()),
		records:  make(chan usage.Record, 4),
	}
	usage.RegisterPlugin(plugin)
	return plugin
}

func readUsageReporterRecord(t *testing.T, plugin *usageReporterCapturePlugin) usage.Record {
	t.Helper()
	select {
	case record := <-plugin.records:
		return record
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage record")
	}
	return usage.Record{}
}

func assertNoUsageReporterRecord(t *testing.T, plugin *usageReporterCapturePlugin) {
	t.Helper()
	select {
	case record := <-plugin.records:
		t.Fatalf("unexpected usage record: %+v", record)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

func TestUsageReporterPublishesFailureWithoutDeferral(t *testing.T) {
	plugin := installUsageReporterCapturePlugin(t)
	reporter := NewUsageReporter(context.Background(), plugin.provider, "test-model", nil)

	reporter.PublishFailure(context.Background())

	record := readUsageReporterRecord(t, plugin)
	if !record.Failed {
		t.Fatalf("expected failed usage record, got success: %+v", record)
	}
	if record.Provider != plugin.provider {
		t.Fatalf("provider = %q, want %q", record.Provider, plugin.provider)
	}
}

func TestUsageReporterDefersFailureUntilFlush(t *testing.T) {
	plugin := installUsageReporterCapturePlugin(t)
	ctx, deferred := cliproxyexecutor.WithDeferredFailure(context.Background())
	reporter := NewUsageReporter(ctx, plugin.provider, "test-model", nil)

	reporter.PublishFailure(ctx)
	assertNoUsageReporterRecord(t, plugin)

	deferred.Flush(ctx)
	record := readUsageReporterRecord(t, plugin)
	if !record.Failed {
		t.Fatalf("expected failed usage record, got success: %+v", record)
	}
}

func TestUsageReporterDiscardedDeferredFailureIsNotPublished(t *testing.T) {
	plugin := installUsageReporterCapturePlugin(t)
	ctx, deferred := cliproxyexecutor.WithDeferredFailure(context.Background())
	reporter := NewUsageReporter(ctx, plugin.provider, "test-model", nil)

	reporter.PublishFailure(ctx)
	deferred.Discard()
	deferred.Flush(ctx)

	assertNoUsageReporterRecord(t, plugin)
}

func TestUsageReporterSuccessClosesDeferredFailureScope(t *testing.T) {
	plugin := installUsageReporterCapturePlugin(t)
	ctx, deferred := cliproxyexecutor.WithDeferredFailure(context.Background())
	failureReporter := NewUsageReporter(ctx, plugin.provider, "test-model", nil)
	successReporter := NewUsageReporter(ctx, plugin.provider, "test-model", nil)

	failureReporter.PublishFailure(ctx)
	successReporter.Publish(ctx, usage.Detail{TotalTokens: 7})
	deferred.Discard()
	deferred.Flush(ctx)

	record := readUsageReporterRecord(t, plugin)
	if record.Failed {
		t.Fatalf("expected successful usage record, got failed: %+v", record)
	}
	if record.Detail.TotalTokens != 7 {
		t.Fatalf("total tokens = %d, want 7", record.Detail.TotalTokens)
	}
	assertNoUsageReporterRecord(t, plugin)
}
