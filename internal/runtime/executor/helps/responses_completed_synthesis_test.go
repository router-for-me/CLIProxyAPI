package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestResponsesStreamOutputTracker_SynthesizesUnstreamedRefusal(t *testing.T) {
	var tracker ResponsesStreamOutputTracker
	tracker.Observe("response.created", []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","output":[]}}`))
	tracker.Observe("response.in_progress", []byte(`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_1","output":[]}}`))

	completed := []byte(`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","output":[{"id":"msg_refusal","type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"I'm sorry, but I can't help with that request."}]}]}}`)
	synth, updatedCompleted := tracker.SynthesizeMissingOutputEvents(completed)

	wantTypes := []string{
		"response.output_item.added",
		"response.content_part.added",
		"response.refusal.delta",
		"response.refusal.done",
		"response.content_part.done",
		"response.output_item.done",
	}
	if len(synth) != len(wantTypes) {
		t.Fatalf("synthesized count = %d, want %d; events=%q", len(synth), len(wantTypes), synth)
	}
	var lastSeq int64 = 1
	for i, wantType := range wantTypes {
		if got := ojson.GetBytesNoCopy(synth[i], "type").String(); got != wantType {
			t.Fatalf("synth[%d].type = %q, want %q", i, got, wantType)
		}
		seq := ojson.GetBytesNoCopy(synth[i], "sequence_number").Int()
		if seq <= lastSeq {
			t.Fatalf("synth[%d].sequence_number = %d, want > %d", i, seq, lastSeq)
		}
		lastSeq = seq
	}
	if got := ojson.GetBytesNoCopy(synth[2], "delta").String(); got != "I'm sorry, but I can't help with that request." {
		t.Fatalf("refusal delta = %q", got)
	}
	if got := ojson.GetBytesNoCopy(synth[3], "refusal").String(); got != "I'm sorry, but I can't help with that request." {
		t.Fatalf("refusal done = %q", got)
	}
	if gotSeq := ojson.GetBytesNoCopy(updatedCompleted, "sequence_number").Int(); gotSeq <= lastSeq {
		t.Fatalf("completed sequence_number = %d, want > %d", gotSeq, lastSeq)
	}
}

func TestResponsesStreamOutputTracker_SkipsAlreadyStreamedAndSynthesizesRemaining(t *testing.T) {
	var tracker ResponsesStreamOutputTracker
	tracker.Observe("response.created", []byte(`{"type":"response.created","response":{"id":"resp_1"}}`))
	tracker.Observe("response.output_item.added", []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message"}}`))
	tracker.Observe("response.output_text.delta", []byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"hi"}`))
	tracker.Observe("response.output_item.done", []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"hi"}]}}`))

	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"hi"}]},{"id":"msg_2","type":"message","content":[{"type":"refusal","refusal":"Blocked."}]}]}}`)
	synth, _ := tracker.SynthesizeMissingOutputEvents(completed)
	if len(synth) != 6 {
		t.Fatalf("expected 6 events only for unstreamed item 1, got %d: %q", len(synth), synth)
	}
	for i, ev := range synth {
		if idx := ojson.GetBytesNoCopy(ev, "output_index").Int(); idx != 1 {
			t.Fatalf("synth[%d].output_index = %d, want 1", i, idx)
		}
	}
}

func TestResponsesStreamOutputTracker_EmptyDeltaDoesNotSuppressSynthesis(t *testing.T) {
	var tracker ResponsesStreamOutputTracker
	tracker.Observe("response.output_item.added", []byte(`{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`))
	tracker.Observe("response.output_text.delta", []byte(`{"type":"response.output_text.delta","delta":""}`))

	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}}`)
	synth, _ := tracker.SynthesizeMissingOutputEvents(completed)
	if len(synth) == 0 {
		t.Fatal("expected synthesis when prior output_text.delta was empty")
	}
}

func TestResponsesStreamOutputTracker_UnkeyedDeltaSuppressesDuplicate(t *testing.T) {
	var tracker ResponsesStreamOutputTracker
	tracker.Observe("response.output_text.delta", []byte(`{"type":"response.output_text.delta","delta":"hello"}`))

	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`)
	synth, _ := tracker.SynthesizeMissingOutputEvents(completed)
	if len(synth) != 0 {
		t.Fatalf("expected no synthesis after unkeyed text delta, got %d: %q", len(synth), synth)
	}
}

func TestResponsesStreamOutputTracker_ClonesIDsAcrossMutatedBuffers(t *testing.T) {
	var tracker ResponsesStreamOutputTracker
	buf := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"hi"}]}}`)
	tracker.Observe("response.output_item.done", buf)
	// Simulate bufio.Scanner reusing and overwriting its internal line buffer.
	for i := range buf {
		buf[i] = 'x'
	}

	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"hi"}]}]}}`)
	synth, _ := tracker.SynthesizeMissingOutputEvents(completed)
	if len(synth) != 0 {
		t.Fatalf("expected no synthesis after observed item ID even when input buffer is overwritten, got %d: %q", len(synth), synth)
	}
}

func TestResponsesStreamOutputTracker_UnkeyedReasoningDeltaDoesNotSuppressMessageSynthesis(t *testing.T) {
	var tracker ResponsesStreamOutputTracker
	tracker.Observe("response.reasoning_summary_text.delta", []byte(`{"type":"response.reasoning_summary_text.delta","delta":"Thinking..."}`))

	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Thinking..."}]},{"id":"msg_1","type":"message","content":[{"type":"refusal","refusal":"Blocked."}]}]}}`)
	synth, _ := tracker.SynthesizeMissingOutputEvents(completed)
	if len(synth) == 0 {
		t.Fatal("expected message refusal synthesis even after unkeyed reasoning delta")
	}
}

func TestHasMeaningfulCodexOutputDelta_RefusalDelta(t *testing.T) {
	if !HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.refusal.delta","delta":"No"}`)) {
		t.Fatal("expected non-empty refusal delta to count as meaningful output delta")
	}
	if HasMeaningfulCodexOutputDelta([]byte(`{"type":"response.refusal.delta","delta":"   "}`)) {
		t.Fatal("expected whitespace-only refusal delta not to count as meaningful output delta")
	}
}
