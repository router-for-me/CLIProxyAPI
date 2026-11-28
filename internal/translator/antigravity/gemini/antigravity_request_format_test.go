package gemini

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func buildToolHeavyAntigravityRequest(turns int, useResultKey bool) []byte {
	payload := strings.Repeat("x", 256)
	var b strings.Builder
	b.WriteString(`{"request":{"contents":[`)
	for i := 0; i < turns; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"model","parts":[{"text":"%s"},{"functionCall":{"id":"call_%d","name":"lookup","args":{"query":"%s"}}}]},`, payload, i, payload)
		if useResultKey {
			fmt.Fprintf(&b, `{"role":"user","parts":[{"functionResponse":{"id":"call_%d","name":"lookup","response":{"result":"turn_%d_%s"}}}]}`, i, i, payload)
		} else {
			fmt.Fprintf(&b, `{"role":"user","parts":[{"functionResponse":{"id":"call_%d","name":"lookup","response":{"output":"turn_%d_%s"}}}]}`, i, i, payload)
		}
	}
	b.WriteString(`]}}`)
	return []byte(b.String())
}

func TestNormalizeAntigravityFunctionResponseResultsSemantics(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		input    string
		validate func(t *testing.T, out []byte)
	}{
		{
			name:   "camelCase single string result rewritten to output",
			prefix: "request.",
			input:  `{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"read","response":{"result":"hello\n\"world\""}}}]}]}}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				resp := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.functionResponse.response")
				if resp.Get("result").Exists() {
					t.Fatalf("expected result key to be removed, got %s", resp.Raw)
				}
				if got := resp.Get("output").String(); got != "hello\n\"world\"" {
					t.Fatalf("output = %q, want %q; raw=%s", got, "hello\n\"world\"", resp.Raw)
				}
			},
		},
		{
			name:   "snake_case single string result rewritten to output",
			prefix: "",
			input:  `{"contents":[{"role":"user","parts":[{"function_response":{"name":"read","response":{"result":"ok"}}}]}]}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				resp := ojson.GetBytesNoCopy(out, "contents.0.parts.0.function_response.response")
				if resp.Get("result").Exists() {
					t.Fatalf("expected result key to be removed, got %s", resp.Raw)
				}
				if got := resp.Get("output").String(); got != "ok" {
					t.Fatalf("output = %q, want ok; raw=%s", got, resp.Raw)
				}
			},
		},
		{
			name:   "both camelCase and snake_case in same part are normalized",
			prefix: "request.",
			input:  `{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"response":{"result":"a"}},"function_response":{"response":{"result":"b"}}}]}]}}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				camel := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.functionResponse.response")
				snake := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.function_response.response")
				if camel.Get("result").Exists() || camel.Get("output").String() != "a" {
					t.Fatalf("camelCase response = %s", camel.Raw)
				}
				if snake.Get("result").Exists() || snake.Get("output").String() != "b" {
					t.Fatalf("snake_case response = %s", snake.Raw)
				}
			},
		},
		{
			name:   "structured response with multiple keys preserved",
			prefix: "request.",
			input:  `{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"response":{"result":"a","output":"b"}}},{"function_response":{"response":{"result":"a","extra":1}}}]}]}}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				r0 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.functionResponse.response")
				if r0.Get("result").String() != "a" || r0.Get("output").String() != "b" {
					t.Fatalf("r0 changed: %s", r0.Raw)
				}
				r1 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.1.function_response.response")
				if r1.Get("result").String() != "a" || r1.Get("extra").Int() != 1 || r1.Get("output").Exists() {
					t.Fatalf("r1 changed: %s", r1.Raw)
				}
			},
		},
		{
			name:   "non-string result and empty or non-object response preserved",
			prefix: "request.",
			input:  `{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"response":{"result":123}}},{"functionResponse":{"response":{"result":true}}},{"functionResponse":{"response":{"result":null}}},{"functionResponse":{"response":{"result":{"nested":"v"}}}},{"functionResponse":{"response":{}}},{"functionResponse":{"response":"plain"}}]}]}}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				expected := `{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"response":{"result":123}}},{"functionResponse":{"response":{"result":true}}},{"functionResponse":{"response":{"result":null}}},{"functionResponse":{"response":{"result":{"nested":"v"}}}},{"functionResponse":{"response":{}}},{"functionResponse":{"response":"plain"}}]}]}}`
				if string(out) != expected {
					t.Fatalf("payload modified unexpectedly:\n got: %s\nwant: %s", out, expected)
				}
			},
		},
		{
			name:   "non-array contents left untouched",
			prefix: "request.",
			input:  `{"request":{"contents":{"role":"user","parts":[{"functionResponse":{"response":{"result":"ok"}}}]}}}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				expected := `{"request":{"contents":{"role":"user","parts":[{"functionResponse":{"response":{"result":"ok"}}}]}}}`
				if string(out) != expected {
					t.Fatalf("non-array contents modified:\n got: %s\nwant: %s", out, expected)
				}
			},
		},
		{
			name:   "non-array parts preserves existing handling",
			prefix: "request.",
			input:  `{"request":{"contents":[{"role":"user","parts":{"functionResponse":{"response":{"result":"ok"}},"0":{"functionResponse":{"response":{"metadata":1}}}}},{"role":"user","parts":null},{"role":"user","parts":"text"}]}}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				resp := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.functionResponse.response")
				if got := resp.Get("output").String(); got != "ok" {
					t.Fatalf("expected non-array object parts fallback write, got: %s", out)
				}
				if got := resp.Get("metadata").Int(); got != 1 {
					t.Fatalf("expected existing metadata field in non-array parts target preserved, got: %s", resp.Raw)
				}
				if got := ojson.GetBytesNoCopy(out, "request.contents.1.parts"); got.Type != ojson.Null {
					t.Fatalf("expected null parts preserved, got: %s", got.Raw)
				}
				if got := ojson.GetBytesNoCopy(out, "request.contents.2.parts"); got.String() != "text" {
					t.Fatalf("expected string parts preserved, got: %s", got.Raw)
				}
			},
		},
		{
			name:   "duplicate result keys preserve existing semantics",
			prefix: "request.",
			input:  `{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"response":{"result":"first","result":"second"}}}]}]}}`,
			validate: func(t *testing.T, out []byte) {
				t.Helper()
				resp := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.functionResponse.response")
				if got := resp.Get("output").String(); got != "first" {
					t.Fatalf("expected output=first for duplicate result keys, got: %s", resp.Raw)
				}
				if got := resp.Get("result").String(); got != "second" {
					t.Fatalf("expected remaining second result key, got: %s", resp.Raw)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(tc.input)
			normalizeAntigravityFunctionResponseResultsDoc(doc, tc.prefix)
			tc.validate(t, doc.Bytes())
		})
	}
}

