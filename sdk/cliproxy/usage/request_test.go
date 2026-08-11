package usage

import (
	"context"
	"testing"
	"time"
)

type requestCapturePlugin struct {
	records []Record
}

func (p *requestCapturePlugin) HandleUsage(_ context.Context, record Record) {
	p.records = append(p.records, record)
}

func TestFinalizeRequestSeparatesAttemptsFromFinalOutcome(t *testing.T) {
	manager := NewManager(8)
	capture := &requestCapturePlugin{}
	manager.RegisterCriticalNamed("test", capture)
	ctx := WithManager(context.Background(), manager)
	ctx = BeginRequest(ctx, Record{Model: "gpt-test", Alias: "client-model", RequestedAt: time.Now()})

	PublishRecord(ctx, Record{
		Provider:     "openai",
		Model:        "gpt-test",
		OutcomeKnown: true,
		Failed:       true,
		Detail:       Detail{InputTokens: 10, OutputTokens: 2},
	})
	PublishRecord(ctx, Record{
		Provider:     "openai",
		Model:        "gpt-test",
		OutcomeKnown: true,
		Detail:       Detail{InputTokens: 12, OutputTokens: 3},
	})
	FinalizeRequest(ctx, false, Failure{})
	FinalizeRequest(ctx, true, Failure{StatusCode: 500})

	if len(capture.records) != 3 {
		t.Fatalf("captured records = %d, want 3", len(capture.records))
	}
	if capture.records[0].ExternalRequest || capture.records[1].ExternalRequest {
		t.Fatal("upstream attempt was marked as external request")
	}
	final := capture.records[2]
	if !final.ExternalRequest || !final.OutcomeKnown || final.Failed {
		t.Fatalf("final record = %#v", final)
	}
	if final.Detail != (Detail{}) {
		t.Fatalf("final record copied token detail: %#v", final.Detail)
	}
}
