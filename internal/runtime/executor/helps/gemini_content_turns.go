package helps

import (
	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

var emptyGeminiUserTurnJSON = []byte(`{"role":"user","parts":[{"text":""}]}`)

// EnsureGeminiLeadingUserContent ensures that the contents array at the given path
// starts with a user turn when sending to Gemini/Antigravity upstreams.
func EnsureGeminiLeadingUserContent(payload []byte, path string) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !EnsureGeminiLeadingUserContentDoc(doc, path) {
		return payload
	}
	return doc.Bytes()
}

// EnsureGeminiLeadingUserContentDoc ensures that the contents array at the given path
// starts with a user turn in place on doc.
func EnsureGeminiLeadingUserContentDoc(doc *ojson.Doc, path string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	firstRole := doc.Get(path + ".0.role")
	if firstRole.String() != "model" {
		return false
	}
	contents := doc.Get(path)
	if !contents.IsArray() {
		return false
	}
	contentArray := contents.Array()
	if len(contentArray) == 0 {
		return false
	}

	contentItems := make([][]byte, 0, len(contentArray)+1)
	contentItems = append(contentItems, emptyGeminiUserTurnJSON)
	for _, content := range contentArray {
		contentItems = append(contentItems, []byte(content.Raw))
	}

	return doc.SetRawBytes(path, translatorcommon.JoinRawArray(contentItems)) == nil
}

func contentHasFunctionResponse(content ojson.Result) bool {
	parts := content.Get("parts")
	if !parts.IsArray() {
		return false
	}
	for _, part := range parts.Array() {
		if part.Get("functionResponse").Exists() {
			return true
		}
	}
	return false
}

// contentHasFunctionResponseMedia reports whether any functionResponse in the
// content carries nested media parts (inlineData/fileData).
func contentHasFunctionResponseMedia(content ojson.Result) bool {
	parts := content.Get("parts")
	if !parts.IsArray() {
		return false
	}
	for _, part := range parts.Array() {
		for _, nested := range part.Get("functionResponse.parts").Array() {
			if nested.Get("inlineData").Exists() || nested.Get("inline_data").Exists() ||
				nested.Get("fileData").Exists() || nested.Get("file_data").Exists() {
				return true
			}
		}
	}
	return false
}

// EnsureGeminiTrailingUserContent ensures that the contents array at the given path
// ends with a user turn when sending to Gemini/Antigravity upstreams.
// If the final turn contains a functionResponse, it is preserved without appending
// an empty user turn because upstream expects the model to generate the response.
// A model-role functionResponse turn carrying nested media is the exception:
// Antigravity rejects it as the final turn with 400 INVALID_ARGUMENT (#6524).
func EnsureGeminiTrailingUserContent(payload []byte, path string) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !EnsureGeminiTrailingUserContentDoc(doc, path) {
		return payload
	}
	return doc.Bytes()
}

// EnsureGeminiTrailingUserContentDoc ensures that the contents array at the given path
// ends with a user turn in place on doc.
func EnsureGeminiTrailingUserContentDoc(doc *ojson.Doc, path string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	contents := doc.Get(path)
	if !contents.IsArray() {
		return false
	}
	contentArray := contents.Array()
	if len(contentArray) == 0 {
		return false
	}
	lastContent := contentArray[len(contentArray)-1]
	lastRole := lastContent.Get("role").String()
	if lastRole != "model" && lastRole != "assistant" {
		return false
	}
	if contentHasFunctionResponse(lastContent) && !contentHasFunctionResponseMedia(lastContent) {
		return false
	}

	contentItems := make([][]byte, 0, len(contentArray)+1)
	for _, content := range contentArray {
		contentItems = append(contentItems, []byte(content.Raw))
	}
	contentItems = append(contentItems, emptyGeminiUserTurnJSON)

	return doc.SetRawBytes(path, translatorcommon.JoinRawArray(contentItems)) == nil
}

// DropGeminiTrailingThoughtOnlyContent removes trailing model turns made only of
// thought parts, keeping at least one turn. Such a tail comes from a replayed
// trailing reasoning item; dropping it avoids relying only on the synthetic
// empty user turn, which did not prevent Antigravity (#6530) or Vertex (#6630)
// from rejecting the request as ending with a model turn.
func DropGeminiTrailingThoughtOnlyContent(payload []byte, path string) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !DropGeminiTrailingThoughtOnlyContentDoc(doc, path) {
		return payload
	}
	return doc.Bytes()
}

// DropGeminiTrailingThoughtOnlyContentDoc removes trailing model turns made only of
// thought parts in place on doc, keeping at least one turn.
func DropGeminiTrailingThoughtOnlyContentDoc(doc *ojson.Doc, path string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	contents := doc.Get(path)
	if !contents.IsArray() {
		return false
	}
	contentArray := contents.Array()
	keep := len(contentArray)
	for keep > 1 && contentIsThoughtOnlyModelTurn(contentArray[keep-1]) {
		keep--
	}
	if keep == len(contentArray) {
		return false
	}

	contentItems := make([][]byte, 0, keep)
	for _, content := range contentArray[:keep] {
		contentItems = append(contentItems, []byte(content.Raw))
	}
	return doc.SetRawBytes(path, translatorcommon.JoinRawArray(contentItems)) == nil
}

func contentIsThoughtOnlyModelTurn(content ojson.Result) bool {
	role := content.Get("role").String()
	if role != "model" && role != "assistant" {
		return false
	}
	parts := content.Get("parts").Array()
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !part.Get("thought").Bool() || part.Get("functionCall").Exists() || part.Get("functionResponse").Exists() {
			return false
		}
	}
	return true
}

// EnsureGeminiBoundaryUserContent ensures that the contents array at the given path
// both starts and ends with a user turn when sending to Gemini/Antigravity upstreams.
func EnsureGeminiBoundaryUserContent(payload []byte, path string) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !EnsureGeminiBoundaryUserContentDoc(doc, path) {
		return payload
	}
	return doc.Bytes()
}

// EnsureGeminiBoundaryUserContentDoc ensures that the contents array at the given path
// both starts and ends with a user turn in place on doc.
func EnsureGeminiBoundaryUserContentDoc(doc *ojson.Doc, path string) bool {
	leading := EnsureGeminiLeadingUserContentDoc(doc, path)
	trailing := EnsureGeminiTrailingUserContentDoc(doc, path)
	return leading || trailing
}
