package helps

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestApplyPatchTranslationError(t *testing.T) {
	state := &common.ApplyPatchErrorState{}
	want := errors.New("invalid wrapper")
	state.SetToolInputError(want)
	if got := ApplyPatchTranslationError(state); got != want {
		t.Fatalf("translation error = %v, want %v", got, want)
	}
	if ApplyPatchTranslationError(struct{}{}) != nil || ApplyPatchTranslationError(nil) != nil {
		t.Fatal("unrelated translator state was rejected")
	}
}

func TestApplyPatchFinalizeOptionalCanonicalState(t *testing.T) {
	original := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, sdktranslator.FormatGemini, sdktranslator.FormatInteractions} {
		t.Run(source.String(), func(t *testing.T) {
			var param any
			InitializeApplyPatchStream(context.Background(), source, sdktranslator.FormatOpenAIResponse, "model", original, original, &param)
			frames := FinalizeApplyPatchStream(param)
			if len(frames) != 1 || ApplyPatchTranslationError(param) == nil || !bytes.Contains(frames[0], []byte(`"type":"response.failed"`)) {
				t.Fatalf("EOF did not fail canonically: %s", frames)
			}
			if len(FinalizeApplyPatchStream(param)) != 0 {
				t.Fatal("EOF failure emitted twice")
			}
			late := []byte(`data: {"type":"message_stop","event_type":"interaction.completed","candidates":[{"finishReason":"STOP"}],"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			if len(sdktranslator.TranslateStream(context.Background(), source, sdktranslator.FormatOpenAIResponse, "model", original, original, late, &param)) != 0 {
				t.Fatal("EOF failure reopened into success")
			}
			if len(sdktranslator.TranslateStream(context.Background(), source, sdktranslator.FormatOpenAIResponse, "model", original, original, []byte("[DONE]"), &param)) != 0 {
				t.Fatal("failure forwarded transport success")
			}
		})
	}
	if len(FinalizeApplyPatchStream(struct{}{})) != 0 || len(FinalizeApplyPatchStream(nil)) != 0 {
		t.Fatal("unrelated states received an EOF failure")
	}
}

func TestApplyPatchTokenUsageHelperRetainsFailureState(t *testing.T) {
	original := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	var param any
	tokens := NewClaudeInputTokenState(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, original)
	chunks := TranslateStreamWithClaudeInputTokens(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, "model", original, original, []byte(`data: {"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"apply_patch","arguments":"{\"input\":7}"}}]}}]}`), &param, tokens)
	if ApplyPatchTranslationError(param) == nil {
		t.Fatal("token helper discarded the same retained error state")
	}
	failed := 0
	for _, chunk := range chunks {
		if bytes.Contains(chunk, []byte(`"type":"response.failed"`)) {
			failed++
			if bytes.Contains(chunk, []byte(`"usage"`)) {
				t.Fatal("usage normalization altered a failure frame")
			}
		}
	}
	if failed != 1 {
		t.Fatalf("failure frames=%d", failed)
	}
}

func TestApplyPatchInteractionsOnlyValidSourceDoneClosesTransport(t *testing.T) {
	var param any
	translate := func(payload string) [][]byte {
		return sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatInteractions, sdktranslator.FormatOpenAIResponse, "model", nil, nil, []byte(payload), &param)
	}
	_ = translate(`{"event_type":"interaction.completed","interaction":{"id":"r"}}`)
	if chunks := translate(`{"event_type":"done",`); len(chunks) != 0 {
		t.Fatalf("malformed post-terminal event was accepted as a sentinel: %s", chunks)
	}
	chunks := translate("[DONE]")
	if len(chunks) != 1 || !bytes.Equal(chunks[0], []byte("data: [DONE]")) {
		t.Fatalf("first real source sentinel was swallowed: %s", chunks)
	}
	if len(translate("[DONE]")) != 0 || len(translate(`{"event_type":"done"}`)) != 0 {
		t.Fatal("duplicate source sentinel was forwarded")
	}
}

type patchCanceledEOFCapture struct{ records chan usage.Record }

func (p *patchCanceledEOFCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider == "task6-canceled-eof" {
		p.records <- record
	}
}

func TestApplyPatchCanceledEOFStillRecordsFailure(t *testing.T) {
	capture := &patchCanceledEOFCapture{records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin("task6-canceled-eof", capture)
	var param any
	original := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	InitializeApplyPatchStream(context.Background(), sdktranslator.FormatGemini, sdktranslator.FormatOpenAIResponse, "m", original, original, &param)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reporter := NewUsageReporter(ctx, "task6-canceled-eof", "m", nil)
	out := make(chan cliproxyexecutor.StreamChunk)
	if !EndApplyPatchStream(ctx, param, reporter, out, errors.New("clean failure")) {
		t.Fatal("EOF failure was discarded")
	}
	reporter.EnsurePublished(ctx)
	select {
	case record := <-capture.records:
		if !record.Failed {
			t.Fatal("cancellation recovered a retained EOF failure as success usage")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled EOF usage was not dispatched")
	}
}
