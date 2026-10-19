package helps

import (
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func metaInputKinds(body []byte) []string {
	var kinds []string
	for _, item := range gjson.GetBytes(body, "input").Array() {
		itemType, role := metaInputItemKind(item)
		if role != "" {
			itemType += ":" + role
		}
		kinds = append(kinds, itemType)
	}
	return kinds
}

func assertMetaInputKinds(t *testing.T, body []byte, want []string) {
	t.Helper()
	got := metaInputKinds(body)
	if len(got) != len(want) {
		t.Fatalf("input = %v, want %v; body=%s", got, want, body)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("input = %v, want %v; body=%s", got, want, body)
		}
	}
}

func TestDropMetaDanglingReasoning(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "keeps reasoning before a preamble and tool call",
			input: `[{"type":"message","role":"user","content":"hi"},{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},{"type":"message","role":"assistant","content":[]},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]`,
			want:  []string{"message:user", "reasoning", "message:assistant", "function_call"},
		},
		{
			name:  "keeps reasoning followed by more reasoning and a tool call",
			input: `[{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD2"},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]`,
			want:  []string{"reasoning", "reasoning", "function_call"},
		},
		{
			name:  "drops a thinking-only turn before a user message",
			input: `[{"type":"message","role":"user","content":"hi"},{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},{"role":"user","content":"again"}]`,
			want:  []string{"message:user", "message:user"},
		},
		{
			name:  "drops every reasoning item of a thinking-only turn",
			input: `[{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD2"},{"type":"message","role":"user","content":"again"},{"type":"message","role":"assistant","content":[]}]`,
			want:  []string{"message:user", "message:assistant"},
		},
		{
			name:  "drops reasoning before a developer message",
			input: `[{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},{"type":"message","role":"developer","content":[]},{"type":"message","role":"assistant","content":[]}]`,
			want:  []string{"message:developer", "message:assistant"},
		},
		{
			name:  "keeps trailing reasoning",
			input: `[{"type":"message","role":"user","content":"hi"},{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"}]`,
			want:  []string{"message:user", "reasoning"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"muse-wonder-1.0","input":` + tt.input + `}`)
			assertMetaInputKinds(t, DropMetaDanglingReasoning(body), tt.want)
		})
	}
}

func TestTagMetaCommentaryPhase(t *testing.T) {
	body := []byte(`{"input":[` +
		`{"type":"message","role":"user","content":"hi"},` +
		`{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Let me check."}]},` +
		`{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD2"},` +
		`{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},` +
		`{"type":"function_call","call_id":"c2","name":"f","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"a"},` +
		`{"type":"function_call_output","call_id":"c2","output":"b"},` +
		`{"role":"assistant","content":[{"type":"output_text","text":"Now the other one."}]},` +
		`{"type":"function_call","call_id":"c3","name":"f","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c3","output":"c"},` +
		`{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Kept."}]},` +
		`{"type":"function_call","call_id":"c4","name":"f","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c4","output":"d"},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]},` +
		`{"type":"message","role":"user","content":"thanks"}` +
		`]}`)

	got := TagMetaCommentaryPhase(body)

	wantPhase := map[int]string{2: "commentary", 8: "commentary", 11: "final_answer", 14: ""}
	for index, want := range wantPhase {
		phase := gjson.GetBytes(got, "input."+strconv.Itoa(index)+".phase")
		if want == "" {
			if phase.Exists() {
				t.Fatalf("input.%d.phase = %q, want none; body=%s", index, phase.String(), got)
			}
			continue
		}
		if phase.String() != want {
			t.Fatalf("input.%d.phase = %q, want %q; body=%s", index, phase.String(), want, got)
		}
	}
	for _, index := range []int{0, 1, 3, 15} {
		if gjson.GetBytes(got, "input."+strconv.Itoa(index)+".phase").Exists() {
			t.Fatalf("input.%d got a phase; body=%s", index, got)
		}
	}
}

func TestDropMetaDanglingReasoning_ReturnsUnchangedBody(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]}`)
	if got := DropMetaDanglingReasoning(body); string(got) != string(body) {
		t.Fatalf("body changed: %s", got)
	}
}

// A long run of consecutive assistant messages and reasoning items is decided
// in one pass: every preamble before the trailing function_call is tagged, and
// every reasoning item before the trailing user message is dropped.
func TestMetaInputHelpers_LongRuns(t *testing.T) {
	const n = 20000
	var b strings.Builder
	b.WriteString(`{"input":[`)
	for i := 0; i < n; i++ {
		b.WriteString(`{"type":"message","role":"assistant","content":[]},`)
	}
	b.WriteString(`{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},`)
	for i := 0; i < n; i++ {
		b.WriteString(`{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},`)
	}
	b.WriteString(`{"role":"user","content":"again"}]}`)
	body := []byte(b.String())

	tagged := TagMetaCommentaryPhase(body)
	if got := strings.Count(string(tagged), `"phase":"commentary"`); got != n {
		t.Fatalf("tagged %d preambles, want %d", got, n)
	}
	dropped := DropMetaDanglingReasoning(body)
	if kinds := metaInputKinds(dropped); len(kinds) != n+2 || kinds[len(kinds)-1] != "message:user" {
		t.Fatalf("got %d items after drop, want %d ending in the user message", len(kinds), n+2)
	}
}
