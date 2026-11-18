package helps

import (
	"bytes"
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPayloadReplaceUserText(t *testing.T) {
	for _, tt := range []struct {
		name, protocol, root, input string
		positions                   []int
		all                         bool
		want                        map[string]string
	}{
		{"chat roles and occurrences", "openai", "", `{"messages":[{"role":"system","content":"you are hero"},{"role":"user","content":"prefix you are hero; you are hero"},{"role":"assistant","content":"you are hero"},{"role":"user","content":"you are hero"},{"role":"user","content":"you are hero"}]}`, []int{1, 2}, false, map[string]string{"messages.0.content": "you are hero", "messages.1.content": "prefix you are fool; you are fool", "messages.2.content": "you are hero", "messages.3.content": "you are fool", "messages.4.content": "you are hero"}},
		{"single user skips missing second", "openai", "", `{"messages":[{"role":"user","content":"you are hero"}]}`, []int{1, 2}, false, map[string]string{"messages.0.content": "you are fool"}},
		{"all users", "openai", "", `{"messages":[{"role":"user","content":"you are hero"},{"role":"user","content":"you are hero"}]}`, nil, true, map[string]string{"messages.0.content": "you are fool", "messages.1.content": "you are fool"}},
		{"claude tools and metadata protected", "claude", "", `{"system":"you are hero","messages":[{"role":"user","content":[{"type":"tool_result","content":"you are hero"}]},{"role":"user","content":[{"type":"text","text":"you are hero","cache_control":{"type":"ephemeral"}},{"type":"tool_result","content":[{"type":"text","text":"you are hero"}]},{"type":"thinking","text":"you are hero","signature":"you are hero"},{"type":"image","source":{"data":"you are hero"}}]}]}`, []int{2}, false, map[string]string{"system": "you are hero", "messages.0.content.0.content": "you are hero", "messages.1.content.0.text": "you are fool", "messages.1.content.0.cache_control.type": "ephemeral", "messages.1.content.1.content.0.text": "you are hero", "messages.1.content.2.text": "you are hero", "messages.1.content.2.signature": "you are hero", "messages.1.content.3.source.data": "you are hero"}},
		{"chat multimodal", "openai", "", `{"messages":[{"role":"user","content":[{"type":"text","text":"you are hero"},{"type":"image_url","image_url":{"url":"you are hero"}},{"type":"input_audio","input_audio":{"data":"you are hero"}},{"type":"input_text","text":"you are hero"}]}]}`, []int{1}, false, map[string]string{"messages.0.content.0.text": "you are fool", "messages.0.content.1.image_url.url": "you are hero", "messages.0.content.2.input_audio.data": "you are hero", "messages.0.content.3.text": "you are hero"}},
		{"responses message types", "openai-response", "", `{"input":[{"type":"function_call_output","role":"user","output":"you are hero","content":"you are hero"},{"type":"message","role":"assistant","content":"you are hero"},{"role":"user","content":[{"type":"input_text","text":"you are hero"},{"type":"input_image","image_url":"you are hero"}]},{"type":"message","role":"user","content":"you are hero"}],"instructions":"you are hero"}`, []int{1, 2}, false, map[string]string{"input.0.output": "you are hero", "input.0.content": "you are hero", "input.1.content": "you are hero", "input.2.content.0.text": "you are fool", "input.2.content.1.image_url": "you are hero", "input.3.content": "you are fool", "instructions": "you are hero"}},
		{"codex string input", "codex", "", `{"input":"you are hero","previous_response_id":"you are hero"}`, []int{1, 2}, false, map[string]string{"input": "you are fool", "previous_response_id": "you are hero"}},
		{"gemini parts", "gemini", "", `{"contents":[{"role":"model","parts":[{"text":"you are hero"}]},{"role":"user","parts":[{"text":"you are hero"},{"text":"you are hero","thought":true,"thoughtSignature":"you are hero"},{"functionResponse":{"response":{"text":"you are hero"}}},{"inlineData":{"data":"you are hero"}}]}]}`, []int{1}, false, map[string]string{"contents.0.parts.0.text": "you are hero", "contents.1.parts.0.text": "you are fool", "contents.1.parts.1.text": "you are hero", "contents.1.parts.1.thoughtSignature": "you are hero", "contents.1.parts.2.functionResponse.response.text": "you are hero", "contents.1.parts.3.inlineData.data": "you are hero"}},
		{"antigravity root", "antigravity", "request", `{"request":{"contents":[{"role":"user","parts":[{"text":"you are hero"}]}]},"text":"you are hero"}`, []int{1}, false, map[string]string{"request.contents.0.parts.0.text": "you are fool", "text": "you are hero"}},
		{"empty users still count", "claude", "", `{"messages":[{"role":"user","content":null},{"role":"user","content":[]},{"role":"user","content":23},{"role":"user"},{"role":"user","content":"you are hero"}]}`, []int{5}, false, map[string]string{"messages.4.content": "you are fool"}},
		{"unmatched first still counts", "openai", "", `{"messages":[{"role":"user","content":"hello"},{"role":"user","content":"you are hero"}]}`, []int{1}, false, map[string]string{"messages.0.content": "hello", "messages.1.content": "you are hero"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
				Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Role: "user", Positions: tt.positions, All: tt.all},
				Replacements: []config.PayloadReplacement{{"you are hero": "you are fool"}},
			}}}}
			out := ApplyPayloadConfigWithRoot(cfg, "model", tt.protocol, tt.root, []byte(tt.input), nil, "", "")
			for path, want := range tt.want {
				if got := ojson.GetBytes(out, path).String(); got != want {
					t.Errorf("%s = %q, want %q; body: %s", path, got, want, out)
				}
			}
			// Replacement must preserve the entire structure and every unrelated field.
			wantDoc := ojson.NewDocString(tt.input)
			for path, value := range tt.want {
				if errSet := wantDoc.Set(path, value); errSet != nil {
					t.Fatal(errSet)
				}
			}
			if !reflect.DeepEqual(ojson.ParseBytes(out).Value(), ojson.ParseBytes(wantDoc.Bytes()).Value()) {
				t.Fatalf("unexpected structural change: %s, want %s", out, wantDoc.Bytes())
			}
		})
	}
}

