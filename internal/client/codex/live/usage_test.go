package live

import "testing"

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
