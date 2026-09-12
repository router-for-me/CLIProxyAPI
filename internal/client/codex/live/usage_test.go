package live

import (
	"bytes"
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"io"
	"strings"
	"testing"
)

func TestLiveUsageTerminalDimensions(t *testing.T) {
	d, ok := liveUsageDetail([]byte(`{"type":"response.done","response":{"usage":{"input_tokens":100,"output_tokens":20,"input_token_details":{"audio_tokens":60,"text_tokens":40},"output_token_details":{"audio_tokens":20}}}}`))
	if !ok || !d.UsageObserved || d.InputTokens != 100 || d.OutputTokens != 20 || d.RawUsage == "" {
		t.Fatalf("lost live dimensions: %+v", d)
	}
	if _, ok := liveUsageDetail([]byte(`{"type":"response.audio.delta","usage":{"input_tokens":100}}`)); ok {
		t.Fatal("nonterminal counted")
	}
}
func TestLiveUsageCaptureDoesNotLimitForwarding(t *testing.T) {
	b := &usageFrameCapture{}
	n, err := b.Write(make([]byte, 2<<20))
	if n != 2<<20 || err != nil || !b.overflow || len(b.data) != 0 {
		t.Fatal("capture must be bounded without limiting stream")
	}
}

type disconnectedUsageWriter struct{}

func (disconnectedUsageWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestUsageFrameSurvivesDownstreamWriteFailure(t *testing.T) {
	payload := `{"type":"response.done","response":{"usage":{"input_tokens":100,"output_tokens":20}}}`
	observed := ""
	err := forwardUsageFrame(disconnectedUsageWriter{}, strings.NewReader(payload), func(b []byte) { observed = string(b) })
	if err != io.ErrClosedPipe {
		t.Fatalf("lost forwarding failure: %v", err)
	}
	if observed != payload {
		t.Fatal("provider usage lost after downstream disconnect")
	}
}

type liveReviewSink struct {
	authID  string
	records chan usage.Record
}

func (s *liveReviewSink) Synchronous() bool { return true }
func (s *liveReviewSink) HandleUsage(_ context.Context, r usage.Record) {
	if r.AuthID == s.authID {
		select {
		case s.records <- r:
		default:
		}
	}
}
func TestTranscriptionUsesItsOwnModelAndContentIdentity(t *testing.T) {
	sink := &liveReviewSink{authID: t.Name(), records: make(chan usage.Record, 10)}
	usage.RegisterPlugin(sink)
	o := newLiveUsageObserver(context.Background(), &auth.Auth{ID: t.Name()}, "gpt-realtime")
	o.observe([]byte(`{"type":"session.updated","session":{"model":"gpt-realtime","audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}}}}`))
	for _, payload := range []string{
		`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item1","content_index":0,"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`,
		`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item1","content_index":1,"usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}`,
	} {
		o.observe([]byte(payload))
		o.observe([]byte(payload))
	}
	if len(sink.records) != 2 {
		t.Fatalf("want two content events without duplicate terminals, got %d", len(sink.records))
	}
	for i := 0; i < 2; i++ {
		r := <-sink.records
		if r.Model != "gpt-4o-transcribe" || r.Kind != "tool" {
			t.Fatalf("transcription attributed to session: model=%s kind=%s", r.Model, r.Kind)
		}
	}
}

func TestOversizedTerminalUsageFramePreservesAccounting(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, disconnected := range []bool{false, true} {
			name := "terminal_only"
			if pending {
				name = "pending"
			}
			if disconnected {
				name += "_disconnected"
			}
			t.Run(name, func(t *testing.T) {
				sink := &liveReviewSink{authID: t.Name(), records: make(chan usage.Record, 10)}
				usage.RegisterPlugin(sink)
				o := newLiveUsageObserver(context.Background(), &auth.Auth{ID: t.Name()}, "gpt-realtime")
				if pending {
					o.observe([]byte(`{"type":"response.created","response":{"id":"large-response","model":"gpt-realtime"}}`))
				}
				payload := `{"response":{"output":[{"type":"message","content":[{"text":"` + strings.Repeat(`large escaped \" text `, 100000) + `"}]},{"type":"web_search_call"},{"type":"web_search_call"}],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"audio_tokens":60,"text_tokens":40}},"id":"large-response","model":"gpt-realtime","status":"completed"},"type":"response.done"}`
				var forwarded bytes.Buffer
				var writer io.Writer = &forwarded
				if disconnected {
					writer = disconnectedUsageWriter{}
				}
				err := forwardUsageFrame(writer, strings.NewReader(payload), o.observe)
				if disconnected && err != io.ErrClosedPipe || !disconnected && err != nil {
					t.Fatalf("forward error: %v", err)
				}
				if !disconnected && forwarded.String() != payload {
					t.Fatal("forwarded frame was changed")
				}
				o.close()
				if len(sink.records) != 1 {
					t.Fatalf("want one record, got %d", len(sink.records))
				}
				r := <-sink.records
				if r.Failed || !r.Detail.UsageObserved || r.Detail.InputTokens != 100 || r.Detail.OutputTokens != 20 || r.Model != "gpt-realtime" {
					t.Fatalf("lost terminal accounting: %+v", r)
				}
				if !strings.Contains(r.Detail.RawUsage, `"web_search_calls":2`) || !strings.Contains(r.Detail.RawUsage, `"audio_tokens":60`) {
					t.Fatalf("lost billing dimensions: %s", r.Detail.RawUsage)
				}
			})
		}
	}
}

