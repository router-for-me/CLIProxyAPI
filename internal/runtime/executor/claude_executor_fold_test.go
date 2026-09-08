package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func foldOrFail(t *testing.T, in string) ([]byte, bool) {
	t.Helper()
	out, applied := foldMidSystemMessagesIntoUserTurns([]byte(in))
	if !gjson.ValidBytes(out) {
		t.Fatalf("fold produced invalid JSON: %s", out)
	}
	return out, applied
}

func TestFoldMidSystemMessagesIntoUserTurns(t *testing.T) {
	t.Run("trailing system turn folds into last user message with its marker and no extra anchor", func(t *testing.T) {
		out, applied := foldOrFail(t, `{"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},
			{"role":"system","content":[{"type":"text","text":"reminder","cache_control":{"type":"ephemeral","ttl":"1h"}}]}
		]}`)
		if !applied {
			t.Fatal("expected applied")
		}
		msgs := gjson.GetBytes(out, "messages").Array()
		if len(msgs) != 3 || msgs[2].Get("role").String() != "user" {
			t.Fatalf("unexpected messages: %s", out)
		}
		blocks := msgs[2].Get("content").Array()
		if len(blocks) != 2 || blocks[0].Get("type").String() != "tool_result" || blocks[1].Get("text").String() != "reminder" || blocks[1].Get("cache_control.ttl").String() != "1h" {
			t.Fatalf("unexpected blocks: %s", msgs[2].Raw)
		}
		if strings.Contains(string(out), `"text":"`+claudeFoldAnchorText+`"`) {
			t.Fatalf("anchor must not be added when a reminder was folded: %s", out)
		}
	})

	t.Run("mid-history system turn folds into the preceding user message; string content promoted", func(t *testing.T) {
		out, applied := foldOrFail(t, `{"messages":[
			{"role":"user","content":"hello"},
			{"role":"system","content":[{"type":"text","text":"r1"}]},
			{"role":"assistant","content":[{"type":"text","text":"hey"}]},
			{"role":"user","content":[{"type":"text","text":"next","cache_control":{"type":"ephemeral"}}]}
		]}`)
		if !applied {
			t.Fatal("expected applied")
		}
		msgs := gjson.GetBytes(out, "messages").Array()
		first := msgs[0].Get("content").Array()
		if len(msgs) != 3 || len(first) != 2 || first[0].Get("text").String() != "hello" || first[1].Get("text").String() != "r1" || first[1].Get("cache_control").Exists() {
			t.Fatalf("unexpected: %s", out)
		}
		if msgs[2].Get("content.0.cache_control.type").String() != "ephemeral" {
			t.Fatalf("existing user marker lost: %s", msgs[2].Raw)
		}
	})

	t.Run("tool_result-ending user turn gets the anchor and inherits the marker", func(t *testing.T) {
		out, applied := foldOrFail(t, `{"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"ok"}],"cache_control":{"type":"ephemeral"}}]}
		]}`)
		if !applied {
			t.Fatal("expected applied")
		}
		last := gjson.GetBytes(out, "messages.2.content").Array()
		if len(last) != 2 || last[1].Get("type").String() != "text" || last[1].Get("text").String() != claudeFoldAnchorText {
			t.Fatalf("anchor missing: %s", out)
		}
		if last[0].Get("cache_control").Exists() || last[1].Get("cache_control.type").String() != "ephemeral" {
			t.Fatalf("marker not moved to anchor: %s", out)
		}
		if last[0].Get("content.0.text").String() != "ok" {
			t.Fatalf("tool_result content altered: %s", last[0].Raw)
		}
	})

	t.Run("every tool_result-ending user turn is anchored deterministically", func(t *testing.T) {
		out, _ := foldOrFail(t, `{"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"b"}]}
		]}`)
		if n := strings.Count(string(out), `"text":"`+claudeFoldAnchorText+`"`); n != 2 {
			t.Fatalf("expected 2 anchors, got %d: %s", n, out)
		}
	})

	t.Run("system turn first becomes a user turn in place", func(t *testing.T) {
		out, applied := foldOrFail(t, `{"messages":[{"role":"system","content":"lead"},{"role":"user","content":"hi"}]}`)
		msgs := gjson.GetBytes(out, "messages").Array()
		if !applied || len(msgs) != 2 || msgs[0].Get("role").String() != "user" || msgs[0].Get("content.0.text").String() != "lead" || msgs[1].Get("content").String() != "hi" {
			t.Fatalf("unexpected: %s", out)
		}
	})

	t.Run("system turn after an assistant tool_use fails closed", func(t *testing.T) {
		in := `{"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
			{"role":"system","content":"note"},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a"}]}
		]}`
		out, applied := foldOrFail(t, in)
		if applied || string(out) != in {
			t.Fatalf("expected untouched payload: %s", out)
		}
	})

	t.Run("system turn after a plain assistant turn is inserted in place as a user turn", func(t *testing.T) {
		out, applied := foldOrFail(t, `{"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"text","text":"yo"}]},
			{"role":"system","content":"note"},
			{"role":"user","content":"more"}
		]}`)
		msgs := gjson.GetBytes(out, "messages").Array()
		if !applied || len(msgs) != 4 || msgs[2].Get("role").String() != "user" || msgs[2].Get("content.0.text").String() != "note" {
			t.Fatalf("unexpected: %s", out)
		}
	})

	for name, in := range map[string]string{
		"empty system array":         `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":[]}]}`,
		"whitespace system string":   `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":"   "}]}`,
		"tool_addition system block": `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"tool_addition","tools":[]}]}]}`,
		"mixed system blocks":        `{"messages":[{"role":"user","content":"hi"},{"role":"system","content":[{"type":"text","text":"t"},{"type":"output_config","effort":"low"}]}]}`,
		"no system no tool_result":   `{"model":"x","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":[{"type":"text","text":"more","cache_control":{"type":"ephemeral"}}]}]}`,
		"messages not an array":      `{"messages":"nope"}`,
		"invalid json":               `{"messages":[`,
	} {
		t.Run("fails closed / no-op: "+name, func(t *testing.T) {
			out, applied := foldMidSystemMessagesIntoUserTurns([]byte(in))
			if applied || string(out) != in {
				t.Fatalf("expected untouched payload, got applied=%v: %s", applied, out)
			}
		})
	}
}
