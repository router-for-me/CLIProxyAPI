package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesHasToolSearch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "top-level client declaration",
			payload: `{"tools":[{"type":"tool_search","execution":"client"}],"input":"hi"}`,
			want:    true,
		},
		{
			name:    "additional_tools client declaration",
			payload: `{"input":[{"type":"additional_tools","tools":[{"type":"tool_search","execution":" client "}]}]}`,
			want:    true,
		},
		{
			name:    "tool_search_call history only",
			payload: `{"input":[{"type":"tool_search_call","call_id":"c1","execution":"client","arguments":{}}]}`,
			want:    true,
		},
		{
			name:    "tool_search_output history only",
			payload: `{"input":[{"type":"tool_search_output","call_id":"c1","tools":[]}]}`,
			want:    true,
		},
		{
			name:    "hosted declaration only",
			payload: `{"tools":[{"type":"tool_search"}],"input":"hi"}`,
			want:    false,
		},
		{
			name:    "plain request",
			payload: `{"tools":[{"type":"function","name":"a"}],"input":[{"type":"message","role":"user","content":"hi"}]}`,
			want:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResponsesHasToolSearch([]byte(tc.payload)); got != tc.want {
				t.Fatalf("ResponsesHasToolSearch() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStripResponsesToolSearch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		payload   string
		wantTools string
		wantInput string
	}{
		{
			name:      "client declaration removed",
			payload:   `{"tools":[{"type":"function","name":"a"},{"type":"tool_search","execution":"client"}],"input":"hi"}`,
			wantTools: `[{"type":"function","name":"a"}]`,
			wantInput: `"hi"`,
		},
		{
			name:      "only client declaration keeps empty tools",
			payload:   `{"tools":[{"type":"tool_search","execution":"client"}],"input":"hi"}`,
			wantTools: `[]`,
			wantInput: `"hi"`,
		},
		{
			name:      "hosted declaration kept",
			payload:   `{"tools":[{"type":"tool_search","name":"ToolSearch"},{"type":"tool_search","execution":"client"}],"input":"hi"}`,
			wantTools: `[{"type":"tool_search","name":"ToolSearch"}]`,
			wantInput: `"hi"`,
		},
		{
			name: "input items and additional_tools cleaned",
			payload: `{"input":[` +
				`{"type":"additional_tools","tools":[{"type":"tool_search","execution":"client"},{"type":"function","name":"b"}]},` +
				`{"type":"message","role":"user","content":"hi"},` +
				`{"type":"tool_search_call","call_id":"c1","execution":"client","arguments":{"query":"x"}},` +
				`{"type":"tool_search_output","call_id":"c1","execution":"client","tools":[]}` +
				`]}`,
			wantInput: `[{"type":"additional_tools","tools":[{"type":"function","name":"b"}]},{"type":"message","role":"user","content":"hi"}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := StripResponsesToolSearch([]byte(tc.payload))
			if tc.wantTools != "" {
				if raw := gjson.GetBytes(got, "tools").Raw; raw != tc.wantTools {
					t.Fatalf("tools = %s, want %s", raw, tc.wantTools)
				}
			}
			if raw := gjson.GetBytes(got, "input").Raw; raw != tc.wantInput {
				t.Fatalf("input = %s, want %s", raw, tc.wantInput)
			}
		})
	}
}

func TestStripResponsesToolSearchReturnsOriginalWhenUnchanged(t *testing.T) {
	payload := []byte(`{"tools":[{"type":"function","name":"a"},{"type":"tool_search"}],"input":[{"type":"message","role":"user","content":"hi"}]}`)
	got := StripResponsesToolSearch(payload)
	if len(got) == 0 || &got[0] != &payload[0] {
		t.Fatalf("StripResponsesToolSearch() copied an unchanged payload: %s", got)
	}
}
