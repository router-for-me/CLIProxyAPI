package responsestools

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCustomBridgeRejectsUnicodeLossAtJSONBoundary(t *testing.T) {
	policy := RoutePolicy{ClientSearch: ClientSearchNative, CustomTools: CustomToolsFunction, CustomGrammar: CustomGrammarDescribe}
	for _, bad := range []string{`\ud800`, string([]byte{0xff})} {
		request := `{"tools":[{"type":"custom","name":"apply_patch"}],"input":[{"type":"custom_tool_call","name":"apply_patch","call_id":"old","input":"` + bad + `"}]}`
		if prepared, err := Prepare([]byte(request), policy, DefaultLimits(), NewLimiter(DefaultLimits())); err == nil {
			if prepared.Attempt != nil {
				prepared.Attempt.Close()
			}
			t.Fatal("request decoding silently changed custom history")
		}
		prepared, err := Prepare([]byte(`{"tools":[{"type":"custom","name":"apply_patch"}],"input":[]}`), policy, DefaultLimits(), NewLimiter(DefaultLimits()))
		if err != nil || prepared.Attempt == nil {
			t.Fatalf("prepare control: %v", err)
		}
		alias, _ := prepared.Attempt.bridge.Alias(ToolIdentity{Name: "apply_patch", Kind: ToolKindCustom})
		body := fmt.Sprintf(`{"output":[{"type":"function_call","id":"fc_boundary","name":%q,"call_id":"c_boundary","arguments":"{\"input\":\"%s\"}"}]}`, alias, bad)
		if _, err := prepared.Attempt.RewriteResponse([]byte(body)); err == nil {
			t.Fatal("buffered response decoding silently changed custom input")
		}
		if _, err := prepared.Attempt.Feed(frameJSON(t, map[string]any{
			"type": eventOutputItemAdded,
			"item": map[string]any{"type": "function_call", "id": "fc_boundary", "name": alias, "call_id": "c_boundary"},
		})); err != nil {
			t.Fatalf("stream identity control: %v", err)
		}
		frame := fmt.Sprintf(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_boundary","name":%q,"call_id":"c_boundary","arguments":"{\"input\":\"%s\"}"}}`, alias, bad)
		if _, err := prepared.Attempt.Feed([]byte(frame)); err == nil {
			t.Fatal("stream JSON decoding silently changed custom input")
		}
		prepared.Attempt.Close()
	}
}

func TestCustomPatchBridgeDescriptionAndIdentity(t *testing.T) {
	value, _ := decodeValue([]byte(`{
		"tools":[
			{"type":"function","name":"apply_patch","description":"ordinary function","parameters":{"type":"object"}},
			{"type":"namespace","name":"fs","tools":[
				{"type":"custom","name":"apply_patch","description":"Patch files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
				"format":{"type":"grammar","syntax":"lark","definition":"start: \"*** Begin Patch\" \"*** Environment ID:\" \"*** End Patch\""}}
			]}
		],"input":[]
	}`))
	bridge, changed, err := RewriteCustomDeclarations(value, CustomGrammarDescribe)
	if err != nil || !changed {
		t.Fatalf("rewrite: changed=%v err=%v", changed, err)
	}
	tools := value.(map[string]any)["tools"].([]any)
	ordinary := tools[0].(map[string]any)
	if ordinary["name"] != "apply_patch" || ordinary["description"] != "ordinary function" {
		t.Fatalf("ordinary function was rewritten: %v", ordinary)
	}
	tool := tools[1].(map[string]any)["tools"].([]any)[0].(map[string]any)
	description := tool["description"].(string)
	for _, required := range []string{"JSON object", "*** Begin Patch", "*** End Patch", "*** Environment ID:", "Original custom tool format"} {
		if !strings.Contains(description, required) {
			t.Errorf("bridged description omitted %q: %s", required, description)
		}
	}
	if strings.Contains(description, "do not wrap the patch in JSON") {
		t.Fatal("bridged tool retains contradictory freeform instructions")
	}
	identity, ok := bridge.ResolveWireAlias(tool["name"].(string))
	if !ok || identity != (ToolIdentity{Namespace: "fs", Name: "apply_patch", Kind: ToolKindCustom}) {
		t.Fatalf("bridge lost namespace or custom identity: %+v", identity)
	}
}

func TestCustomArgumentsUnicodeFidelity(t *testing.T) {
	for _, tc := range []struct {
		name, arguments, want string
		valid                 bool
	}{
		{"paired", `{"input":"\ud83d\ude00"}`, "😀", true},
		{"bmp", `{"input":"\u4e2d\u6587\r\n"}`, "中文\r\n", true},
		{"escaped_literal", `{"input":"\\ud800"}`, `\ud800`, true},
		{"literal_replacement", `{"input":"�"}`, "�", true},
		{"high_only", `{"input":"\ud800"}`, "", false},
		{"low_only", `{"input":"\udc00"}`, "", false},
		{"wrong_pair", `{"input":"\ud800\u0041"}`, "", false},
		{"interrupted_pair", `{"input":"\ud800x\udc00"}`, "", false},
		{"invalid_utf8", "{\"input\":\"" + string([]byte{0xff}) + "\"}", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnpackCustomArguments(tc.arguments)
			if tc.valid {
				if err != nil || got != tc.want {
					t.Fatalf("input changed: got=%q want=%q err=%v", got, tc.want, err)
				}
			} else if err == nil {
				t.Fatalf("malformed Unicode silently became %q", got)
			}
		})
	}
}

func TestCustomStreamSplitUnicodeAndMalformedInput(t *testing.T) {
	for _, tc := range []struct {
		name, tail string
		valid      bool
	}{
		{"paired", `\ude00"}`, true},
		{"unpaired", `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			feed := streamTestFeed(t)
			alias, _ := feed.bridge.Alias(ToolIdentity{Name: "apply_patch", Kind: ToolKindCustom})
			if _, err := feed.Feed(frameJSON(t, map[string]any{
				"type": eventOutputItemAdded,
				"item": map[string]any{"type": "function_call", "id": "fc_unicode", "name": alias, "call_id": "c_unicode"},
			})); err != nil {
				t.Fatal(err)
			}
			for _, fragment := range []string{`{"input":"\ud83d`, tc.tail} {
				out, err := feed.Feed(frameJSON(t, map[string]any{
					"type": eventFunctionCallArgsDelta, "item_id": "fc_unicode", "delta": fragment,
				}))
				if err != nil || len(out) != 0 {
					t.Fatalf("partial executable input must stay buffered: out=%s err=%v", out, err)
				}
			}
			out, err := feed.Feed(frameJSON(t, map[string]any{
				"type": eventOutputItemDone,
				"item": map[string]any{"type": "function_call", "id": "fc_unicode", "name": alias, "call_id": "c_unicode"},
			}))
			if !tc.valid {
				if err == nil || len(out) != 0 {
					t.Fatalf("invalid input must fail before custom input events: out=%s err=%v", out, err)
				}
				return
			}
			if err != nil || len(out) != 3 {
				t.Fatalf("valid split input did not finish: out=%s err=%v", out, err)
			}
			var event map[string]any
			if err := json.Unmarshal(out[2], &event); err != nil {
				t.Fatal(err)
			}
			item := event["item"].(map[string]any)
			if item["name"] != "apply_patch" || item["input"] != "😀" || item["call_id"] != "c_unicode" {
				t.Fatalf("custom output lost identity/input: %v", item)
			}
		})
	}
}
