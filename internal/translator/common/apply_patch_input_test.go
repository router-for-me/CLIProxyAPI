package common

import (
	"strings"
	"testing"
	"unicode/utf8"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
)

func TestApplyPatchInputDecoderEverySplit(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: a.txt\n+中文 \\\"\n*** End Patch\n"
	arguments := applypatch.WrapInput(patch)
	for split := 0; split <= len(arguments); split++ {
		var decoder ApplyPatchInputDecoder
		var output strings.Builder
		for _, fragment := range []string{arguments[:split], arguments[split:]} {
			delta, errPush := decoder.Push(fragment)
			if errPush != nil {
				t.Fatalf("split %d: %v", split, errPush)
			}
			output.WriteString(delta)
		}
		tail, errFinish := decoder.Finish(arguments)
		if errFinish != nil {
			t.Fatalf("split %d: %v", split, errFinish)
		}
		output.WriteString(tail)
		if output.String() != patch || decoder.Input() != patch {
			t.Fatalf("split %d: output %q, input %q", split, output.String(), decoder.Input())
		}
	}
}

func TestApplyPatchInputDecoderStringFragments(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "empty", arguments: `{"input":""}`},
		{name: "whitespace", arguments: " \t\r\n{ \n\"input\" \t: \"  line  \\n\\t next\\r\\n\" \r}\n\t", want: "  line  \n\t next\r\n"},
		{name: "escapes", arguments: `{"input":"\"\\\/\b\f\n\r\t\u0000\u0041\u4e2d\u6587"}`, want: "\"\\/\b\f\n\r\t\x00A中文"},
		{name: "escaped key", arguments: `{"in\u0070ut":"patch"}`, want: "patch"},
		{name: "surrogate pair", arguments: `{"input":"before\uD83D\uDE00after"}`, want: "before😀after"},
		{name: "surrogate boundaries", arguments: `{"input":"\ud800\udc00\uDBFF\uDFFF\uD7FF\uE000"}`, want: "\U00010000\U0010ffff\uD7FF\uE000"},
		{name: "utf8", arguments: `{"input":"¢中文😀�"}`, want: "¢中文😀�"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for split := 0; split <= len(test.arguments); split++ {
				checkApplyPatchFragments(t, []string{test.arguments[:split], test.arguments[split:]}, test.arguments, test.want)
				// A final snapshot can complete a pending escape or UTF-8 character.
				checkApplyPatchFragments(t, []string{test.arguments[:split]}, test.arguments, test.want)
			}
			fragments := make([]string, len(test.arguments))
			for i := 0; i < len(test.arguments); i++ {
				fragments[i] = test.arguments[i : i+1]
			}
			checkApplyPatchFragments(t, fragments, test.arguments, test.want)
		})
	}
}

func checkApplyPatchFragments(t *testing.T, fragments []string, arguments, want string) {
	t.Helper()
	var decoder ApplyPatchInputDecoder
	var output strings.Builder
	for i, fragment := range fragments {
		delta, errPush := decoder.Push(fragment)
		if errPush != nil {
			t.Fatalf("fragment %d: %v", i, errPush)
		}
		if !utf8.ValidString(delta) {
			t.Fatalf("fragment %d returned incomplete UTF-8: %q", i, delta)
		}
		output.WriteString(delta)
		if decoder.Input() != output.String() {
			t.Fatalf("fragment %d: input %q, emitted %q", i, decoder.Input(), output.String())
		}
	}
	tail, errFinish := decoder.Finish(arguments)
	if errFinish != nil {
		t.Fatal(errFinish)
	}
	output.WriteString(tail)
	if output.String() != want || decoder.Input() != want {
		t.Fatalf("output %q, input %q, want %q", output.String(), decoder.Input(), want)
	}
}