func TestNormalizeAntigravityFunctionResponseResultsNoOpAllocs(t *testing.T) {
	t.Run("plain output", func(t *testing.T) {
		input := buildToolHeavyAntigravityRequest(64, false)

		allocs := testing.AllocsPerRun(5, func() {
			doc := ojson.NewDoc(input)
			normalizeAntigravityFunctionResponseResultsDoc(doc, "request.")
		})
		if allocs > 5 {
			t.Fatalf("no-op normalizeAntigravityFunctionResponseResultsDoc allocated %.0f objects/op for 64 turns, want <= 5", allocs)
		}
	})

	t.Run("escaped output strings", func(t *testing.T) {
		escapedChunk := strings.Repeat(`line1\n\"quoted\"\t\\slash `, 16)
		var b strings.Builder
		b.WriteString(`{"request":{"contents":[`)
		for i := 0; i < 64; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"role":"user","parts":[{"functionResponse":{"id":"call_%d","name":"lookup","response":{"output":"%s"}}}]}`, i, escapedChunk)
		}
		b.WriteString(`]}}`)
		input := []byte(b.String())

		allocs := testing.AllocsPerRun(5, func() {
			doc := ojson.NewDoc(input)
			normalizeAntigravityFunctionResponseResultsDoc(doc, "request.")
		})
		if allocs > 5 {
			t.Fatalf("no-op escaped output normalizeAntigravityFunctionResponseResultsDoc allocated %.0f objects/op for 64 turns, want <= 5", allocs)
		}
	})
}

func TestNormalizeAntigravityFunctionResponseResultsRewriteAllocs(t *testing.T) {
	input := buildToolHeavyAntigravityRequest(64, true)

	// Verify rewrite correctness across every turn (ensures buffer reuse never aliases earlier outputs).
	checkDoc := ojson.NewDoc(input)
	normalizeAntigravityFunctionResponseResultsDoc(checkDoc, "request.")
	out := checkDoc.Bytes()
	payload := strings.Repeat("x", 256)
	for i := 0; i < 64; i++ {
		respPath := fmt.Sprintf("request.contents.%d.parts.0.functionResponse.response", i*2+1)
		resp := ojson.GetBytesNoCopy(out, respPath)
		want := fmt.Sprintf("turn_%d_%s", i, payload)
		if got := resp.Get("output").String(); got != want {
			t.Fatalf("turn %d output = %q, want %q", i, got, want)
		}
		if resp.Get("result").Exists() {
			t.Fatalf("turn %d response still contains result key: %s", i, resp.Raw)
		}
	}

	allocs := testing.AllocsPerRun(5, func() {
		doc := ojson.NewDoc(input)
		normalizeAntigravityFunctionResponseResultsDoc(doc, "request.")
	})
	// Before optimization this allocates ~2400+ objects/op due to contents.Array(),
	// parts.Array(), eager fmt.Sprintf, field+".response", response.Map(), and
	// separate SetRaw+Delete mutations.
	if allocs > 800 {
		t.Fatalf("rewrite normalizeAntigravityFunctionResponseResultsDoc allocated %.0f objects/op for 64 turns, want <= 800", allocs)
	}
}
