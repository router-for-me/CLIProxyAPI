package helps

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestStripAntigravityCLIIdentity(t *testing.T) {
	const identity = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	quote := func(text string) string {
		raw, errMarshal := json.Marshal(text)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		return string(raw)
	}
	textPart := func(text string) string { return `{"text":` + quote(text) + `}` }
	wrap := func(parts string) []byte {
		system := ""
		if parts != "" {
			system = `,"systemInstruction":{"role":"user","parts":` + parts + `}`
		}
		return []byte(`{ "model":"gemini-3-flash", "request":{"contents":[{"role":"user","parts":[` + textPart(identity) + `]}],"tools":[{"functionDeclarations":[{"name":"read","parameters":{"type":"object"}}]}],"generationConfig":{"temperature":0.25}` + system + `}, "extra":true }`)
	}
	cases := []struct {
		name      string
		parts     string
		wantParts string
		changed   bool
	}{
		{"suffix including whitespace", `[` + textPart(identity+" \t\r\n\n  Body\n") + `]`, `[` + textPart(" \t\r\n\n  Body\n") + `]`, true},
		{"identity only", `[` + textPart(identity) + `]`, "", true},
		{"whitespace suffix retained", `[` + textPart(identity+" \t\r\n") + `]`, `[` + textPart(" \t\r\n") + `]`, true},
		{"empty part retained", `[{"text":""},` + textPart(identity+"\nBody") + `]`, `[{"text":""},` + textPart("\nBody") + `]`, true},
		{"non-text part retained", `[{"inlineData":{"mimeType":"image/png","data":"AA=="}},` + textPart(identity+"Body") + `]`, `[{"inlineData":{"mimeType":"image/png","data":"AA=="}},{"text":"Body"}]`, true},
		{"identity part removed only", `[{"text":""},` + textPart(identity) + `,{"text":"Body"}]`, `[{"text":""},{"text":"Body"}]`, true},
		{"later occurrence retained", `[` + textPart(identity+"\nBody") + `,` + textPart(identity) + `]`, `[` + textPart("\nBody") + `,` + textPart(identity) + `]`, true},
		{"matched part metadata retained", `[{"text":` + quote(identity+"\nBody") + `,"marker":42}]`, `[{"text":"\nBody","marker":42}]`, true},
		{"default identity", `[` + textPart("You are Claude Code, Anthropic's official CLI for Claude.") + `]`, "", false},
		{"preset identity", `[` + textPart("You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.") + `]`, "", false},
		{"ordinary first part", `[{"text":"Intro"},` + textPart(identity) + `]`, "", false},
		{"mid-text", `[` + textPart("Intro\n"+identity) + `]`, "", false},
		{"leading whitespace", `[` + textPart("\n"+identity) + `]`, "", false},
		{"split identity", `[` + textPart(identity[:20]) + `,` + textPart(identity[20:]) + `]`, "", false},
		{"empty text", `[{"text":""}]`, "", false},
		{"empty parts", `[]`, "", false},
		{"missing system", "", "", false},
		{"non-array parts", `{"text":` + quote(identity) + `}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := wrap(tc.parts)
			original := bytes.Clone(body)
			got := StripAntigravityCLIIdentity("gemini-3-flash", body)
			want := original
			if tc.changed {
				want = wrap(tc.wantParts)
			} else if !bytes.Equal(got, original) {
				t.Fatalf("unmatched body changed: %s", got)
			}
			var gotJSON, wantJSON any
			if errParse := json.Unmarshal(got, &gotJSON); errParse != nil {
				t.Fatal(errParse)
			}
			if errParse := json.Unmarshal(want, &wantJSON); errParse != nil {
				t.Fatal(errParse)
			}
			if !reflect.DeepEqual(gotJSON, wantJSON) {
				t.Fatalf("got %s, want %s", got, want)
			}
			if !bytes.Equal(body, original) {
				t.Fatal("input buffer mutated")
			}
			if again := StripAntigravityCLIIdentity("gemini-3-flash", got); !bytes.Equal(again, got) {
				t.Fatalf("second normalization changed result: %s", again)
			}
		})
	}
	for _, model := range []string{"claude-sonnet-4-6", "gemini-3-pro", "gemini-3-flash-preview", "gemini-3-flash-image", "models/gemini-3-flash", ""} {
		t.Run("other model/"+model, func(t *testing.T) {
			body := wrap(`[` + textPart(identity) + `]`)
			if got := StripAntigravityCLIIdentity(model, body); !bytes.Equal(got, body) {
				t.Fatalf("model %q changed: %s", model, got)
			}
		})
	}
}