func TestPayloadReplaceNoOp(t *testing.T) {
	for _, input := range []string{
		`{}`, `{"messages":null}`, `{"messages":{}}`, `{"messages":[]}`,
		`{"messages":[null,1,"user",{}, {"role":"assistant","content":"you are hero"}]}`,
		`{"messages":[{"role":"user","content":null},{"role":"user","content":123},{"role":"user","content":{"text":"you are hero"}}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"you are "},{"type":"text","text":"hero"},{"type":"text","text":123},{"text":"you are hero"}]}]}`,
		`{"messages":[{"role":"user","content":"You are hero"}]}`,
	} {
		cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Role: "user", All: true}, Replacements: []config.PayloadReplacement{{"you are hero": "you are fool"}}}}}}
		body := []byte(input)
		out := ApplyPayloadConfigWithRoot(cfg, "model", "openai", "", body, nil, "", "")
		if !bytes.Equal(out, body) || &out[0] != &body[0] {
			t.Fatalf("no-op changed input: %s -> %s", body, out)
		}
	}
}

func TestPayloadReplaceOrderingAndPositions(t *testing.T) {
	for _, tt := range []struct {
		name         string
		replacements []config.PayloadReplacement
		want         string
	}{
		{"ordered cascading", []config.PayloadReplacement{{"hero": "fool"}, {"fool": "friend"}}, "friend friend"},
		{"single pass and duplicate positions", []config.PayloadReplacement{{"hero": "hero!"}}, "hero! hero!"},
		{"delete", []config.PayloadReplacement{{"hero": ""}}, " "},
		{"identity", []config.PayloadReplacement{{"hero": "hero"}}, "hero hero"},
		{"literal not regex", []config.PayloadReplacement{{"h.*": "x"}}, "hero hero"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Role: "user", Positions: []int{2, 1, 1, 1000000}}, Replacements: tt.replacements}}}}
			out := ApplyPayloadConfigWithRoot(cfg, "model", "openai", "", []byte(`{"messages":[{"role":"user","content":"hero hero"}]}`), nil, "", "")
			if got := ojson.GetBytes(out, "messages.0.content").String(); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if got := len(ojson.GetBytes(out, "messages").Array()); got != 1 {
				t.Fatalf("created missing messages: %s", out)
			}
		})
	}
}

