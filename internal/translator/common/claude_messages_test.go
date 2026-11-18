package common

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestClaudeMessageAccumulatorGroupsAndOrdersAssistantParts(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(3)
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"first","input":{}}]}`))
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"thinking","thinking":"reason"},{"type":"text","text":"answer"}]}`))
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"tool_use","id":"call_2","name":"second","input":{}}]}`))

	messages := accumulator.Messages()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	content := ojson.GetBytesNoCopy(messages[0], "content").Array()
	wantTypes := []string{"thinking", "text", "tool_use", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("content count = %d, want %d. Message: %s", len(content), len(wantTypes), string(messages[0]))
	}
	for i, wantType := range wantTypes {
		if got := content[i].Get("type").String(); got != wantType {
			t.Fatalf("content[%d].type = %q, want %q", i, got, wantType)
		}
	}
	if got := content[2].Get("id").String(); got != "call_1" {
		t.Fatalf("first tool_use id = %q, want call_1", got)
	}
	if got := content[3].Get("id").String(); got != "call_2" {
		t.Fatalf("second tool_use id = %q, want call_2", got)
	}
}

func TestClaudeMessageAccumulatorPreservesUserOrderAndRoleBoundaries(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(3)
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}`))
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"text","text":"continue"}]}`))
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"text","text":"done"}]}`))

	messages := accumulator.Messages()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	if got := ojson.GetBytesNoCopy(messages[0], "role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want user", got)
	}
	if got := ojson.GetBytesNoCopy(messages[0], "content.0.type").String(); got != "tool_result" {
		t.Fatalf("first user block type = %q, want tool_result", got)
	}
	if got := ojson.GetBytesNoCopy(messages[0], "content.1.text").String(); got != "continue" {
		t.Fatalf("second user block text = %q, want continue", got)
	}
	if got := ojson.GetBytesNoCopy(messages[1], "role").String(); got != "assistant" {
		t.Fatalf("messages[1].role = %q, want assistant", got)
	}
}

func TestClaudeMessageAccumulatorSkipsEmptyMessagesWithoutBreakingTurn(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(3)
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"text","text":"first"}]}`))
	accumulator.Append([]byte(`{"role":"user"}`))
	accumulator.Append([]byte(`{"role":"user","content":null}`))
	accumulator.Append([]byte(`{"role":"user","content":""}`))
	accumulator.Append([]byte(`{"role":"user","content":[]}`))
	accumulator.Append([]byte(`{"role":"invalid","content":[{"type":"text","text":"ignored"}]}`))
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"text","text":"second"}]}`))

	messages := accumulator.Messages()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	if got := ojson.GetBytesNoCopy(messages[0], "content.#").Int(); got != 2 {
		t.Fatalf("assistant content count = %d, want 2. Message: %s", got, string(messages[0]))
	}
}

