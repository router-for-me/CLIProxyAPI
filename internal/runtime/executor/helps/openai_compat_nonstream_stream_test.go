package helps

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestShouldForceNonStreamToolCalls(t *testing.T) {
	withTools := []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"read"}}]}`)
	withoutTools := []byte(`{"model":"m"}`)
	emptyTools := []byte(`{"model":"m","tools":[]}`)

	tests := []struct {
		name    string
		compat  *config.OpenAICompatibility
		payload []byte
		want    bool
	}{
		{"nil compat", nil, withTools, false},
		{"disabled", &config.OpenAICompatibility{}, withTools, false},
		{"enabled with tools", &config.OpenAICompatibility{NonStreamToolCalls: true}, withTools, true},
		{"enabled without tools", &config.OpenAICompatibility{NonStreamToolCalls: true}, withoutTools, false},
		{"enabled empty tools", &config.OpenAICompatibility{NonStreamToolCalls: true}, emptyTools, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldForceNonStreamToolCalls(tc.compat, tc.payload); got != tc.want {
				t.Fatalf("ShouldForceNonStreamToolCalls = %v, want %v", got, tc.want)
			}
		})
	}
}

// decodeFrames parses every synthesized frame except the [DONE] sentinel.
func decodeFrames(t *testing.T, frames []string) []map[string]any {
	t.Helper()
	if len(frames) == 0 {
		t.Fatal("no frames produced")
	}
	if frames[len(frames)-1] != "[DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", frames[len(frames)-1])
	}
	out := make([]map[string]any, 0, len(frames)-1)
	for _, frame := range frames[:len(frames)-1] {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(frame), &parsed); err != nil {
			t.Fatalf("frame %q is not valid JSON: %v", frame, err)
		}
		if parsed["object"] != "chat.completion.chunk" {
			t.Fatalf("frame object = %v, want chat.completion.chunk", parsed["object"])
		}
		out = append(out, parsed)
	}
	return out
}

func TestSynthesizeOpenAIStreamBootstrapFrame(t *testing.T) {
	frame := SynthesizeOpenAIStreamBootstrapFrame("chatcmpl-stable", "alias-model", 1700000000)
	for _, want := range []string{`"role":"assistant"`, `"model":"alias-model"`, `"id":"chatcmpl-stable"`, `"created":1700000000`} {
		if !strings.Contains(frame, want) {
			t.Fatalf("bootstrap frame missing %s: %s", want, frame)
		}
	}
}

func TestSynthesizeOpenAIStreamFramesToolCalls(t *testing.T) {
	body := []byte(`{
		"id":"msg_1","model":"claude-opus-5","created":1700000000,
		"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"config.yaml\"}"}}
		]}}],
		"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}
	}`)

	frames := SynthesizeOpenAIStreamFrames(body)
	parsed := decodeFrames(t, frames)

	joined := strings.Join(frames, "\n")
	if !strings.Contains(joined, `"arguments":"{\"path\":\"config.yaml\"}"`) {
		t.Fatalf("tool call arguments missing from frames: %s", joined)
	}

	for _, frame := range parsed {
		if frame["id"] != "msg_1" || frame["model"] != "claude-opus-5" {
			t.Fatalf("frame lost id/model: %v", frame)
		}
	}

	last := parsed[len(parsed)-1]
	choice := last["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v, want tool_calls", choice["finish_reason"])
	}
	if _, ok := last["usage"]; !ok {
		t.Fatal("terminal frame is missing the usage block")
	}

	// Only the terminal frame may carry the finish reason.
	for _, frame := range parsed[:len(parsed)-1] {
		ch := frame["choices"].([]any)[0].(map[string]any)
		if ch["finish_reason"] != nil {
			t.Fatalf("intermediate frame carries finish_reason: %v", frame)
		}
	}
}

func TestSynthesizeOpenAIStreamFramesContentOnly(t *testing.T) {
	body := []byte(`{"id":"msg_2","model":"m","created":1,"choices":[{"index":0,"finish_reason":"stop",
		"message":{"role":"assistant","content":"hola"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)

	frames := SynthesizeOpenAIStreamFrames(body)
	parsed := decodeFrames(t, frames)

	if !strings.Contains(strings.Join(frames, "\n"), `"content":"hola"`) {
		t.Fatalf("content missing from frames: %v", frames)
	}
	last := parsed[len(parsed)-1]
	choice := last["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v, want stop", choice["finish_reason"])
	}
}

func TestSynthesizeOpenAIStreamFramesPreservesRefusal(t *testing.T) {
	body := []byte(`{"id":"msg_r","model":"m","created":1,"choices":[{"index":0,"finish_reason":"stop",
		"message":{"role":"assistant","content":null,"refusal":"I cannot help with that."}}]}`)
	frames := SynthesizeOpenAIStreamFrames(body)
	decodeFrames(t, frames)
	if !strings.Contains(strings.Join(frames, "\n"), `"refusal":"I cannot help with that."`) {
		t.Fatalf("refusal missing from frames: %v", frames)
	}
}

func TestSynthesizeOpenAIStreamFramesMultipleToolCalls(t *testing.T) {
	body := []byte(`{"id":"msg_3","model":"m","created":1,"choices":[{"index":0,"finish_reason":"tool_calls",
		"message":{"role":"assistant","tool_calls":[
			{"id":"a","type":"function","function":{"name":"read","arguments":"{}"}},
			{"id":"b","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}
		]}}]}`)

	frames := SynthesizeOpenAIStreamFrames(body)
	decodeFrames(t, frames)

	joined := strings.Join(frames, "\n")
	for _, want := range []string{`"name":"read"`, `"name":"bash"`, `"index":0`, `"index":1`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("frames missing %s: %s", want, joined)
		}
	}
}

func TestSynthesizeOpenAIStreamFramesUsageOnceWithMultipleChoices(t *testing.T) {
	body := []byte(`{"id":"msg_5","model":"m","created":1,"choices":[
		{"finish_reason":"stop","message":{"role":"assistant","content":"a"}},
		{"finish_reason":"stop","message":{"role":"assistant","content":"b"}}
	],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)

	frames := SynthesizeOpenAIStreamFrames(body)
	parsed := decodeFrames(t, frames)

	usageFrames := 0
	for _, frame := range parsed {
		if _, ok := frame["usage"]; ok {
			usageFrames++
		}
	}
	// Both choices omit "index", so a value-based guard would emit usage twice.
	if usageFrames != 1 {
		t.Fatalf("usage appears in %d frames, want exactly 1", usageFrames)
	}
}

func TestSynthesizeOpenAIStreamFramesRejectsUnusableBodies(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"choices":[]}`, `not json`} {
		if frames := SynthesizeOpenAIStreamFrames([]byte(body)); len(frames) != 0 {
			t.Fatalf("body %q produced frames %v, want none", body, frames)
		}
	}
}
