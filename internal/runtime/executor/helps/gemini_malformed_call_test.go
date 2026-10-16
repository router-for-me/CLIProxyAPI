package helps

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/tidwall/sjson"
)

const geminiPrefixFailure = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"\"}]},\"finishReason\":\"MALFORMED_FUNCTION_CALL\",\"finishMessage\":\"Malformed function call: thought\\n call:default_api:run_command{invalid args}\"}]}\n\n"

func newGeminiPrefixFixture(wire io.Reader) ([]byte, io.Reader) {
	request := []byte(`{"contents":[{"role":"user","parts":[{"text":"original"}]}],"tools":[{"functionDeclarations":[{"name":"run_command"},{"name":"get_weather"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`)
	return request, wire
}

func TestReadGeminiStreamPrefixReplaysBoundedInput(t *testing.T) {
	wire := ": keepalive\n\ndata: {\"responseId\":\"first\",\"cpaUsageMetadata\":{\"promptTokenCount\":7},\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"\"}]}}]}\n\n" + geminiPrefixFailure + "data: {\"usageMetadata\":{\"promptTokenCount\":7}}\n\n"
	for _, tc := range []struct {
		name  string
		limit int64
		tool  string
	}{
		{"small prefix", 16, ""},
		{"exact limit", int64(len(wire)), ""},
		{"complete failure", int64(len(wire) + 1), "run_command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, reader := newGeminiPrefixFixture(strings.NewReader(wire))
			prefix, err := ReadGeminiStreamPrefix(reader, request, request, tc.limit)
			if err != nil || prefix.Tool != tc.tool {
				t.Fatalf("tool = %q, error = %v; want %q", prefix.Tool, err, tc.tool)
			}
			if int64(len(prefix.Bytes)) > tc.limit {
				t.Fatalf("buffered %d bytes with limit %d", len(prefix.Bytes), tc.limit)
			}
			replayed, err := io.ReadAll(prefix.Reader)
			if err != nil || string(replayed) != wire {
				t.Fatalf("replay lost or duplicated bytes: %q, error %v", replayed, err)
			}
		})
	}
}

func TestReadGeminiStreamPrefixSkipsNonterminalReasons(t *testing.T) {
	for _, reason := range []string{"FINISH_REASON_UNSPECIFIED", "UNKNOWN"} {
		t.Run(reason, func(t *testing.T) {
			payload := fmt.Sprintf(`{"candidates":[{"content":{"parts":[]},"finishReason":%q}]}`, reason)
			if IsGeminiTokenEvent([]byte(payload)) {
				t.Fatal("nonterminal placeholder was counted as output")
			}
			wire := "data: " + payload + "\n\n" + geminiPrefixFailure
			request, reader := newGeminiPrefixFixture(strings.NewReader(wire))
			prefix, err := ReadGeminiStreamPrefix(reader, request, request, 4096)
			if err != nil || prefix.Tool != "run_command" {
				t.Fatalf("nonterminal placeholder prevented recovery: tool=%q error=%v", prefix.Tool, err)
			}
		})
	}
}

func TestReadGeminiStreamPrefixKeepsTargetAtTerminator(t *testing.T) {
	wire := geminiPrefixFailure + "data: [DONE]\n\n"
	request, reader := newGeminiPrefixFixture(strings.NewReader(wire))
	prefix, err := ReadGeminiStreamPrefix(reader, request, request, 4096)
	if err != nil || prefix.Tool != "run_command" {
		t.Fatalf("SSE terminator lost recovery target: tool=%q error=%v", prefix.Tool, err)
	}
	replayed, err := io.ReadAll(prefix.Reader)
	if err != nil || string(replayed) != wire {
		t.Fatalf("SSE terminator was not preserved: %q, error %v", replayed, err)
	}
}

