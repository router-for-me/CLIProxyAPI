package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithRoot_OverrideQueryPath(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: map[string]any{
						`tools.#(type=="custom").description`: "patched",
					},
				},
			},
		},
	}
	input := []byte(`{"tools":[{"type":"function","name":"alpha"},{"type":"custom","name":"beta"},{"type":"custom","name":"gamma"}]}`)
	out := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6", "claude", "", input, nil, "")

	if got := gjson.GetBytes(out, "tools.0.description"); got.Exists() {
		t.Fatalf("tools.0.description should not exist, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.1.description").String(); got != "patched" {
		t.Fatalf("tools.1.description = %q, want %q", got, "patched")
	}
	if got := gjson.GetBytes(out, "tools.2.description").String(); got != "patched" {
		t.Fatalf("tools.2.description = %q, want %q", got, "patched")
	}
}

func TestApplyPayloadConfigWithRoot_OverrideRawQueryPath(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			OverrideRaw: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: map[string]any{
						`tools.#(type=="custom").input_schema`: `{"type":"object","properties":{"command":{"type":"string"}}}`,
					},
				},
			},
		},
	}
	input := []byte(`{"tools":[{"type":"function","name":"alpha"},{"type":"custom","name":"beta"}]}`)
	out := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6", "claude", "", input, nil, "")

	if got := gjson.GetBytes(out, "tools.0.input_schema"); got.Exists() {
		t.Fatalf("tools.0.input_schema should not exist, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.type").String(); got != "object" {
		t.Fatalf("tools.1.input_schema.type = %q, want %q", got, "object")
	}
	if got := gjson.GetBytes(out, "tools.1.input_schema.properties.command.type").String(); got != "string" {
		t.Fatalf("tools.1.input_schema.properties.command.type = %q, want %q", got, "string")
	}
}

func TestApplyPayloadConfigWithRoot_FilterQueryPath_RemovesMatchingElements(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: []string{`tools.#(type=="custom")`},
				},
			},
		},
	}
	input := []byte(`{"tools":[{"type":"function","name":"alpha"},{"type":"custom","name":"beta"},{"type":"custom","name":"gamma"}]}`)
	out := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6", "claude", "", input, nil, "")

	tools := gjson.GetBytes(out, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("tools should be an array, got %s", tools.Raw)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("tools length = %d, want 1", len(arr))
	}
	if got := arr[0].Get("name").String(); got != "alpha" {
		t.Fatalf("tools.0.name = %q, want %q", got, "alpha")
	}
}

func TestApplyPayloadConfigWithRoot_FilterQueryPath_RemovesNestedField(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: []string{`tools.#(type=="custom").format`},
				},
			},
		},
	}
	input := []byte(`{"tools":[{"type":"custom","name":"beta","format":{"syntax":"lark"}},{"type":"function","name":"alpha","format":{"syntax":"json"}}]}`)
	out := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6", "claude", "", input, nil, "")

	if got := gjson.GetBytes(out, "tools.0.format"); got.Exists() {
		t.Fatalf("tools.0.format should be removed, got %s", got.Raw)
	}
	if got := gjson.GetBytes(out, "tools.1.format.syntax").String(); got != "json" {
		t.Fatalf("tools.1.format.syntax = %q, want %q", got, "json")
	}
}

func TestApplyPayloadConfigWithRoot_OverrideNestedQueryPath(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: map[string]any{
						`messages.#(role=="assistant").content.#(type=="tool_use").name`: "patched",
					},
				},
			},
		},
	}
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"a"},{"type":"text","text":"ok"}]},{"role":"user","content":[{"type":"tool_use","name":"b"}]}]}`)
	out := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6", "claude", "", input, nil, "")

	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "patched" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "patched")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.name").String(); got != "b" {
		t.Fatalf("messages.1.content.0.name = %q, want %q", got, "b")
	}
}

func TestApplyPayloadConfigWithRoot_OverrideRawNestedQueryPath(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			OverrideRaw: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: map[string]any{
						`messages.#(role=="assistant").content.#(type=="tool_use").input`: `{"command":"run"}`,
					},
				},
			},
		},
	}
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"a","input":{}},{"type":"text","text":"ok"}]},{"role":"assistant","content":[{"type":"tool_use","name":"b","input":{}}]}]}`)
	out := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6", "claude", "", input, nil, "")

	if got := gjson.GetBytes(out, "messages.0.content.0.input.command").String(); got != "run" {
		t.Fatalf("messages.0.content.0.input.command = %q, want %q", got, "run")
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.input.command").String(); got != "run" {
		t.Fatalf("messages.1.content.0.input.command = %q, want %q", got, "run")
	}
}

func TestApplyPayloadConfigWithRoot_FilterNestedQueryPath_RemovesMatchingNestedElements(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: []string{`messages.#(role=="assistant").content.#(type=="tool_use")`},
				},
			},
		},
	}
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"a"},{"type":"text","text":"ok"},{"type":"tool_use","name":"b"}]},{"role":"user","content":[{"type":"tool_use","name":"c"}]}]}`)
	out := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6", "claude", "", input, nil, "")

	if got := gjson.GetBytes(out, "messages.0.content.#").Int(); got != 1 {
		t.Fatalf("messages.0.content length = %d, want 1", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "text" {
		t.Fatalf("messages.0.content.0.type = %q, want %q", got, "text")
	}
	if got := gjson.GetBytes(out, "messages.1.content.#").Int(); got != 1 {
		t.Fatalf("messages.1.content length = %d, want 1", got)
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.name").String(); got != "c" {
		t.Fatalf("messages.1.content.0.name = %q, want %q", got, "c")
	}
}

func TestApplyPayloadConfigWithRoot_InvalidQueryPath_NoOp(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-*", Protocol: "claude"}},
					Params: []string{`tools.#(type=="custom"`},
				},
			},
		},
	}
	input := []byte(`{"tools":[{"type":"custom","name":"beta"},{"type":"function","name":"alpha"}]}`)
	out := applyPayloadConfigWithRoot(cfg, "claude-opus-4-6", "claude", "", input, nil, "")

	if string(out) != string(input) {
		t.Fatalf("invalid query path should leave payload unchanged\ngot:  %s\nwant: %s", string(out), string(input))
	}
}
