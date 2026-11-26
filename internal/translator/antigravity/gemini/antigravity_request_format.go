package gemini

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// NormalizeAntigravityRequestDoc applies the native Antigravity wire format.
func NormalizeAntigravityRequestDoc(doc *ojson.Doc) {
	normalizeAntigravityRequestDocAt(doc, "request.")
}

func normalizeAntigravityRequestDocAt(doc *ojson.Doc, prefix string) {
	if doc == nil {
		return
	}
	normalizeAntigravitySystemInstructionDoc(doc, prefix)
	normalizeAntigravityFunctionResponseResultsDoc(doc, prefix)
}

func normalizeAntigravitySystemInstructionDoc(doc *ojson.Doc, prefix string) {
	instructionPath := prefix + "systemInstruction"
	instruction := doc.Get(instructionPath)
	if !instruction.Exists() {
		return
	}
	if !instruction.IsObject() {
		if instruction.Type == ojson.Null {
			_ = doc.Delete(instructionPath)
		}
		return
	}
	_ = doc.Set(instructionPath+".role", "user")
	parts := instruction.Get("parts").Array()
	if len(parts) == 0 {
		return
	}

	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		text := part.Get("text")
		if !text.Exists() || text.Type != ojson.String {
			return
		}
		if text.String() != "" {
			texts = append(texts, text.String())
		}
	}
	if len(texts) == 0 {
		_ = doc.Delete(instructionPath)
		return
	}

	part := ojson.NewDocString(`{"text":""}`)
	_ = part.Set("text", strings.Join(texts, "\n\n"))
	_ = doc.SetRawBytes(instructionPath+".parts", []byte("["+part.String()+"]"))
}

func normalizeAntigravityFunctionResponseResultsDoc(doc *ojson.Doc, prefix string) {
	contentsPath := prefix + "contents"
	contents := doc.Get(contentsPath)
	if !contents.IsArray() {
		return
	}
	for contentIndex, content := range contents.Array() {
		parts := content.Get("parts")
		for partIndex, part := range parts.Array() {
			for _, field := range []string{"functionResponse", "function_response"} {
				responsePath := fmt.Sprintf("%s%d.parts.%d.%s.response", contentsPath+".", contentIndex, partIndex, field)
				response := part.Get(field + ".response")
				result := response.Get("result")
				if result.Type == ojson.String && len(response.Map()) == 1 {
					_ = doc.SetRaw(responsePath+".output", result.Raw)
					_ = doc.Delete(responsePath + ".result")
				}
			}
		}
	}
}