func TestPayloadReplaceMatchingAndStageOrder(t *testing.T) {
	rule := config.PayloadReplaceRule{
		Models: []config.PayloadModelRule{{Name: "alias-*", Protocol: "claude", FromProtocol: "responses", Headers: map[string]string{"X-Tier": "prod-*"}, Match: []map[string]any{{"stage": "ready"}}, NotMatch: []map[string]any{{"blocked": true}}, Exist: []string{"messages.0.content"}, NotExist: []string{"disabled"}}},
		Target: config.PayloadReplaceTarget{Role: "user", Positions: []int{1}}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
	}
	// Multiple matching model entries must not execute a rule more than once.
	rule.Models = append(rule.Models, rule.Models[0])
	cfg := &config.Config{Payload: config.PayloadConfig{
		Default:  []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"stage": "ready"}}},
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"messages.0.content": "hero"}}},
		Replace:  []config.PayloadReplaceRule{rule, {Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"messages.0.content": "hero!"}}}}, Target: rule.Target, Replacements: []config.PayloadReplacement{{"!": "!!"}}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"messages.0.content": "hero!!"}}}}, Params: []string{"stage"}}},
	}}
	out, touched := ApplyPayloadConfigWithTrackedPaths(cfg, "upstream", "claude", "openai-response", "", []byte(`{"messages":[{"role":"user","content":"original"}]}`), nil, "alias-model", "", http.Header{"X-Tier": {"prod-a"}}, "messages")
	if got := ojson.GetBytes(out, "messages.0.content").String(); got != "hero!!" || ojson.GetBytes(out, "stage").Exists() || !touched["messages"] {
		t.Fatalf("unexpected stage order or tracking: %s, touched=%v", out, touched)
	}
	for _, tt := range []struct{ name, model, protocol, from, input, header string }{
		{"model", "other", "claude", "responses", `{}`, "prod-a"},
		{"protocol", "alias-model", "openai", "responses", `{}`, "prod-a"},
		{"source", "alias-model", "claude", "openai", `{}`, "prod-a"},
		{"headers", "alias-model", "claude", "responses", `{}`, "dev"},
		{"match", "alias-model", "claude", "responses", `{"stage":"wrong"}`, "prod-a"},
		{"not match", "alias-model", "claude", "responses", `{"stage":"ready","blocked":true}`, "prod-a"},
		{"not exist", "alias-model", "claude", "responses", `{"stage":"ready","disabled":false}`, "prod-a"},
		{"exist", "alias-model", "claude", "responses", `{"stage":"ready"}`, "prod-a"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDocString(tt.input)
			if tt.name != "exist" {
				_ = doc.Set("messages", []any{map[string]any{"role": "user", "content": "hero"}})
			}
			body := doc.Bytes()
			out := ApplyPayloadConfigWithRequest(&config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{rule}}}, tt.model, tt.protocol, tt.from, "", body, nil, "", "", http.Header{"X-Tier": {tt.header}})
			if !bytes.Equal(out, body) {
				t.Fatalf("nonmatching rule modified body: %s", out)
			}
		})
	}
}

func TestPayloadReplaceFinalizerRetryAndFilter(t *testing.T) {
	raw := []byte(`{"input":[{"role":"user","content":"hero"}]}`)
	cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Role: "user", All: true}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}}}}}}
	finalize := NewPayloadFinalizer(cfg, "codex", "model", "codex", "", raw, cliproxyexecutor.Request{Model: "model", Payload: raw}, cliproxyexecutor.Options{})
	for attempt := 0; attempt < 3; attempt++ {
		doc := ojson.NewDoc(append([]byte(nil), raw...))
		FinalizePayloadDoc(WithPayloadFinalizer(context.Background(), finalize), doc)
		if got := doc.Get("input.0.content").String(); got != "hero!" {
			t.Fatalf("attempt %d: %q", attempt, got)
		}
		if out := finalize(append([]byte(nil), raw...)); ojson.GetBytes(out, "input.0.content").String() != "hero!" {
			t.Fatalf("byte finalizer differs: %s", out)
		}
	}
	if ojson.GetBytes(raw, "input.0.content").String() != "hero" {
		t.Fatal("finalizer mutated original request")
	}
	cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"input"}}}
	if out := finalize(raw); ojson.GetBytes(out, "input").Exists() {
		t.Fatalf("filter did not run last: %s", out)
	}
}

