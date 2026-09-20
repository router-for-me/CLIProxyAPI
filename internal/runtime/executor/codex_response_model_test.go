package executor

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const codexResponseModelTestStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}

data: {"type":"response.output_text.delta","delta":"he"}

data: {"type":"response.output_text.delta","delta":"llo"}

data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-luna","usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}}

data: [DONE]`

// TestObserveCodexTokenEventRecordsResponseModel guards the wiring between the codex
// stream loops and the usage reporter: every transport funnels through observeCodexTokenEvent.
func TestObserveCodexTokenEventRecordsResponseModel(t *testing.T) {
	reporter := helps.NewExecutorUsageReporter(context.Background(), NewCodexExecutor(&config.Config{}), "gpt-6-astra", nil)

	for _, line := range bytes.Split([]byte(codexResponseModelTestStream), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		observeCodexTokenEvent(reporter, bytes.TrimSpace(line[len(dataTag):]))
	}

	if got := reporter.ResponseModel(); got != "gpt-5.6-luna" {
		t.Fatalf("reporter response model = %q, want %q", got, "gpt-5.6-luna")
	}
}

type codexResponseModelUsageCapture struct {
	alias   string
	records chan coreusage.Record
}

func (c *codexResponseModelUsageCapture) HandleUsage(_ context.Context, record coreusage.Record) {
	if record.Alias != c.alias {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type codexResponseModelNoopUsagePlugin struct{}

func (codexResponseModelNoopUsagePlugin) HandleUsage(context.Context, coreusage.Record) {}

func (c *codexResponseModelUsageCapture) await(t *testing.T) coreusage.Record {
	t.Helper()
	select {
	case record := <-c.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the usage record")
		return coreusage.Record{}
	}
}

// TestCodexUsageRecordsCarryResponseModelPerModel checks that the attempt record reports
// the served model while the image generation tool record must not claim it.
func TestCodexUsageRecordsCarryResponseModelPerModel(t *testing.T) {
	const alias = "codex-response-model-wiring-test"
	capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
	coreusage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{})
	})

	ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
	auth := &cliproxyauth.Auth{ID: "codex-auth-1", Index: "auth-index-7", Provider: "codex"}
	reporter := helps.NewExecutorUsageReporter(ctx, NewCodexExecutor(&config.Config{}), "gpt-5.4-mini", auth)

	observeCodexTokenEvent(reporter, []byte(`{"type":"response.completed","response":{"model":"gpt-5.4-mini","usage":{"total_tokens":12}}}`))
	reporter.EnsurePublished(ctx)
	reporter.PublishAdditionalModel(ctx, "gpt-image-1.5", coreusage.Detail{TotalTokens: 5})

	attemptRecord := capture.await(t)
	if attemptRecord.Model != "gpt-5.4-mini" {
		t.Fatalf("attempt record model = %q, want %q", attemptRecord.Model, "gpt-5.4-mini")
	}
	if attemptRecord.ResponseModel != "gpt-5.4-mini" {
		t.Fatalf("attempt record response model = %q, want %q", attemptRecord.ResponseModel, "gpt-5.4-mini")
	}

	imageRecord := capture.await(t)
	if imageRecord.Model != "gpt-image-1.5" {
		t.Fatalf("image record model = %q, want %q", imageRecord.Model, "gpt-image-1.5")
	}
	if imageRecord.ResponseModel != "" {
		t.Fatalf("image record response model = %q, want empty", imageRecord.ResponseModel)
	}
}
