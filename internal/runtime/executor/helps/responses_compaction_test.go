package helps

import "testing"

func TestResponsesHasCompactionTrigger(t *testing.T) {
	if ResponsesHasCompactionTrigger([]byte(`{"input":[{"type":"message"},{"type":"compaction_trigger"}]}`)) != true {
		t.Fatal("expected compaction_trigger to be detected")
	}
	if ResponsesHasCompactionTrigger([]byte(`{"input":[{"type":"message"}]}`)) {
		t.Fatal("ordinary input must not look like a compaction trigger")
	}
	if ResponsesHasCompactionTrigger(nil, []byte(`{"input":[{"type":"compaction_trigger"}]}`)) != true {
		t.Fatal("expected trigger on a later payload")
	}
}

func TestResponsesOutputItemCounts(t *testing.T) {
	typed, total := ResponsesOutputItemCounts([]byte(`{"output":[{"type":"message"}]}`), "compaction")
	if typed != 0 || total != 1 {
		t.Fatalf("typed=%d total=%d, want 0 and 1", typed, total)
	}
	typed, total = ResponsesOutputItemCounts([]byte(`{"type":"response.completed","response":{"output":[{"type":"compaction","encrypted_content":"x"}]}}`), "compaction")
	if typed != 1 || total != 1 {
		t.Fatalf("typed=%d total=%d, want 1 and 1", typed, total)
	}
}
