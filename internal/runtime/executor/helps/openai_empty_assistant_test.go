package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestDropEmptyAssistantMessages(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		wantRoles []string
	}{
		{
			name:      "drops empty string content",
			payload:   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":""},{"role":"user","content":"ok"}]}`,
			wantRoles: []string{"user", "user"},
		},
		{
			name:      "drops empty array content",
			payload:   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[]},{"role":"user","content":"ok"}]}`,
			wantRoles: []string{"user", "user"},
		},
		{
			name:      "drops null content",
			payload:   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null}]}`,
			wantRoles: []string{"user"},
		},
		{
			name:      "drops array of empty text parts",
			payload:   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":""}]}]}`,
			wantRoles: []string{"user"},
		},
		{
			name:      "keeps assistant with text",
			payload:   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			wantRoles: []string{"user", "assistant"},
		},
		{
			name:      "keeps empty assistant with tool calls",
			payload:   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"type":"function","id":"a","function":{"name":"f","arguments":"{}"}}]}]}`,
			wantRoles: []string{"user", "assistant"},
		},
		{
			name:      "keeps empty user messages",
			payload:   `{"messages":[{"role":"user","content":""},{"role":"assistant","content":"hello"}]}`,
			wantRoles: []string{"user", "assistant"},
		},
		{
			name:      "drops multiple empty assistants",
			payload:   `{"messages":[{"role":"assistant","content":""},{"role":"user","content":"hi"},{"role":"assistant"},{"role":"user","content":"ok"}]}`,
			wantRoles: []string{"user", "user"},
		},
		{
			name:      "no messages field untouched",
			payload:   `{"model":"m"}`,
			wantRoles: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := DropEmptyAssistantMessages([]byte(tc.payload))
			got := gjson.GetBytes(out, "messages")
			var roles []string
			got.ForEach(func(_, m gjson.Result) bool {
				roles = append(roles, m.Get("role").String())
				return true
			})
			if len(roles) != len(tc.wantRoles) {
				t.Fatalf("roles = %v, want %v (payload %s)", roles, tc.wantRoles, out)
			}
			for i := range roles {
				if roles[i] != tc.wantRoles[i] {
					t.Fatalf("roles = %v, want %v (payload %s)", roles, tc.wantRoles, out)
				}
			}
		})
	}
}

func TestDropEmptyAssistantMessagesNoChange(t *testing.T) {
	payload := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}],"model":"m"}`
	out := DropEmptyAssistantMessages([]byte(payload))
	if string(out) != payload {
		t.Fatalf("payload changed without empty assistant messages: %s", out)
	}
}
