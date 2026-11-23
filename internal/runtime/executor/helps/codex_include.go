package helps

import (
	neturl "net/url"
	"strings"

	"github.com/MoeForge/moego/ojson"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type codexOpenAIIncludeEntry struct {
	value        string
	platformOnly bool
}

// codexOpenAIResponsesOptionalIncludes lists the official OpenAI Responses
// ResponseIncludable entries (other than the always-required
// reasoning.encrypted_content) plus Codex's opt-in output.encrypted_content.
// Entries without platformOnly match the relative order emitted by the
// Responses-to-Codex translator so requests without platform-only values are a
// fast no-op.
var codexOpenAIResponsesOptionalIncludes = [...]codexOpenAIIncludeEntry{
	{value: "web_search_call.action.sources"},
	{value: "web_search_call.results"},
	{value: "file_search_call.results", platformOnly: true},
	{value: "message.input_image.image_url", platformOnly: true},
	{value: "computer_call_output.output.image_url", platformOnly: true},
	{value: "code_interpreter_call.outputs", platformOnly: true},
	{value: "message.output_text.logprobs", platformOnly: true},
	{value: "output.encrypted_content"},
}

// IsChatGPTCodexBackendBaseURL reports whether baseURL targets the ChatGPT
// Codex backend (or is empty or unparseable, failing closed to the conservative
// ChatGPT Codex allowlist).
func IsChatGPTCodexBackendBaseURL(baseURL string) bool {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return true
	}
	parsed, errParse := neturl.Parse(trimmed)
	if errParse != nil {
		return true
	}
	if !strings.EqualFold(strings.TrimRight(parsed.Hostname(), "."), "chatgpt.com") {
		return false
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	return cleanPath == "/backend-api/codex" || strings.HasPrefix(cleanPath, "/backend-api/codex/")
}

// PreserveCodexOpenAIResponsesIncludeDoc restores explicitly requested official
// OpenAI Responses include values (and output.encrypted_content) from the
// caller's OpenAI Responses payload onto doc when targeting a non-ChatGPT
// OpenAI Responses upstream (such as SIWC or a custom codex-api-key endpoint).
// Unknown values and non-string entries remain filtered, and
// reasoning.encrypted_content is always guaranteed first.
func PreserveCodexOpenAIResponsesIncludeDoc(doc *ojson.Doc, source []byte, from sdktranslator.Format) bool {
	if doc == nil || len(source) == 0 || !strings.EqualFold(strings.TrimSpace(from.String()), sdktranslator.FormatOpenAIResponse.String()) {
		return false
	}
	current := ojson.GetBytesNoCopy(source, "include")
	if !current.IsArray() {
		return false
	}

	var present [len(codexOpenAIResponsesOptionalIncludes)]bool
	hasPlatformOnly := false
	for _, value := range current.Array() {
		if value.Type != ojson.String {
			continue
		}
		entry := value.String()
		for i, allowed := range codexOpenAIResponsesOptionalIncludes {
			if entry == allowed.value {
				present[i] = true
				if allowed.platformOnly {
					hasPlatformOnly = true
				}
				break
			}
		}
	}
	if !hasPlatformOnly {
		return false
	}

	encoded := make([]byte, 0, 256)
	encoded = append(encoded, `["reasoning.encrypted_content"`...)
	for i, allowed := range codexOpenAIResponsesOptionalIncludes {
		if !present[i] {
			continue
		}
		encoded = append(encoded, `,"`...)
		encoded = append(encoded, allowed.value...)
		encoded = append(encoded, '"')
	}
	encoded = append(encoded, ']')
	return doc.SetRawBytes("include", encoded) == nil
}

// PreserveCodexOpenAIResponsesInclude is the byte-slice wrapper around
// PreserveCodexOpenAIResponsesIncludeDoc for keeping originalTranslated aligned
// before constructing a PayloadFinalizer.
func PreserveCodexOpenAIResponsesInclude(body []byte, source []byte, from sdktranslator.Format) []byte {
	if len(body) == 0 || len(source) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	if !PreserveCodexOpenAIResponsesIncludeDoc(doc, source, from) {
		return body
	}
	return doc.Bytes()
}

// StripResponsesIncludeValuesDoc removes any listed string values from doc's
// include array in place while preserving remaining entries and order.
func StripResponsesIncludeValuesDoc(doc *ojson.Doc, blocked ...string) bool {
	if doc == nil || doc.Len() == 0 || len(blocked) == 0 {
		return false
	}
	current := doc.Get("include")
	if !current.IsArray() {
		return false
	}
	values := current.Array()
	if len(values) == 0 {
		return false
	}
	isBlocked := func(s string) bool {
		for _, b := range blocked {
			if s == b {
				return true
			}
		}
		return false
	}

	removed := false
	kept := make([][]byte, 0, len(values))
	for _, value := range values {
		if value.Type == ojson.String && isBlocked(value.String()) {
			removed = true
			continue
		}
		kept = append(kept, []byte(value.Raw))
	}
	if !removed {
		return false
	}
	if len(kept) == 0 {
		return doc.Delete("include") == nil
	}
	encoded := make([]byte, 0, len(current.Raw))
	encoded = append(encoded, '[')
	for i, raw := range kept {
		if i > 0 {
			encoded = append(encoded, ',')
		}
		encoded = append(encoded, raw...)
	}
	encoded = append(encoded, ']')
	return doc.SetRawBytes("include", encoded) == nil
}
