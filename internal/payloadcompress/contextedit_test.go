package payloadcompress

import (
	"github.com/MoeForge/moego/ojson"
	"strconv"
	"testing"
)

func clearToolUsesEdit(trigger, keep int) string {
	return `{"type":"` + ContextEditClearToolUses + `","trigger":{"type":"input_tokens","value":` +
		strconv.Itoa(trigger) + `},"keep":{"type":"tool_uses","value":` + strconv.Itoa(keep) + `}}`
}

// TestApplyContextEditingCreatesEdit verifies a missing context_management
// gains an edits array with the configured clear_tool_uses edit.
func TestApplyContextEditingCreatesEdit(t *testing.T) {
	doc := ojson.NewDocString(`{"model":"claude-x","messages":[]}`)
	if !ApplyContextEditing(doc, 100000, 3) {
		t.Fatal("expected body change")
	}
	edits := doc.Get("context_management.edits")
	if !edits.IsArray() || len(edits.Array()) != 1 {
		t.Fatalf("unexpected edits: %s", edits.Raw)
	}
	if edits.Array()[0].Get("type").String() != ContextEditClearToolUses {
		t.Fatalf("wrong edit type: %s", edits.Array()[0].Raw)
	}
	if edits.Array()[0].Get("trigger.value").Int() != 100000 ||
		edits.Array()[0].Get("keep.value").Int() != 3 {
		t.Fatalf("wrong edit values: %s", edits.Array()[0].Raw)
	}
}

// TestApplyContextEditingIdempotent verifies a second pass does not duplicate
// the edit or change the body.
func TestApplyContextEditingIdempotent(t *testing.T) {
	body := `{"context_management":{"edits":[` + clearToolUsesEdit(100000, 3) + `]}}`
	doc := ojson.NewDocString(body)
	if ApplyContextEditing(doc, 100000, 3) {
		t.Fatal("expected no change when edit already exists")
	}
	if doc.Get("context_management.edits").Array()[0].Get("type").String() != ContextEditClearToolUses {
		t.Fatal("edit lost")
	}
	if len(doc.Get("context_management.edits").Array()) != 1 {
		t.Fatal("edit duplicated")
	}
}

// TestApplyContextEditingStableSort verifies clear_thinking edits are moved
// ahead of clear_tool_uses edits while other edits keep their relative order.
func TestApplyContextEditingStableSort(t *testing.T) {
	doc := ojson.NewDocString(`{"context_management":{"edits":[` +
		`{"type":"other_edit"},` +
		`{"type":"` + ContextEditClearThinking + `","trigger":{"type":"thinking"}},` +
		`{"type":"another_edit"}]}}`)
	if !ApplyContextEditing(doc, 100000, 3) {
		t.Fatal("expected body change")
	}
	arr := doc.Get("context_management.edits").Array()
	want := []string{ContextEditClearThinking, "other_edit", "another_edit", ContextEditClearToolUses}
	if len(arr) != len(want) {
		t.Fatalf("unexpected edit count: %d", len(arr))
	}
	for i, w := range want {
		if got := arr[i].Get("type").String(); got != w {
			t.Fatalf("position %d: got %q want %q", i, got, w)
		}
	}
}

// TestApplyContextEditingNoOp verifies nil docs and non-positive settings
// never mutate anything.
func TestApplyContextEditingNoOp(t *testing.T) {
	if ApplyContextEditing(nil, 100000, 3) {
		t.Fatal("nil doc must be a no-op")
	}
	doc := ojson.NewDocString(`{"messages":[]}`)
	if ApplyContextEditing(doc, 0, 3) || ApplyContextEditing(doc, 100000, 0) {
		t.Fatal("zero settings must be a no-op")
	}
	if doc.Get("context_management").Exists() {
		t.Fatal("context_management must not be created")
	}
}

// TestContextEditingConfigDefaults verifies zero values normalize to the
// upstream defaults.
func TestContextEditingConfigDefaults(t *testing.T) {
	cfg := Config{}
	cfg.ContextEditing.TriggerTokens = 0
	cfg.ContextEditing.KeepToolUses = 0
	cfg.Sanitize()
	if cfg.ContextEditing.TriggerTokens != defaultContextEditTriggerTokens ||
		cfg.ContextEditing.KeepToolUses != defaultContextEditKeepToolUses {
		t.Fatalf("unexpected defaults: %+v", cfg.ContextEditing)
	}
	// Explicit values survive.
	cfg.ContextEditing.TriggerTokens = 50000
	cfg.ContextEditing.KeepToolUses = 5
	cfg.Sanitize()
	if cfg.ContextEditing.TriggerTokens != 50000 || cfg.ContextEditing.KeepToolUses != 5 {
		t.Fatalf("explicit values overwritten: %+v", cfg.ContextEditing)
	}
}
