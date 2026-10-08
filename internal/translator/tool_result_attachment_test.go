package translator

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// toolResultTargets names, per target format, the JSON path where a base64 file
// from a Claude tool_result must end up. Codex has no file part inside a function_call_output,
// so the file rides in the user message that follows it. Bytes anywhere else, such as inside an
// escaped string, are text the model has to decode itself.
var toolResultTargets = []struct {
	name   string
	format sdktranslator.Format
	path   string
}{
	{"gemini", sdktranslator.FormatGemini, "contents.#.parts.#.inline_data.data"},
	{"antigravity", sdktranslator.FormatAntigravity, "request.contents.#.parts.#.functionResponse.parts.#.inlineData.data"},
	{"codex", sdktranslator.FormatCodex, "input.#.content.#.file_data"},
	{"openai", sdktranslator.FormatOpenAI, "messages.#.content.#.file.file_data"},
	{"interactions", sdktranslator.FormatInteractions, "input.#.result.#.data"},
}

func claudeToolResultRequest(blocks ...string) []byte {
	return []byte(`{"model":"m","max_tokens":64,"messages":[` +
		`{"role":"user","content":"read it"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[` + strings.Join(blocks, ",") + `]}]}]}`)
}

func translateClaudeRequest(t *testing.T, to sdktranslator.Format, body []byte) string {
	t.Helper()
	envelope := sdktranslator.TranslateRequestEnvelope(context.Background(), sdktranslator.FormatClaude, to,
		sdktranslator.RequestEnvelope{Format: sdktranslator.FormatClaude, Model: "m", Body: body})
	if envelope.Err != nil {
		t.Fatalf("translation refused the request: %v", envelope.Err)
	}
	return string(envelope.Body)
}

// A PDF returned by a tool must reach every target as bytes the model can read,
// exactly once and only where that target carries files.
func TestToolResultPDFReachesEveryTargetAsBytes(t *testing.T) {
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 tool result"))
	document := `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + pdf + `"}}`
	text := `{"type":"text","text":"PDF file read: report.pdf"}`

	for _, shape := range []struct {
		name   string
		blocks []string
	}{
		{"beside text", []string{text, document}},
		{"alone", []string{document}},
	} {
		for _, target := range toolResultTargets {
			t.Run(target.name+"/"+shape.name, func(t *testing.T) {
				out := translateClaudeRequest(t, target.format, claudeToolResultRequest(shape.blocks...))

				if got := strings.Count(out, pdf); got != 1 {
					t.Fatalf("pdf bytes appear %d times, want 1\n%s", got, out)
				}
				if structured := gjson.Get(out, target.path).Raw; !strings.Contains(structured, pdf) {
					t.Fatalf("pdf bytes are not at %s\n%s", target.path, out)
				}
			})
		}
	}
}

// An attachment a target cannot carry as bytes must not vanish: some trace of it
// has to reach the model, even if only as the original block text.
func TestToolResultAttachmentIsNeverDroppedSilently(t *testing.T) {
	text := `{"type":"text","text":"read result"}`

	for _, shape := range []struct {
		name   string
		marker string
		block  string
	}{
		{"document url", "MKdocurl", `{"type":"document","source":{"type":"url","url":"https://example.com/MKdocurl.pdf"}}`},
		{"document file id", "file_MKdocid", `{"type":"document","source":{"type":"file","file_id":"file_MKdocid"}}`},
		{"image url", "MKimgurl", `{"type":"image","source":{"type":"url","url":"https://example.com/MKimgurl.png"}}`},
		{"image file id", "file_MKimgid", `{"type":"image","source":{"type":"file","file_id":"file_MKimgid"}}`},
	} {
		for _, placement := range []struct {
			name   string
			blocks []string
		}{
			{"beside text", []string{text, shape.block}},
			{"alone", []string{shape.block}},
		} {
			for _, target := range toolResultTargets {
				t.Run(shape.name+"/"+target.name+"/"+placement.name, func(t *testing.T) {
					out := translateClaudeRequest(t, target.format, claudeToolResultRequest(placement.blocks...))
					if !strings.Contains(out, shape.marker) {
						t.Fatalf("%s left no trace in the %s request\n%s", shape.name, target.name, out)
					}
				})
			}
		}
	}
}
