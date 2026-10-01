package translator

import (
	"bytes"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// isResponsesClientFormat reports whether the client-facing response dialect
// carries function-call items of the shape this canonicalizer understands.
//
// Both dialects reach the same clients: the Codex client posts to
// /v1/responses and /backend-api/codex/responses, and both are served by
// OpenAIResponsesAPIHandler, whose HandlerType is openai-response. FormatCodex
// is the dialect used towards upstream Codex providers, not towards clients,
// but responses in it carry the same items, so it is covered too.
func isResponsesClientFormat(format Format) bool {
	return format == FormatOpenAIResponse || format == FormatCodex
}

// CanonicalizeCodexToolArguments rewrites integral float literals in Codex
// function-call arguments to their integer form.
//
// JSON has a single number type, so 2500.0 and 2500 are the same value and
// differ only lexically. Codex clients bind most tool parameters as integers
// and reject the float form even though the value is integral. Upstream
// addresses this on the request side by rewriting a fixed table of tool and
// parameter schemas from number to integer (upstream #6237, #6244); that table
// is finite, so anything outside it still produces a rejected literal.
//
// Only literals whose fractional part is entirely zeros are rewritten. Genuine
// floats (1.5), exponent forms (1e3, 1.0e3), digits inside string values and
// every other byte are left untouched. The input is returned unchanged when
// there is nothing to rewrite.
//
// Codex clients parse function-call arguments from the complete item carried
// by response.output_item.done and ignore both
// response.function_call_arguments.delta and
// response.function_call_arguments.done, so no cross-chunk state is needed.
func CanonicalizeCodexToolArguments(body []byte, stream bool) []byte {
	if len(body) == 0 {
		return body
	}
	if stream {
		switch gjson.GetBytes(body, "type").String() {
		case "response.output_item.added", "response.output_item.done":
			return canonicalizeCodexStreamItem(body)
		}
		return body
	}
	return canonicalizeCodexResponseBody(body)
}

// canonicalizeCodexStreamItem rewrites the tool payload of one output item
// event. Function calls carry it in arguments, custom tool calls in input.
// Custom input events (response.custom_tool_call_input.delta/.done) are left
// alone: Codex clients ignore function-call argument delta events the same
// way, and there is no client-side evidence that these carry the parsed form.
func canonicalizeCodexStreamItem(event []byte) []byte {
	itemType := gjson.GetBytes(event, "item.type").String()
	switch itemType {
	case "function_call":
		if updated := canonicalizeCodexArguments(event, "item.arguments"); updated != nil {
			return updated
		}
	case "custom_tool_call":
		if updated := canonicalizeCodexArguments(event, "item.input"); updated != nil {
			return updated
		}
	}
	return event
}

// canonicalizeCodexResponseBody rewrites every function-call item in a
// non-streaming Codex response. Custom tool calls carry their payload in
// input rather than arguments and are rewritten the same way.
func canonicalizeCodexResponseBody(body []byte) []byte {
	output := gjson.GetBytes(body, "output")
	if !output.IsArray() {
		return body
	}
	out := body
	for index, item := range output.Array() {
		var argumentsPath string
		switch gjson.Get(item.Raw, "type").String() {
		case "function_call":
			argumentsPath = "arguments"
		case "custom_tool_call":
			argumentsPath = "input"
		default:
			continue
		}
		updated := canonicalizeCodexArguments([]byte(item.Raw), argumentsPath)
		if updated == nil {
			continue
		}
		next, errSet := sjson.SetRawBytes(out, "output."+strconv.Itoa(index), updated)
		if errSet != nil {
			continue
		}
		out = next
	}
	return out
}

// canonicalizeCodexArguments rewrites the arguments value at argumentsPath and
// returns the updated document, or nil when nothing changed. The value may be
// either the string form Codex clients expect or an inline object produced by
// some translators.
func canonicalizeCodexArguments(document []byte, argumentsPath string) []byte {
	raw := gjson.GetBytes(document, argumentsPath)
	if !raw.Exists() {
		return nil
	}
	switch raw.Type {
	case gjson.String:
		value := raw.String()
		canonicalized := canonicalizeIntegralFloats([]byte(value))
		if bytes.Equal(canonicalized, []byte(value)) {
			return nil
		}
		out, errSet := sjson.SetBytes(document, argumentsPath, string(canonicalized))
		if errSet != nil {
			return nil
		}
		return out
	case gjson.JSON:
		canonicalized := canonicalizeIntegralFloats([]byte(raw.Raw))
		if bytes.Equal(canonicalized, []byte(raw.Raw)) {
			return nil
		}
		out, errSet := sjson.SetRawBytes(document, argumentsPath, canonicalized)
		if errSet != nil {
			return nil
		}
		return out
	default:
		return nil
	}
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// canonicalizeIntegralFloats rewrites integral float literals outside of
// string values. It never mutates the input.
func canonicalizeIntegralFloats(src []byte) []byte {
	var out []byte
	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"':
			end := scanJSONString(src, i)
			if out != nil {
				out = append(out, src[i:end]...)
			}
			i = end
		case c == '-' || isASCIIDigit(c):
			end, replacement := scanJSONNumber(src, i)
			if len(replacement) != end-i {
				if out == nil {
					out = make([]byte, 0, len(src))
					out = append(out, src[:i]...)
				}
			}
			if out != nil {
				out = append(out, replacement...)
			}
			i = end
		default:
			if out != nil {
				out = append(out, c)
			}
			i++
		}
	}
	if out == nil {
		return src
	}
	return out
}

// scanJSONString returns the offset just past the string literal starting at
// start, honouring backslash escapes.
func scanJSONString(src []byte, start int) int {
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(src)
}

// scanJSONNumber returns the offset just past the JSON number starting at
// start and the bytes to emit in its place.
func scanJSONNumber(src []byte, start int) (int, []byte) {
	i := start
	if i < len(src) && src[i] == '-' {
		i++
	}
	digitStart := i
	for i < len(src) && isASCIIDigit(src[i]) {
		i++
	}
	if i == digitStart {
		// Not a well-formed number. Leave the byte untouched and move on.
		return start + 1, src[start : start+1]
	}
	intEnd := i
	fracEnd := intEnd
	if i+1 < len(src) && src[i] == '.' && isASCIIDigit(src[i+1]) {
		i++
		for i < len(src) && isASCIIDigit(src[i]) {
			i++
		}
		fracEnd = i
	}
	end := fracEnd
	if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
		j := i + 1
		if j < len(src) && (src[j] == '+' || src[j] == '-') {
			j++
		}
		digits := j
		for j < len(src) && isASCIIDigit(src[j]) {
			j++
		}
		if j > digits {
			// Exponent form is never rewritten.
			return j, src[start:j]
		}
	}
	if fracEnd == intEnd {
		return end, src[start:end]
	}
	for _, d := range src[intEnd+1 : fracEnd] {
		if d != '0' {
			return end, src[start:end]
		}
	}
	return end, src[start:intEnd]
}
