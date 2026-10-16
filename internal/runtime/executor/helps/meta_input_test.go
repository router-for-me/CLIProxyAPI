package helps

import (
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

func TestDropMetaUnreplayableReasoning(t *testing.T) {
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
			name:  "drops reasoning before a developer message",
			input: `[{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},{"type":"message","role":"developer","content":[]},{"type":"message","role":"assistant","content":[]}]`,
			want:  []string{"message:developer", "message:assistant"},
		},
		{
			name:  "drops reasoning with nothing to replay",
			input: `[{"type":"reasoning","summary":[],"content":null},{"type":"reasoning","summary":[],"encrypted_content":""},{"type":"message","role":"assistant","content":[]}]`,
			want:  []string{"message:assistant"},
		},
		{
			name:  "keeps a stored reasoning id without encrypted content",
			input: `[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"message","role":"assistant","content":[]}]`,
			want:  []string{"reasoning", "message:assistant"},
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
			assertMetaInputKinds(t, DropMetaUnreplayableReasoning(body), tt.want)
		})
	}
}

func TestDropMetaUnreplayableReasoning_ReturnsUnchangedBody(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","summary":[],"encrypted_content":"Q-PaD1"},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]}`)
	if got := DropMetaUnreplayableReasoning(body); string(got) != string(body) {
		t.Fatalf("body changed: %s", got)
	}
}
