package helps

import (
	"bytes"
	"strings"

	"github.com/MoeForge/moego/ojson"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// GenerationResponseComplete requires a provider terminal or a complete native
// non-stream response. A tool argument, output item, or transport EOF alone is
// not proof that generation ended.
func GenerationResponseComplete(format sdktranslator.Format, payload []byte) bool {
	if ojson.ValidBytes(payload) {
		return generationDocumentComplete(format, ojson.ParseBytes(payload))
	}
	for _, line := range bytes.Split(payload, []byte("\n")) {
		data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
		if !ok || !ojson.ValidBytes(bytes.TrimSpace(data)) {
			continue
		}
		doc := ojson.ParseBytes(bytes.TrimSpace(data))
		switch doc.Get("type").String() {
		case "message_stop":
			if format == sdktranslator.FormatClaude {
				return true
			}
		case "response.completed", "response.incomplete", "response.done":
			if (format == sdktranslator.FormatOpenAIResponse || format == sdktranslator.FormatCodex) && doc.Get("response").IsObject() {
				return true
			}
		}
		if format == sdktranslator.FormatGemini && generationDocumentComplete(format, doc) {
			return true
		}
		if format == sdktranslator.FormatInteractions && doc.Get("event_type").String() == "interaction.completed" {
			return true
		}
	}
	return false
}

func generationDocumentComplete(format sdktranslator.Format, doc ojson.Result) bool {
	switch format {
	case sdktranslator.FormatOpenAI:
		choices := doc.Get("choices")
		if !choices.IsArray() || len(choices.Array()) == 0 {
			return false
		}
		for _, choice := range choices.Array() {
			if strings.TrimSpace(choice.Get("finish_reason").String()) == "" {
				return false
			}
		}
		return true
	case sdktranslator.FormatGemini, sdktranslator.FromString("antigravity"):
		if response := doc.Get("response"); response.IsObject() {
			doc = response
		}
		candidates := doc.Get("candidates")
		if !candidates.IsArray() || len(candidates.Array()) == 0 {
			return false
		}
		for _, candidate := range candidates.Array() {
			if strings.TrimSpace(candidate.Get("finishReason").String()) == "" {
				return false
			}
		}
		return true
	case sdktranslator.FormatClaude:
		return doc.Get("type").String() == "message" && doc.Get("id").String() != "" && doc.Get("role").String() == "assistant" && doc.Get("content").IsArray()
	case sdktranslator.FormatInteractions:
		status := doc.Get("status").String()
		return (status == "" || status == "completed" || status == "incomplete") && doc.Get("id").String() != "" && doc.Get("steps").IsArray()
	case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex:
		kind := doc.Get("type").String()
		status := doc.Get("status").String()
		return ((kind == "" || kind == "response") && (status == "" || status == "completed" || status == "incomplete") && doc.Get("output").IsArray()) || ((kind == "response.completed" || kind == "response.incomplete" || kind == "response.done") && doc.Get("response").IsObject())
	}
	return false
}
