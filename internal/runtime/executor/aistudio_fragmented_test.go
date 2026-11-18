package executor

import (
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAIStudioStreamReassemblesFragmentedFrames(t *testing.T) {
	terminal := strings.Replace(multiFrameB, `"text":"BBB"`, `"text":"BBB","thoughtSignature":"`+strings.Repeat("signature", 512)+`"`, 1)
	frame := "data: " + terminal + "\n\n"
	crlf := "data: " + terminal + "\r\n\r\n"
	for _, format := range []sdktranslator.Format{
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatClaude,
	} {
		t.Run(format.String(), func(t *testing.T) {
			for name, fragments := range map[string][]string{
				"large signature":   {frame[:1181], frame[1181:2362], frame[2362:3543], frame[3543:]},
				"split data prefix": {frame[:2], frame[2:4], frame[4:]},
				"split crlf":        {crlf[:len(crlf)-3], crlf[len(crlf)-3 : len(crlf)-1], crlf[len(crlf)-1:]},
				"unterminated tail": {frame[:1181], strings.TrimSuffix(frame[1181:], "\n\n")},
			} {
				t.Run(name, func(t *testing.T) {
					chunks := append([]string{"data: " + multiFrameA + "\n\n"}, fragments...)
					got, errGot := streamThroughRelay(t, format, chunks)
					if errGot != nil {
						t.Fatal(errGot)
					}
					if !strings.Contains(got, "AAA") || !strings.Contains(got, "BBB") {
						t.Errorf("fragmented stream lost content: %s", got)
					}
					terminalMarker := `"finish_reason": "stop"`
					if format == sdktranslator.FormatOpenAIResponse {
						terminalMarker = "event: response.completed"
					} else if format == sdktranslator.FormatClaude {
						terminalMarker = "event: message_stop"
					}
					if !strings.Contains(got, terminalMarker) {
						t.Errorf("fragmented stream lacks %q: %s", terminalMarker, got)
					}
				})
			}
		})
	}
}
