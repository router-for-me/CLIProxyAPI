package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestIsClaudeAutoModeClassifierRequest(t *testing.T) {
	const prompt = "You are a security monitor for autonomous AI coding agents. Judge the action."
	const billing = `{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.304.a80;"}`
	const blocks = `[` + billing + `,{"type":"text","text":"` + prompt + `"}]`
	const userOnly = `[{"role":"user","content":"CLAUDE.md"},{"role":"user","content":"<transcript/>"}]`
	const tool = `{"name":"inspect","input_schema":{"type":"object"}}`

	tests := []struct {
		name     string
		system   string
		messages string
		extra    string
		want     bool
	}{
		{name: "classifier blocks", system: blocks, messages: userOnly, want: true},
		{name: "classifier string", system: `"` + prompt + `"`, messages: userOnly, want: true},
		{name: "empty tools array", system: blocks, messages: userOnly, extra: `,"tools":[]`, want: true},
		{name: "tools present", system: blocks, messages: userOnly, extra: `,"tools":[` + tool + `]`},
		{name: "tool_choice present", system: blocks, messages: userOnly, extra: `,"tool_choice":{"type":"auto"}`},
		{name: "forced tool", system: blocks, messages: userOnly, extra: `,"tools":[` + tool + `],"tool_choice":{"type":"tool","name":"inspect"}`},
		{name: "sentence quoted after another block", system: `[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"` + prompt + `"}]`, messages: userOnly},
		{name: "assistant turn present", system: blocks, messages: `[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]`},
		{name: "no messages", system: `"` + prompt + `"`, messages: `[]`},
		{name: "other prompt", system: `"You are Claude Code."`, messages: userOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDocString(`{"system":` + tt.system + `,"messages":` + tt.messages + tt.extra + `}`)
			if got := IsClaudeAutoModeClassifierRequest(doc); got != tt.want {
				t.Fatalf("IsClaudeAutoModeClassifierRequest() = %v, want %v", got, tt.want)
			}
		})
	}
	if IsClaudeAutoModeClassifierRequest(nil) {
		t.Fatal("nil doc matched")
	}
}