func TestClaudeMessageAccumulatorFlushPreservesExplicitBoundary(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(2)
	accumulator.Append([]byte(`{"role":"user","content":"system reminder"}`))
	accumulator.Flush()
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"text","text":"question"}]}`))

	messages := accumulator.Messages()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	if got := ojson.GetBytesNoCopy(messages[0], "content.0.text").String(); got != "system reminder" {
		t.Fatalf("first message text = %q, want system reminder", got)
	}
	if got := ojson.GetBytesNoCopy(messages[1], "content.0.text").String(); got != "question" {
		t.Fatalf("second message text = %q, want question", got)
	}
}

func TestClaudeMessageAccumulatorPreservesBlockCacheControl(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(2)
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"text","text":"cached","cache_control":{"type":"ephemeral"}}]}`))
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"text","text":"fresh"}]}`))

	messages := accumulator.Messages()
	if got := ojson.GetBytesNoCopy(messages[0], "content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("cache_control.type = %q, want ephemeral", got)
	}
	if ojson.GetBytesNoCopy(messages[0], "content.1.cache_control").Exists() {
		t.Fatalf("second block should not have cache_control: %s", string(messages[0]))
	}
}

func TestAlignClaudeToolResults(t *testing.T) {
	t.Run("reorders permuted results while preserving non-result slots", func(t *testing.T) {
		input := ojson.Parse(`[
			{"type":"tool_result","tool_use_id":"call_2","content":"two"},
			{"type":"text","text":"extra user text"},
			{"type":"tool_result","tool_use_id":"call_1","content":"one"}
		]`)
		aligned := AlignClaudeToolResults(input, []string{"call_1", "call_2"})
		parts := aligned.Array()
		if len(parts) != 3 {
			t.Fatalf("len(parts) = %d, want 3", len(parts))
		}
		if parts[0].Get("tool_use_id").String() != "call_1" {
			t.Fatalf("parts[0].tool_use_id = %q, want call_1", parts[0].Get("tool_use_id").String())
		}
		if parts[1].Get("type").String() != "text" || parts[1].Get("text").String() != "extra user text" {
			t.Fatalf("parts[1] = %s, want extra user text", parts[1].Raw)
		}
		if parts[2].Get("tool_use_id").String() != "call_2" {
			t.Fatalf("parts[2].tool_use_id = %q, want call_2", parts[2].Get("tool_use_id").String())
		}

		leadingTextInput := ojson.Parse(`[
			{"type":"text","text":"leading text"},
			{"type":"tool_result","tool_use_id":"call_2","content":"two"},
			{"type":"tool_result","tool_use_id":"call_1","content":"one"}
		]`)
		leadingAligned := AlignClaudeToolResults(leadingTextInput, []string{"call_1", "call_2"})
		leadingParts := leadingAligned.Array()
		if len(leadingParts) != 3 {
			t.Fatalf("len(leadingParts) = %d, want 3", len(leadingParts))
		}
		if leadingParts[0].Get("type").String() != "text" || leadingParts[0].Get("text").String() != "leading text" {
			t.Fatalf("leadingParts[0] = %s, want leading text", leadingParts[0].Raw)
		}
		if leadingParts[1].Get("tool_use_id").String() != "call_1" {
			t.Fatalf("leadingParts[1].tool_use_id = %q, want call_1", leadingParts[1].Get("tool_use_id").String())
		}
		if leadingParts[2].Get("tool_use_id").String() != "call_2" {
			t.Fatalf("leadingParts[2].tool_use_id = %q, want call_2", leadingParts[2].Get("tool_use_id").String())
		}
	})

	t.Run("returns original content when count mismatches", func(t *testing.T) {
		input := ojson.Parse(`[
			{"type":"tool_result","tool_use_id":"call_1","content":"one"}
		]`)
		aligned := AlignClaudeToolResults(input, []string{"call_1", "call_2"})
		if aligned.Raw != input.Raw {
			t.Fatalf("aligned = %s, want %s", aligned.Raw, input.Raw)
		}
	})

	t.Run("returns original content when id not found", func(t *testing.T) {
		input := ojson.Parse(`[
			{"type":"tool_result","tool_use_id":"call_unknown","content":"unknown"},
			{"type":"tool_result","tool_use_id":"call_1","content":"one"}
		]`)
		aligned := AlignClaudeToolResults(input, []string{"call_1", "call_2"})
		if aligned.Raw != input.Raw {
			t.Fatalf("aligned = %s, want %s", aligned.Raw, input.Raw)
		}
	})

	t.Run("returns original content for non-array", func(t *testing.T) {
		input := ojson.Parse(`"text content"`)
		aligned := AlignClaudeToolResults(input, []string{"call_1"})
		if aligned.Raw != input.Raw {
			t.Fatalf("aligned = %s, want %s", aligned.Raw, input.Raw)
		}
	})
}

func TestClaudeDocumentHelpers(t *testing.T) {
	textDoc := ojson.Parse(`{"type":"document","title":"note.txt","context":"ctx","source":{"type":"text","media_type":"text/plain","data":"ORANGE-42"}}`)
	got, ok := ClaudeDocumentTextBlock(textDoc)
	if !ok || !strings.Contains(got, "Document: note.txt") || !strings.Contains(got, "Context: ctx") || !strings.Contains(got, "ORANGE-42") {
		t.Fatalf("ClaudeDocumentTextBlock = (%q, %v)", got, ok)
	}

	plainDoc := ojson.Parse(`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"plain"}}`)
	if got, ok := ClaudeDocumentTextBlock(plainDoc); !ok || got != "plain" {
		t.Fatalf("plain ClaudeDocumentTextBlock = (%q, %v)", got, ok)
	}

	emptyTextDoc := ojson.Parse(`{"type":"document","title":"empty.txt","source":{"type":"text","media_type":"text/plain","data":"   "}}`)
	if _, ok := ClaudeDocumentTextBlock(emptyTextDoc); ok {
		t.Fatal("expected whitespace-only text document to return false")
	}

	var texts []string
	var drops int
	badContentDoc := ojson.Parse(`{"type":"document","title":"bad","source":{"type":"content","content":[{"type":"image","source":{"type":"file","file_id":"f1"}}]}}`)
	ok = ExpandClaudeContentDocument(
		badContentDoc,
		func(s string) { texts = append(texts, s) },
		func(ojson.Result) ([]byte, bool) { return nil, false },
		func([]byte) {},
		func() { drops++ },
	)
	if ok || len(texts) != 0 || drops != 1 {
		t.Fatalf("badContentDoc = ok:%v texts:%v drops:%d", ok, texts, drops)
	}
}
