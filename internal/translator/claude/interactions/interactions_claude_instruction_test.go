package interactions

import (
	"strings"
	"testing"
)

// claudeInstructionSteps are developer and system steps in the shapes an
// Interactions client can send. Each one carries the text "note".
var claudeInstructionSteps = map[string]string{
	"developer user_input":     `{"type":"user_input","role":"developer","content":[{"type":"text","text":"note"}]}`,
	"system user_input":        `{"type":"user_input","role":"system","content":[{"type":"text","text":"note"}]}`,
	"developer string content": `{"type":"user_input","role":"developer","content":"note"}`,
	"bare system object":       `{"role":"system","content":"note"}`,
	"bare system text":         `{"type":"system","text":"note"}`,
	"developer native parts":   `{"role":"developer","parts":[{"text":"note"}]}`,
	"developer wrapper":        `{"role":"developer","steps":[{"type":"user_input","content":[{"type":"text","text":"note"}]}]}`,
}

// claudeEmptiedStep is a user step whose only part Claude cannot carry.
const claudeEmptiedStep = `{"type":"user_input","content":[{"type":"video","mime_type":"video/mp4","uri":"https://example.test/a.mp4"}]}`

func TestInteractionsToClaudeDeveloperOrSystemStepDoesNotHideAnEmptiedUserTurn(t *testing.T) {
	for name, note := range claudeInstructionSteps {
		t.Run("after "+name, func(t *testing.T) {
			requireMediaRefusal(t, interactionsToClaudeEnvelope(`{"model":"m","input":[`+interactionsHistory+`,`+claudeEmptiedStep+`,`+note+`]}`), "video")
		})
		t.Run("before "+name, func(t *testing.T) {
			requireMediaRefusal(t, interactionsToClaudeEnvelope(`{"model":"m","input":[`+interactionsHistory+`,`+note+`,`+claudeEmptiedStep+`]}`), "video")
		})
	}
}

func TestInteractionsToClaudeDeveloperOrSystemStepStillSendsItsText(t *testing.T) {
	for name, note := range claudeInstructionSteps {
		t.Run(name, func(t *testing.T) {
			envelope := interactionsToClaudeEnvelope(`{"model":"m","input":[` + interactionsHistory + `,{"type":"user_input","content":[{"type":"text","text":"keep me"}]},` + note + `]}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			if !strings.Contains(string(envelope.Body), "note") || !strings.Contains(string(envelope.Body), "keep me") {
				t.Fatalf("text was lost: %s", envelope.Body)
			}
		})
	}
}

func TestInteractionsToClaudeRealUserTextBesideAnUnrepresentableAttachmentSurvivesAnInstructionStep(t *testing.T) {
	for name, note := range claudeInstructionSteps {
		t.Run(name, func(t *testing.T) {
			envelope := interactionsToClaudeEnvelope(`{"model":"m","input":[` + interactionsHistory + `,` + note + `,{"type":"user_input","content":[{"type":"text","text":"keep me"},` + videoByURI + `]}]}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			if !strings.Contains(string(envelope.Body), "keep me") {
				t.Fatalf("text was lost: %s", envelope.Body)
			}
		})
	}
}
