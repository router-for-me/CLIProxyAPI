package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestIsClaudeAutoModeClassifierRequest(t *testing.T) {
	const prompt = "You are a security monitor for autonomous AI coding agents. Judge the action."
	const billing = `{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.304.a80;"}`
	const userOnly = `[{"role":"user","content":"CLAUDE.md"},{"role":"user","content":"<transcript/>"}]`

	tests := []struct {
		name     string
		system   string
		messages string
		want     bool
	}{
		{name: "classifier blocks", system: `[` + billing + `,{"type":"text","text":"` + prompt + `"}]`, messages: userOnly, want: true},
		{name: "classifier string", system: `"` + prompt + `"`, messages: userOnly, want: true},
		{name: "sentence quoted after another block", system: `[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"` + prompt + `"}]`, messages: userOnly},
		{name: "assistant turn present", system: `[` + billing + `,{"type":"text","text":"` + prompt + `"}]`, messages: `[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]`},
		{name: "no messages", system: `"` + prompt + `"`, messages: `[]`},
		{name: "other prompt", system: `"You are Claude Code."`, messages: userOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			system := ojson.Parse(tt.system)
			messages := ojson.Parse(tt.messages)
			if got := IsClaudeAutoModeClassifierRequest(system, messages); got != tt.want {
				t.Fatalf("IsClaudeAutoModeClassifierRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}
