package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	x "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCopilotParallelParentsAcrossRoutes(t *testing.T) {
	links := NewCopilotTurnLinks()
	opts := x.Options{Metadata: map[string]any{x.CanonicalSessionIDMetadataKey: "session"}}
	parent := func(resp string, calls string) CopilotTurn {
		turn := links.Resolve("account", CopilotTurnInput{Initiator: "user"}, opts, tr.FormatOpenAIResponse, ojson.Result{})
		links.Observe(turn, tr.FormatOpenAIResponse, ojson.Parse(`{"id":"`+resp+`","output":[`+calls+`]}`))
		return turn
	}
	one := parent("resp1", `{"call_id":"a"},{"call_id":"b"}`)
	two := parent("resp2", `{"call_id":"c"}`)
	batch := func(f tr.Format, ids []string, prev string) string {
		d := ojson.NewDocString(`{}`)
		var results []map[string]string
		for _, id := range ids {
			switch f {
			case tr.FormatOpenAI:
				results = append(results, map[string]string{"role": "tool", "tool_call_id": id})
			case tr.FormatOpenAIResponse:
				results = append(results, map[string]string{"type": "function_call_output", "call_id": id})
			case tr.FormatClaude:
				results = append(results, map[string]string{"type": "tool_result", "tool_use_id": id})
			}
		}
		switch f {
		case tr.FormatOpenAIResponse:
			_ = d.Set("input", results)
			if prev != "" {
				_ = d.Set("previous_response_id", prev)
			}
		case tr.FormatClaude:
			_ = d.Set("messages", []any{map[string]any{"role": "user", "content": results}})
		default:
			_ = d.Set("messages", results)
		}
		return d.String()
	}
	formats := []tr.Format{tr.FormatOpenAI, tr.FormatClaude, tr.FormatOpenAIResponse}
	for _, from := range formats {
		for _, to := range formats {
			for _, tc := range []struct {
				ids   []string
				prev  string
				reuse bool
			}{{[]string{"b", "a"}, "", true}, {[]string{"a", "c"}, "", false}, {[]string{"c", "a"}, "", false}, {[]string{"unknown", "b"}, "", false}, {[]string{"b", "b"}, "", false}, {[]string{""}, "", false}, {[]string{"c"}, "resp1", false}, {[]string{"unknown", "a"}, "resp1", true}} {
				if tc.prev != "" && to != tr.FormatOpenAIResponse {
					continue
				}
				src := batch(from, tc.ids, tc.prev)
				input := ParseCopilotTurnInput([]byte(src), from)
				got := links.Resolve("account", input, opts, to, ojson.Parse(batch(to, tc.ids, tc.prev)))
				if (got == one) != tc.reuse || got == two {
					t.Errorf("%s -> %s ids=%v previous=%s reused unexpected parent", from, to, tc.ids, tc.prev)
				}
			}
		}
	}
}

func TestCopilotRecycledChatIDsRequireCompletePairs(t *testing.T) {
	const assistant = `{"role":"assistant","tool_calls":[{"id":"tool:0","function":{"name":"sample","arguments":"{}"}}]}`
	const result = `{"role":"tool","tool_call_id":"tool:0","content":"ok"}`
	const user = `{"role":"user","content":"again"}`
	for _, tc := range []struct {
		tail string
		want bool
	}{{assistant + "," + result, true}, {assistant, false}, {assistant + "," + user + "," + result, false}, {assistant + "," + result + "," + result, false}} {
		doc := ojson.NewDocString(`{"messages":[` + assistant + "," + result + "," + user + "," + tc.tail + "]}")
		input := ParseCopilotTurnInput(doc.Bytes(), tr.FormatOpenAI)
		input.UniqueChatToolIDs(doc, false)
		if (doc.Get("messages.3.tool_calls.0.id").String() == "tool:1") != tc.want {
			t.Fatal("incorrect call ID rewrite")
		}
		if tc.want {
			if doc.Get("messages.4.tool_call_id").String() != "tool:1" {
				t.Fatal("unpaired result")
			}
			ids, ok := input.wireCalls(tr.FormatOpenAI, doc.Get("messages"))
			if !ok || len(ids) != 1 || ids[0] != "tool:0" {
				t.Fatal("lost issued ID for parent lookup")
			}
			_ = doc.Set("messages.4.tool_call_id", "tool:0")
			if _, ok = input.wireCalls(tr.FormatOpenAI, doc.Get("messages")); ok {
				t.Fatal("accepted substituted result")
			}
		}
	}
}

