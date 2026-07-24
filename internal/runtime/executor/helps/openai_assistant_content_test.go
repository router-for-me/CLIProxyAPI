package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestFlattenAssistantContentArrays(t *testing.T) {
	cases := []struct {
		name, payload, want string
	}{
		{
			"flattens text parts",
			`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`,
			"ab",
		},
		{
			"keeps string content",
			`{"messages":[{"role":"assistant","content":"plain"}]}`,
			"plain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := FlattenAssistantContentArrays([]byte(tc.payload))
			msgs := gjson.GetBytes(out, "messages").Array()
			last := msgs[len(msgs)-1]
			if last.Get("content").Type != gjson.String || last.Get("content").String() != tc.want {
				t.Fatalf("content = %s, want string %q", last.Get("content").Raw, tc.want)
			}
		})
	}
	// non-text parts untouched
	p := `{"messages":[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"u"}}]}]}`
	out := FlattenAssistantContentArrays([]byte(p))
	if !gjson.GetBytes(out, "messages.0.content").IsArray() {
		t.Fatalf("non-text array was flattened: %s", out)
	}
	// user arrays untouched
	p = `{"messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`
	out = FlattenAssistantContentArrays([]byte(p))
	if !gjson.GetBytes(out, "messages.0.content").IsArray() {
		t.Fatalf("user array was flattened: %s", out)
	}
	// empty text parts flatten to "" so the empty-drop can remove the message
	p = `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"text","text":""}]},{"role":"user","content":"ok"}]}`
	out = DropEmptyAssistantMessages(FlattenAssistantContentArrays([]byte(p)))
	if n := len(gjson.GetBytes(out, "messages").Array()); n != 2 {
		t.Fatalf("empty-flattened assistant not dropped: %d msgs, %s", n, out)
	}
}
