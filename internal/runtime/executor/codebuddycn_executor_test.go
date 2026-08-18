package executor

import (
	"testing"

	"github.com/tidwall/gjson"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestApplyCodeBuddyCNReasoning(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantEff  bool // reasoning_effort should still be present
		wantSumm string
	}{
		{
			name:     "reasoning_effort present maps to reasoning_summary",
			body:     `{"model":"glm-5.2","reasoning_effort":"high"}`,
			wantEff:  false,
			wantSumm: "auto",
		},
		{
			name:     "reasoning_effort none drops field",
			body:     `{"model":"glm-5.2","reasoning_effort":"none"}`,
			wantEff:  false,
			wantSumm: "",
		},
		{
			name:     "reasoning_effort off drops field",
			body:     `{"model":"glm-5.2","reasoning_effort":"off"}`,
			wantEff:  false,
			wantSumm: "",
		},
		{
			name:     "absent reasoning_effort leaves body untouched",
			body:     `{"model":"glm-5.2"}`,
			wantEff:  false,
			wantSumm: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyCodeBuddyCNReasoning([]byte(tt.body))
			if eff := gjson.GetBytes(got, "reasoning_effort"); eff.Exists() != tt.wantEff {
				t.Fatalf("reasoning_effort exists=%v, want %v (body=%s)", eff.Exists(), tt.wantEff, got)
			}
			if summ := gjson.GetBytes(got, "reasoning_summary").String(); summ != tt.wantSumm {
				t.Fatalf("reasoning_summary=%q, want %q (body=%s)", summ, tt.wantSumm, got)
			}
		})
	}
}

func TestApplyCodeBuddyCNAgentSystemPrompt(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantRepl bool // top-level system should be replaced
		wantMsgs bool // messages system should be replaced
	}{
		{
			name:     "top-level agent system replaced",
			body:     `{"system":"You are Claude Code, Anthropic's official CLI for software engineering"}`,
			wantRepl: true,
		},
		{
			name:     "top-level benign system untouched",
			body:     `{"system":"You are a translator."}`,
			wantRepl: false,
		},
		{
			name:     "messages system agent replaced",
			body:     `{"messages":[{"role":"system","content":"You are Cursor, an AI coding agent"}]}`,
			wantMsgs: true,
		},
		{
			name:     "messages system benign untouched",
			body:     `{"messages":[{"role":"system","content":"Summarize this text."}]}`,
			wantMsgs: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyCodeBuddyCNAgentSystemPrompt([]byte(tt.body))

			if sys := gjson.GetBytes(got, "system"); sys.Exists() {
				if sys.String() == neutralSystemPrompt != tt.wantRepl {
					t.Fatalf("system=%q, wantRepl=%v (body=%s)", sys.String(), tt.wantRepl, got)
				}
			}

			msgs := gjson.GetBytes(got, "messages")
			if msgs.Exists() && msgs.IsArray() {
				first := msgs.Array()[0].Get("content")
				if first.String() == neutralSystemPrompt != tt.wantMsgs {
					t.Fatalf("messages[0].content=%q, wantMsgs=%v (body=%s)", first.String(), tt.wantMsgs, got)
				}
			}
		})
	}
}

func TestApplyCodeBuddyCNOutgoingTransformsForcesStream(t *testing.T) {
	e := NewCodeBuddyCNExecutor(nil)
	got := e.applyOutgoingTransforms(nil, nil, "glm-5.2", cliproxyexecutor.Options{}, []byte(`{"model":"glm-5.2"}`))
	if !gjson.GetBytes(got, "stream").Bool() {
		t.Fatalf("stream should be forced true, got body=%s", got)
	}
}
