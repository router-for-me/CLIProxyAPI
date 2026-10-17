package helps

import (
	"bytes"
	"testing"
)

func TestStripClaudeRequestMarkers(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "string billing and binding",
			body: `{"system":"x-anthropic-billing-header: cc_version=2.1.302.db3; cc_entrypoint=cli; cch=12345;","thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"},"display":"updates"}}`,
			want: `{"system":"x-anthropic-billing-header: cc_version=2.1.302.db3; cc_entrypoint=cli;","thinking":{"type":"adaptive","display":"updates"}}`,
		},
		{
			name: "all billing blocks and remaining tags",
			body: `{"system":[{"type":"text","text":"Keep cch=12345; in this instruction."},{"type":"text","text":"x-anthropic-billing-header: cc_version=caller; cch=12345; cc_entrypoint=cli; cc_prompt_id=keep;","cache_control":{"type":"ephemeral"}},{"type":"text","text":"x-anthropic-billing-header: cch=abc; cc_version=other; cch=def; cc_prev_req=keep;"}],"messages":[{"role":"user","content":"cch=12345; block_binding"}]}`,
			want: `{"system":[{"type":"text","text":"Keep cch=12345; in this instruction."},{"type":"text","text":"x-anthropic-billing-header: cc_version=caller; cc_entrypoint=cli; cc_prompt_id=keep;","cache_control":{"type":"ephemeral"}},{"type":"text","text":"x-anthropic-billing-header: cc_version=other; cc_prev_req=keep;"}],"messages":[{"role":"user","content":"cch=12345; block_binding"}]}`,
		},
		{
			name: "whitespace and following prompt lines",
			body: `{"system":"\n\t x-anthropic-billing-header: cc_version=caller;\tcch=any-value; cc_entrypoint=cli;\nKeep cch=12345; here."}`,
			want: `{"system":"\n\t x-anthropic-billing-header: cc_version=caller; cc_entrypoint=cli;\nKeep cch=12345; here."}`,
		},
		{
			name: "no trailing semicolon",
			body: `{"system":"x-anthropic-billing-header: cc_version=caller; cch=12345"}`,
			want: `{"system":"x-anthropic-billing-header: cc_version=caller;"}`,
		},
		{
			name: "unrelated fields preserved",
			body: `{"system":"A literal x-anthropic-billing-header: cch=12345;","tools":[{"input_schema":{"properties":{"block_binding":{"type":"object"}}}}],"thinking":{"type":"adaptive"}}`,
			want: `{"system":"A literal x-anthropic-billing-header: cch=12345;","tools":[{"input_schema":{"properties":{"block_binding":{"type":"object"}}}}],"thinking":{"type":"adaptive"}}`,
		},
		{
			name: "binding removed without system",
			body: `{"thinking":{"type":"enabled","budget_tokens":1024,"block_binding":{"prefix_mismatch_behavior":"error"}}}`,
			want: `{"thinking":{"type":"enabled","budget_tokens":1024}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := StripClaudeRequestMarkers([]byte(tt.body))
			if string(got) != tt.want {
				t.Fatalf("body = %s, want %s", got, tt.want)
			}
			if again := StripClaudeRequestMarkers(got); !bytes.Equal(again, got) {
				t.Fatalf("cleanup is not idempotent: %s", again)
			}
		})
	}
}
