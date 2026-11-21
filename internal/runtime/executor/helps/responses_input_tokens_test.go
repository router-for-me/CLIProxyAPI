package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/tiktoken-go/tokenizer"
)

func TestCountResponsesInputTokensMessageForms(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	const text = "\n\n    Привет 世界\t\n"
	var reference int64
	for i, payload := range []string{
		`{"input":[{"type":"message","role":"user","content":"PLACEHOLDER"}]}`,
		`{"input":[{"role":"user","content":"PLACEHOLDER"}]}`,
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"PLACEHOLDER"}]}]}`,
	} {
		doc := ojson.NewDocString(payload)
		path := "input.0.content"
		if i == 2 {
			path += ".0.text"
		}
		if err := doc.Set(path, text); err != nil {
			t.Fatal(err)
		}
		got, err := CountResponsesInputTokens(enc, doc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			reference = got
		} else if got != reference {
			t.Fatalf("form %d: got %d, want %d", i, got, reference)
		}
	}
	expected, err := enc.Count("user\n" + text)
	if err != nil {
		t.Fatal(err)
	}
	if reference != int64(expected) {
		t.Fatalf("whitespace not preserved: got %d want %d", reference, expected)
	}
}

func TestCountResponsesInputTokensShorthand(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"hello", "   \n\t", "Привет 中文"} {
		doc := ojson.NewDocString(`{}`)
		if err := doc.Set("input", text); err != nil {
			t.Fatal(err)
		}
		got, err := CountResponsesInputTokens(enc, doc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		want, err := enc.Count(text)
		if err != nil {
			t.Fatal(err)
		}
		if got != int64(want) || got == 0 {
			t.Fatalf("shorthand %q: %d, want %d", text, got, want)
		}
	}
}

func TestCountResponsesInputTokensCountsToolOutputsAndDefinitions(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	const text = "A tool result with important retained context."
	for _, payload := range []string{
		`{"input":[{"type":"function_call_output","call_id":"ignored","output":"` + text + `"}]}`,
		`{"input":[{"type":"function_call_output","output":[{"type":"input_text","text":"` + text + `"}]}]}`,
		`{"input":[{"type":"custom_tool_call_output","output":"` + text + `"}]}`,
	} {
		got, err := CountResponsesInputTokens(enc, []byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		want, err := enc.Count(text)
		if err != nil {
			t.Fatal(err)
		}
		if got != int64(want) {
			t.Fatalf("tool output %s: %d, want %d", payload, got, want)
		}
	}
	base := []byte(`{"input":"hello"}`)
	doc := ojson.NewDoc(base)
	if err := doc.Set("tools", []any{map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": "custom", "name": "exec", "description": strings.Repeat(text, 100)}}}}); err != nil {
		t.Fatal(err)
	}
	before, err := CountResponsesInputTokens(enc, base)
	if err != nil {
		t.Fatal(err)
	}
	after, err := CountResponsesInputTokens(enc, doc.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if after <= before+100 {
		t.Fatalf("nested tools ignored: %d -> %d", before, after)
	}
}

func TestCountResponsesInputTokensIgnoresControlFields(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	const base = `{"input":"hello"}`
	doc := ojson.NewDocString(base)
	for key, value := range map[string]any{"metadata": map[string]any{"large": strings.Repeat("noise", 10000)}, "prompt_cache_key": "ignored", "model": "gpt-6.1-sol", "max_output_tokens": 1000, "stream": true} {
		if err := doc.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	before, err := CountResponsesInputTokens(enc, []byte(base))
	if err != nil {
		t.Fatal(err)
	}
	after, err := CountResponsesInputTokens(enc, doc.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("control fields changed estimate: %d -> %d", before, after)
	}
	if _, err := CountResponsesInputTokens(nil, []byte(base)); err == nil {
		t.Fatal("nil codec accepted")
	}
	if _, err := CountResponsesInputTokens(enc, []byte(`{broken`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
