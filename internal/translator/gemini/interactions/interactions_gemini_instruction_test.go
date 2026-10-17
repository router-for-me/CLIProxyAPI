package interactions

import (
	"strings"
	"testing"
)

// geminiInstructionSteps are developer and system steps in the shapes an
// Interactions client can send. Each one carries the text "note".
var geminiInstructionSteps = map[string]string{
	"developer user_input":     `{"type":"user_input","role":"developer","content":[{"type":"text","text":"note"}]}`,
	"system user_input":        `{"type":"user_input","role":"system","content":[{"type":"text","text":"note"}]}`,
	"developer string content": `{"type":"user_input","role":"developer","content":"note"}`,
	"bare system object":       `{"role":"system","content":"note"}`,
	"bare system text":         `{"type":"system","text":"note"}`,
	"developer native parts":   `{"role":"developer","parts":[{"text":"note"}]}`,
	"developer wrapper":        `{"role":"developer","steps":[{"type":"user_input","content":[{"type":"text","text":"note"}]}]}`,
}

// geminiEmptiedStep is a user step whose only part Gemini cannot carry.
const geminiEmptiedStep = `{"type":"user_input","content":[{"type":"document","uri":"gs://b/a.pdf"}]}`

func TestInteractionsToGeminiDeveloperOrSystemStepDoesNotHideAnEmptiedUserTurn(t *testing.T) {
	for name, note := range geminiInstructionSteps {
		t.Run("after "+name, func(t *testing.T) {
			requireInteractionsToGeminiRefusal(t, interactionsToGeminiEnvelope(`{"model":"m","input":[`+interactionsGeminiHistory+`,`+geminiEmptiedStep+`,`+note+`]}`), "document")
		})
		t.Run("before "+name, func(t *testing.T) {
			requireInteractionsToGeminiRefusal(t, interactionsToGeminiEnvelope(`{"model":"m","input":[`+interactionsGeminiHistory+`,`+note+`,`+geminiEmptiedStep+`]}`), "document")
		})
	}
}

func TestInteractionsToGeminiDeveloperOrSystemStepStillSendsItsText(t *testing.T) {
	for name, note := range geminiInstructionSteps {
		t.Run(name, func(t *testing.T) {
			envelope := interactionsToGeminiEnvelope(`{"model":"m","input":[` + interactionsGeminiHistory + `,{"type":"user_input","content":[{"type":"text","text":"keep me"}]},` + note + `]}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			if !strings.Contains(string(envelope.Body), "note") || !strings.Contains(string(envelope.Body), "keep me") {
				t.Fatalf("text was lost: %s", envelope.Body)
			}
		})
	}
}

func TestInteractionsToGeminiRealUserTextBesideAnUnrepresentableAttachmentSurvivesAnInstructionStep(t *testing.T) {
	for name, note := range geminiInstructionSteps {
		t.Run(name, func(t *testing.T) {
			envelope := interactionsToGeminiEnvelope(`{"model":"m","input":[` + interactionsGeminiHistory + `,` + note + `,{"type":"user_input","content":[{"type":"text","text":"keep me"},` + `{"type":"document","uri":"gs://b/a.pdf"}` + `]}]}`)
			if envelope.Err != nil {
				t.Fatalf("envelope.Err = %v, body = %s", envelope.Err, envelope.Body)
			}
			if !strings.Contains(string(envelope.Body), "keep me") {
				t.Fatalf("text was lost: %s", envelope.Body)
			}
		})
	}
}