func TestOversizedTranscriptionFramePreservesAccounting(t *testing.T) {
	sink := &liveReviewSink{authID: t.Name(), records: make(chan usage.Record, 10)}
	usage.RegisterPlugin(sink)
	o := newLiveUsageObserver(context.Background(), &auth.Auth{ID: t.Name()}, "gpt-realtime")
	o.observe([]byte(`{"type":"session.updated","session":{"audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}}}}`))
	payload := `{"transcript":"` + strings.Repeat("text ", 300000) + `","type":"conversation.item.input_audio_transcription.completed","item_id":"item1","content_index":2,"usage":{"input_tokens":40,"output_tokens":5,"total_tokens":45}}`
	if err := forwardUsageFrame(io.Discard, strings.NewReader(payload), o.observe); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 1 {
		t.Fatalf("want transcription record, got %d", len(sink.records))
	}
	r := <-sink.records
	if r.Failed || r.Model != "gpt-4o-transcribe" || r.Kind != "tool" || r.Detail.TotalTokens != 45 {
		t.Fatalf("lost transcription accounting: %+v", r)
	}
}

func TestLiveBillingWithoutTokensRemainsUnobserved(t *testing.T) {
	d, ok := liveUsageDetail([]byte(`{"type":"response.done","response":{"id":"tool-only","tool_usage":{"web_search_calls":2}}}`))
	if !ok || d.UsageObserved || !strings.Contains(d.RawUsage, `"web_search_calls":2`) {
		t.Fatalf("lost unobserved billing metadata: %+v ok=%v", d, ok)
	}
}

func TestUsageFrameMalformedContentDoesNotInterruptForwarding(t *testing.T) {
	payload := `{"type":"response.done","response":{"usage":{"input_tokens":1}}} trailing`
	var forwarded bytes.Buffer
	observed := false
	if err := forwardUsageFrame(&forwarded, strings.NewReader(payload), func([]byte) { observed = true }); err != nil {
		t.Fatal(err)
	}
	if forwarded.String() != payload || observed {
		t.Fatalf("forwarded changed or malformed event observed: observed=%v", observed)
	}
}

type usageReadFailure struct{}

func (usageReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestUsageFrameReadFailureDoesNotPublishTerminal(t *testing.T) {
	payload := `{"type":"response.done","response":{"id":"partial","usage":{"input_tokens":1}}}`
	observed := false
	err := forwardUsageFrame(io.Discard, io.MultiReader(strings.NewReader(payload), usageReadFailure{}), func([]byte) { observed = true })
	if err != io.ErrUnexpectedEOF || observed {
		t.Fatalf("incomplete frame error=%v observed=%v", err, observed)
	}
}