func TestPayloadReplaceRegexPatterns(t *testing.T) {
	for _, tt := range []struct {
		name, protocol, root, input string
		positions                   []int
		all                         bool
		replacements                []config.PayloadReplacement
		want                        map[string]string
	}{
		{
			name:     "capture groups dollar escape and mixed literal cascading",
			protocol: "openai",
			input: `{"messages":[{"role":"system","content":"user-12 hero"},{"role":"user","content":"User-42 HERO"},{"role":"assistant","content":"user-99"},{"role":"user","content":"user-7 <debug>drop
line</debug> keep"}]}`,
			positions: []int{1, 2},
			replacements: []config.PayloadReplacement{
				{"pattern": `(?i)user-(?P<id>\d+)`, "replacement": "acct-${id}($$1)"},
				{"pattern": `(?s)<debug>.*?</debug>\s*`, "replacement": ""},
				{"HERO": "friend"},
				{"$literal": "ignored"},
			},
			want: map[string]string{
				"messages.0.content": "user-12 hero",
				"messages.1.content": "acct-42($1) friend",
				"messages.2.content": "user-99",
				"messages.3.content": "acct-7($1) keep",
			},
		},
		{
			name:      "literal key named pattern remains literal",
			protocol:  "claude",
			input:     `{"messages":[{"role":"user","content":[{"type":"text","text":"pattern A"},{"type":"thinking","text":"pattern A"}]}]}`,
			positions: []int{1},
			replacements: []config.PayloadReplacement{
				{"pattern": "schema"},
			},
			want: map[string]string{
				"messages.0.content.0.text": "schema A",
				"messages.0.content.1.text": "pattern A",
			},
		},
		{
			name:     "responses and antigravity layouts respect user selection and block boundaries",
			protocol: "antigravity",
			root:     "request",
			input:    `{"request":{"contents":[{"role":"model","parts":[{"text":"id-1"}]},{"role":"user","parts":[{"text":"id-1 "},{"text":"id-2"},{"text":"id-3","thought":true}]}]},"text":"id-1"}`,
			all:      true,
			replacements: []config.PayloadReplacement{
				{"pattern": `id-(\d+)`, "replacement": "ref-$1"},
				{"pattern": `ref-1 ref-2`, "replacement": "CROSS_BLOCK"},
			},
			want: map[string]string{
				"request.contents.0.parts.0.text": "id-1",
				"request.contents.1.parts.0.text": "ref-1 ",
				"request.contents.1.parts.1.text": "ref-2",
				"request.contents.1.parts.2.text": "id-3",
				"text":                            "id-1",
			},
		},
		{
			name:     "codex responses string and array input",
			protocol: "codex",
			input:    `{"input":[{"type":"function_call_output","role":"user","content":"secret-1"},{"type":"message","role":"user","content":[{"type":"input_text","text":"secret-1 secret-2"},{"type":"input_image","image_url":"secret-3"}]}]}`,
			all:      true,
			replacements: []config.PayloadReplacement{
				{"pattern": `secret-\d+`, "replacement": "[redacted]"},
			},
			want: map[string]string{
				"input.0.content":             "secret-1",
				"input.1.content.0.text":      "[redacted] [redacted]",
				"input.1.content.1.image_url": "secret-3",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
				Models:       []config.PayloadModelRule{{Name: "*"}},
				Target:       config.PayloadReplaceTarget{Role: "user", Positions: tt.positions, All: tt.all},
				Replacements: tt.replacements,
			}}}}
			out := ApplyPayloadConfigWithRoot(cfg, "model", tt.protocol, tt.root, []byte(tt.input), nil, "", "")
			for path, want := range tt.want {
				if got := ojson.GetBytes(out, path).String(); got != want {
					t.Errorf("%s = %q, want %q; body: %s", path, got, want, out)
				}
			}
		})
	}

	// No-op regex and invalid regex rules must leave input bytes untouched.
	for _, replacements := range [][]config.PayloadReplacement{
		{{"pattern": `nomatch-\d+`, "replacement": "x"}},
		{{"pattern": `hero`, "replacement": "hero"}},
		{{"pattern": `[invalid`, "replacement": "x"}},
	} {
		body := []byte(`{"messages":[{"role":"user","content":"hero"}]}`)
		cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
			Models:       []config.PayloadModelRule{{Name: "*"}},
			Target:       config.PayloadReplaceTarget{Role: "user", All: true},
			Replacements: replacements,
		}}}}
		out := ApplyPayloadConfigWithRoot(cfg, "model", "openai", "", body, nil, "", "")
		if !bytes.Equal(out, body) || &out[0] != &body[0] {
			t.Fatalf("expected unchanged slice for %v, got %s", replacements, out)
		}
	}
}