func TestReadGeminiStreamPrefixStopsAtContent(t *testing.T) {
	event := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n"
	sentinel := errors.New("later read")
	request, reader := newGeminiPrefixFixture(io.MultiReader(strings.NewReader(event), iotest.ErrReader(sentinel)))
	prefix, err := ReadGeminiStreamPrefix(reader, request, request, 4096)
	if err != nil || prefix.Tool != "" {
		t.Fatalf("probe read beyond content: tool=%q error=%v", prefix.Tool, err)
	}
	replayed, err := io.ReadAll(prefix.Reader)
	if !errors.Is(err, sentinel) || string(replayed) != event {
		t.Fatalf("later stream was not preserved: %q, error %v", replayed, err)
	}
}

func TestReadGeminiStreamPrefixRejectsAmbiguousFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire string
	}{
		{"same frame content", strings.Replace(geminiPrefixFailure, `"text":""`, `"text":"visible"`, 1)},
		{"multiple candidates", strings.Replace(geminiPrefixFailure, `"candidates":[`, `"candidates":[{},`, 1)},
		{"mixed unknown target", strings.Replace(geminiPrefixFailure, "invalid args}", "invalid args} call:default_api:unknown{invalid}", 1)},
		{"unsupported namespace", strings.Replace(geminiPrefixFailure, "default_api", "other_api", 1)},
		{"unknown content", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"newPart\":{}}]}}]}\n\n" + geminiPrefixFailure},
		{"unknown trailer", geminiPrefixFailure + "data: {\"unknown\":true}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, reader := newGeminiPrefixFixture(strings.NewReader(tc.wire))
			prefix, err := ReadGeminiStreamPrefix(reader, request, request, 4096)
			if err != nil || prefix.Tool != "" {
				t.Fatalf("ambiguous stream was eligible: tool=%q error=%v", prefix.Tool, err)
			}
			replayed, err := io.ReadAll(prefix.Reader)
			if err != nil || string(replayed) != tc.wire {
				t.Fatalf("ambiguous stream changed: %q, error %v", replayed, err)
			}
		})
	}
}

func TestReadGeminiStreamPrefixPreservesReadError(t *testing.T) {
	sentinel := errors.New("upstream interrupted")
	request, reader := newGeminiPrefixFixture(io.MultiReader(strings.NewReader(geminiPrefixFailure), iotest.ErrReader(sentinel)))
	prefix, err := ReadGeminiStreamPrefix(reader, request, request, 4096)
	if !errors.Is(err, sentinel) || prefix.Tool != "" || string(prefix.Bytes) != geminiPrefixFailure {
		t.Fatalf("read error lost or failure recovered: tool=%q error=%v", prefix.Tool, err)
	}
}

func TestGeminiRecoveryPreservesBusinessPayload(t *testing.T) {
	request, _ := newGeminiPrefixFixture(strings.NewReader(""))
	retry, err := ConstrainGeminiRecoveryTool(request, "run_command")
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := GeminiRecoveryBodyPreserved(request, retry)
	if err != nil || !preserved || !GeminiRecoveryToolConfigured(retry, "run_command") {
		t.Fatalf("tool-only change rejected: preserved=%v error=%v", preserved, err)
	}
	if GeminiToolRecoveryAllowed(retry, "run_command") {
		t.Fatal("already constrained tool remained eligible")
	}
	changed, err := sjson.SetBytes(retry, "contents.0.parts.0.text", "changed")
	if err != nil {
		t.Fatal(err)
	}
	preserved, err = GeminiRecoveryBodyPreserved(request, changed)
	if err != nil || preserved {
		t.Fatalf("business payload change accepted: preserved=%v error=%v", preserved, err)
	}
}

func TestReadGeminiStreamPrefixKeepsColonsInToolName(t *testing.T) {
	for _, name := range []string{"mcp:server:run", "recall:run"} {
		t.Run(name, func(t *testing.T) {
			request := []byte(fmt.Sprintf(`{"tools":[{"functionDeclarations":[{"name":%q}]}]}`, name))
			wire := strings.Replace(geminiPrefixFailure, "run_command", name, 1)
			_, reader := newGeminiPrefixFixture(strings.NewReader(wire))
			prefix, err := ReadGeminiStreamPrefix(reader, request, request, 4096)
			if err != nil || prefix.Tool != name {
				t.Fatalf("colon in tool name rejected: tool=%q error=%v", prefix.Tool, err)
			}
		})
	}
}