func TestApplyPatchInputDecoderPreviewBeforeClosingJSON(t *testing.T) {
	fragments := []struct {
		fragment string
		want     string
	}{
		{fragment: `{"input":"*** Begin Patch\n*** Add File: a.txt\n+  `, want: "*** Begin Patch\n*** Add File: a.txt\n+  "},
		{fragment: string([]byte{0xe4}), want: ""},
		{fragment: string([]byte{0xb8}), want: ""},
		{fragment: string([]byte{0xad}) + `\`, want: "中"},
		{fragment: `uD8`, want: ""},
		{fragment: `3D`, want: ""},
		{fragment: `\uDE`, want: ""},
		{fragment: `00\n*** End Patch\n`, want: "😀\n*** End Patch\n"},
	}
	var decoder ApplyPatchInputDecoder
	for i, fragment := range fragments {
		delta, errPush := decoder.Push(fragment.fragment)
		if errPush != nil || delta != fragment.want {
			t.Fatalf("fragment %d: delta %q, error %v, want %q", i, delta, errPush, fragment.want)
		}
	}
	want := "*** Begin Patch\n*** Add File: a.txt\n+  中😀\n*** End Patch\n"
	if decoder.Input() != want {
		t.Fatalf("open JSON input = %q, want %q", decoder.Input(), want)
	}
	tail, errFinish := decoder.Finish(applypatch.WrapInput(want))
	if errFinish != nil || tail != "" {
		t.Fatalf("finish = %q, %v", tail, errFinish)
	}
}

func TestApplyPatchInputDecoderRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
	}{
		{name: "empty"},
		{name: "array", arguments: `[]`},
		{name: "empty object", arguments: `{}`},
		{name: "wrong key", arguments: `{"patch":"x"}`},
		{name: "unquoted key", arguments: `{input:"x"}`},
		{name: "invalid key escape", arguments: `{"in\qput":"x"}`},
		{name: "missing colon", arguments: `{"input" "x"}`},
		{name: "number value", arguments: `{"input":42}`},
		{name: "null value", arguments: `{"input":null}`},
		{name: "boolean value", arguments: `{"input":true}`},
		{name: "object value", arguments: `{"input":{}}`},
		{name: "array value", arguments: `{"input":[]}`},
		{name: "duplicate key", arguments: `{"input":"x","input":"y"}`},
		{name: "extra key", arguments: `{"input":"x","extra":"y"}`},
		{name: "trailing comma", arguments: `{"input":"x",}`},
		{name: "trailing json", arguments: `{"input":"x"}{}`},
		{name: "non json whitespace", arguments: "\v{\"input\":\"x\"}"},
		{name: "invalid escape", arguments: `{"input":"\q"}`},
		{name: "invalid hex", arguments: `{"input":"\u12G4"}`},
		{name: "short unicode", arguments: `{"input":"\u123"}`},
		{name: "dangling escape", arguments: `{"input":"x\`},
		{name: "raw newline", arguments: "{\"input\":\"x\ny\"}"},
		{name: "raw nul", arguments: "{\"input\":\"x\x00y\"}"},
		{name: "low surrogate alone", arguments: `{"input":"\uDE00"}`},
		{name: "high surrogate alone", arguments: `{"input":"\uD83D"}`},
		{name: "high followed by text", arguments: `{"input":"\uD83Dx"}`},
		{name: "high followed by newline escape", arguments: `{"input":"\uD83D\n"}`},
		{name: "high followed by bmp", arguments: `{"input":"\uD83D\u0041"}`},
		{name: "two high surrogates", arguments: `{"input":"\uD83D\uD83D"}`},
		{name: "invalid utf8 continuation", arguments: "{\"input\":\"\xe4A\"}"},
		{name: "utf8 truncated by quote", arguments: "{\"input\":\"\xe4\xb8\"}"},
		{name: "overlong utf8", arguments: "{\"input\":\"\xc0\xaf\"}"},
		{name: "utf8 encoded surrogate", arguments: "{\"input\":\"\xed\xa0\x80\"}"},
		{name: "utf8 beyond maximum", arguments: "{\"input\":\"\xf4\x90\x80\x80\"}"},
		{name: "unclosed value", arguments: `{"input":"x`},
		{name: "unclosed object", arguments: `{"input":"x"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for split := 0; split <= len(test.arguments); split++ {
				var decoder ApplyPatchInputDecoder
				for _, fragment := range []string{test.arguments[:split], test.arguments[split:]} {
					_, errPush := decoder.Push(fragment)
					if errPush != nil {
						break
					}
				}
				if _, errFinish := decoder.Finish(test.arguments); errFinish == nil {
					t.Fatalf("split %d accepted %q", split, test.arguments)
				}
				if !utf8.ValidString(decoder.Input()) {
					t.Fatalf("split %d emitted invalid UTF-8", split)
				}
			}
			var byteDecoder ApplyPatchInputDecoder
			for i := 0; i < len(test.arguments); i++ {
				if _, errPush := byteDecoder.Push(test.arguments[i : i+1]); errPush != nil {
					break
				}
			}
			if _, errFinish := byteDecoder.Finish(test.arguments); errFinish == nil {
				t.Fatalf("single-byte fragments accepted %q", test.arguments)
			}
			var decoder ApplyPatchInputDecoder
			if _, errFinish := decoder.Finish(test.arguments); errFinish == nil {
				t.Fatalf("finish without deltas accepted %q", test.arguments)
			}
		})
	}
}

func TestApplyPatchInputDecoderInvalidValueFailsImmediately(t *testing.T) {
	for _, fragment := range []string{`{"input":4`, `{"input":n`, `{"input":t`, `{"input":[`, `{"input":{`, `{"input":"\q`, `{"input":"\u12G`, `{"input":"\uDE00`, `{"input":"\uD83Dx`} {
		var decoder ApplyPatchInputDecoder
		if _, errPush := decoder.Push(fragment); errPush == nil {
			t.Fatalf("did not immediately reject %q", fragment)
		}
	}
}

func TestApplyPatchInputDecoderInvalidPendingCharactersDoNotEmitReplacement(t *testing.T) {
	for _, test := range []struct {
		name         string
		pending      string
		continuation string
	}{
		{name: "surrogate closed", pending: `\uD83D`, continuation: `"}`},
		{name: "surrogate text", pending: `\uD83D`, continuation: "x"},
		{name: "surrogate escape", pending: `\uD83D\`, continuation: "n"},
		{name: "invalid low surrogate", pending: `\uD83D\u00`, continuation: "41"},
		{name: "invalid escape", pending: `\`, continuation: "q"},
		{name: "invalid hex", pending: `\u12`, continuation: "G"},
		{name: "invalid utf8", pending: "\xe4", continuation: "x"},
		{name: "truncated utf8", pending: "\xe4\xb8", continuation: `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decoder ApplyPatchInputDecoder
			if delta, errPush := decoder.Push(`{"input":"safe` + test.pending); errPush != nil || delta != "safe" {
				t.Fatalf("pending push = %q, %v", delta, errPush)
			}
			if delta, errPush := decoder.Push(test.continuation); errPush == nil || delta != "" {
				t.Fatalf("invalid continuation = %q, %v", delta, errPush)
			}
			if decoder.Input() != "safe" {
				t.Fatalf("invalid character changed confirmed input to %q", decoder.Input())
			}
		})
	}
}

func TestApplyPatchInputDecoderFinishAndRepeatedTermination(t *testing.T) {
	arguments := applypatch.WrapInput("  first\nsecond  \n")
	for _, test := range []struct {
		name string
		push string
		want string
	}{
		{name: "no deltas", want: "  first\nsecond  \n"},
		{name: "partial deltas", push: `{"input":"  first\n`, want: "second  \n"},
		{name: "complete deltas", push: arguments},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decoder ApplyPatchInputDecoder
			if _, errPush := decoder.Push(test.push); errPush != nil {
				t.Fatal(errPush)
			}
			tail, errFinish := decoder.Finish(arguments)
			if errFinish != nil || tail != test.want || decoder.Input() != "  first\nsecond  \n" {
				t.Fatalf("finish: tail %q, input %q, error %v", tail, decoder.Input(), errFinish)
			}
			for i := 0; i < 3; i++ {
				tail, errFinish = decoder.Finish(arguments)
				if errFinish != nil || tail != "" {
					t.Fatalf("repeat %d: %q, %v", i, tail, errFinish)
				}
			}
			if _, errFinish = decoder.Finish(applypatch.WrapInput("  first\nsecond  \nmore")); errFinish == nil {
				t.Fatal("accepted conflicting repeat with the same prefix")
			}
		})
	}
}

func TestApplyPatchInputDecoderRejectsFinalPrefixConflict(t *testing.T) {
	for _, final := range []string{"other", "pre", "prefix changed"} {
		var decoder ApplyPatchInputDecoder
		if delta, errPush := decoder.Push(`{"input":"prefix original`); errPush != nil || delta != "prefix original" {
			t.Fatalf("push = %q, %v", delta, errPush)
		}
		if tail, errFinish := decoder.Finish(applypatch.WrapInput(final)); errFinish == nil || tail != "" {
			t.Fatalf("accepted final conflict %q: %q, %v", final, tail, errFinish)
		}
		if decoder.Input() != "prefix original" {
			t.Fatal("conflict changed the emitted prefix")
		}
	}
}

func TestApplyPatchInputDecoderErrorIsTerminal(t *testing.T) {
	var decoder ApplyPatchInputDecoder
	_, errInitialPush := decoder.Push(`{"input":"safe\uD83D\u0041`)
	if errInitialPush == nil || decoder.Input() != "safe" {
		t.Fatalf("invalid surrogate: input %q, error %v", decoder.Input(), errInitialPush)
	}
	if delta, errPush := decoder.Push(`suffix"}`); errPush != errInitialPush || delta != "" {
		t.Fatalf("push after error = %q, %v", delta, errPush)
	}
	if tail, errFinish := decoder.Finish(applypatch.WrapInput("safe")); errFinish != errInitialPush || tail != "" {
		t.Fatalf("finish after error = %q, %v", tail, errFinish)
	}
}

func TestApplyPatchInputDecoderFinishErrorIsTerminal(t *testing.T) {
	for _, arguments := range []string{`{"input":null}`, `{"input":"wrong prefix"}`, `{"input":"safe\uD83D"}`} {
		var decoder ApplyPatchInputDecoder
		if delta, errPush := decoder.Push(`{"input":"safe`); errPush != nil || delta != "safe" {
			t.Fatalf("push = %q, %v", delta, errPush)
		}
		_, errInitialFinish := decoder.Finish(arguments)
		if errInitialFinish == nil {
			t.Fatalf("accepted invalid final %q", arguments)
		}
		if tail, errFinish := decoder.Finish(`{"input":"safe"}`); errFinish != errInitialFinish || tail != "" || decoder.Input() != "safe" {
			t.Fatalf("finish after error = %q, %v, input %q", tail, errFinish, decoder.Input())
		}
	}
}

func TestApplyPatchInputDecoderEquivalentFinalEncoding(t *testing.T) {
	var decoder ApplyPatchInputDecoder
	if delta, errPush := decoder.Push(`{"input":"中\n`); errPush != nil || delta != "中\n" {
		t.Fatalf("push = %q, %v", delta, errPush)
	}
	if tail, errFinish := decoder.Finish(`{"in\u0070ut":"\u4e2d\n😀"}`); errFinish != nil || tail != "😀" {
		t.Fatalf("finish = %q, %v", tail, errFinish)
	}
	if tail, errFinish := decoder.Finish(" \n{\"input\":\"中\\n\\uD83D\\uDE00\"}\t"); errFinish != nil || tail != "" {
		t.Fatalf("equivalent repeat = %q, %v", tail, errFinish)
	}
}

func TestApplyPatchInputDecoderPushAfterFinish(t *testing.T) {
	var decoder ApplyPatchInputDecoder
	if _, errFinish := decoder.Finish(`{"input":"patch"}`); errFinish != nil {
		t.Fatal(errFinish)
	}
	if delta, errPush := decoder.Push("more"); errPush == nil || delta != "" || decoder.Input() != "patch" {
		t.Fatalf("push after finish = %q, %v, input %q", delta, errPush, decoder.Input())
	}
}
