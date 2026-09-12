package live

import (
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