func TestCopilotLateSignaturesAndReplayIsolation(t *testing.T) {
	links := NewCopilotTurnLinks()
	const start = `{"model":"m","messages":[{"role":"user","content":"start"}]}`
	const message = `{"role":"assistant","content":null,"tool_calls":[{"id":"a","function":{"name":"look","arguments":"{\"y\":2,\"x\":1}"}},{"id":"b","function":{"name":"read","arguments":"{}"}}]}`
	scope := func(caller, session string) x.Options {
		return x.Options{Metadata: map[string]any{x.CanonicalSessionIDMetadataKey: session, x.CallerScopeMetadataKey: caller}}
	}
	opts := scope("caller", "session")
	record := func(prefix, opaque string) {
		c := links.NewChatCapture("account", opts, ojson.Parse(prefix))
		if c == nil {
			t.Fatal("no capture")
		}
		for _, chunk := range []string{`{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"read","arguments":"{}"}}],"reasoning_text":"visible "}}`, `{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"look","arguments":"{\"x\":1,"}}],"reasoning_text":"trace","reasoning_opaque":"` + opaque + `"}}`, `{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"y\":2}"}}]}}`, `{"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"signature-` + opaque + `"}}}]}}`, `{"delta":{},"finish_reason":"tool_calls"}`} {
			c.Observe(ojson.Parse(`{"choices":[` + chunk + `]}`))
		}
	}
	record(start, "first")
	d := ojson.NewDocString(start)
	_ = d.SetRaw("messages.-1", message)
	for _, tc := range []struct {
		name, account string
		opts          x.Options
		path, value   string
		ok            bool
	}{{"exact", "account", opts, "", "", true}, {"another account", "different", opts, "", "", false}, {"caller", "account", scope("other", "session"), "", "", false}, {"session", "account", scope("caller", "another"), "", "", false}, {"unscoped", "account", x.Options{}, "", "", false}, {"model", "account", opts, "model", "another", false}, {"name", "account", opts, "messages.1.tool_calls.1.function.name", "changed", false}, {"args", "account", opts, "messages.1.tool_calls.0.function.arguments", "{\"x\":2}", false}, {"history", "account", opts, "messages.0.content", "another prompt", false}} {
		t.Run(tc.name, func(t *testing.T) {
			q := ojson.NewDoc(d.Bytes())
			if tc.path != "" {
				_ = q.Set(tc.path, tc.value)
			}
			links.RestoreChatReplay(q, tc.account, tc.opts, CopilotTurnInput{})
			a := q.Get("messages.1")
			if (a.Get("reasoning_opaque").String() == "first") != tc.ok || (a.Get("reasoning_text").String() == "visible trace") != tc.ok || (a.Get("tool_calls.0.extra_content.google.thought_signature").String() == "signature-first") != tc.ok || a.Get("tool_calls.1.extra_content").Exists() {
				t.Fatal("incorrect replay state")
			}
		})
	}
	// Reusing IDs later in the same transcript must preserve both historical batches.
	_ = d.SetRaw("messages.-1", `{"role":"tool","tool_call_id":"a","content":"done"}`)
	_ = d.SetRaw("messages.-1", `{"role":"tool","tool_call_id":"b","content":"done"}`)
	_ = d.SetRaw("messages.-1", `{"role":"user","content":"again"}`)
	record(d.String(), "second")
	_ = d.SetRaw("messages.-1", message)
	links.RestoreChatReplay(d, "account", opts, CopilotTurnInput{})
	if d.Get("messages.1.reasoning_opaque").String() != "first" || d.Get("messages.5.reasoning_opaque").String() != "second" {
		t.Fatal("recycled ID stole a previous signature")
	}
	record(start, "conflict")
	q := ojson.NewDocString(start)
	_ = q.SetRaw("messages.-1", message)
	links.RestoreChatReplay(q, "account", opts, CopilotTurnInput{})
	if strings.Contains(q.String(), "reasoning_opaque") {
		t.Fatal("ambiguous retry restored a signature")
	}
}
