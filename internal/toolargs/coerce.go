// Package toolargs normalizes tool-call arguments relayed to OpenAI-compatible clients.
//
// Some upstream models emit an integer-typed tool argument as a whole-number float
// (e.g. 14380.0). Strict clients — notably the Codex CLI's Rust tool router, which
// deserializes `i32` fields exactly (openai/codex#33452) — reject the call. This package
// rewrites such values to their integer form, but ONLY when the request's own tool schema
// declares the parameter as `integer` and the value is exactly integral.
//
// Design constraints (deliberate, see CLIProxyAPI task notes):
//   - The schema of the same-named tool in THIS request is the sole authority. A missing,
//     duplicated, or schema-less tool, and any subschema whose type cannot be determined
//     ($ref, anyOf/oneOf/allOf/not, or a type list admitting "number") passes through.
//   - Never re-encode the whole arguments document through float64. Only the proven target
//     nodes are replaced, spliced in place on the ORIGINAL bytes, so big integers, key order,
//     whitespace, and every unrelated field survive byte-for-byte.
//   - Never invent a value: a non-integral float for an integer param is left for the client.
package toolargs

import (
	"encoding/json"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// CoerceIntegralFloats returns arguments with every JSON number that is (a) located at a
// position the schema declares `integer` and (b) written with a fraction/exponent yet exactly
// integral (14380.0, 1.438e4, -5.0) rewritten as a plain integer literal (14380, 14380, -5).
// The second result lists the JSON paths that were rewritten (e.g. "opts.retries", "ids[2]"),
// suitable for logging without exposing argument values. Malformed arguments, a nil or
// non-object schema, or any decode error return the input unchanged with no paths.
func CoerceIntegralFloats(arguments string, paramsSchema any) (string, []string) {
	root := resolveSchema(paramsSchema)
	if root == nil || arguments == "" {
		return arguments, nil
	}
	type edit struct {
		start, end int64
		text       string
	}
	var edits []edit
	var paths []string

	type frame struct {
		object    bool
		schema    map[string]any // schema for this container (nil = undetermined)
		expectKey bool
		key       string
		index     int
		path      string
		valSchema map[string]any // schema for the value about to be read
		valPath   string
	}
	var stack []*frame
	top := func() *frame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	// nextValue reports the schema/path for the next value in the current container and
	// advances the container's cursor once that value has been consumed.
	prepareValue := func() (map[string]any, string) {
		f := top()
		if f == nil {
			return root, ""
		}
		if f.object {
			return f.valSchema, f.valPath
		}
		p := f.path + "[" + strconv.Itoa(f.index) + "]"
		return schemaItems(f.schema), p
	}
	consumed := func() {
		f := top()
		if f == nil {
			return
		}
		if f.object {
			f.expectKey = true
			f.valSchema, f.valPath = nil, ""
		} else {
			f.index++
		}
	}

	dec := json.NewDecoder(strings.NewReader(arguments))
	dec.UseNumber()
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return arguments, nil
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				s, p := prepareValue()
				stack = append(stack, &frame{object: v == '{', schema: s, expectKey: v == '{', path: p})
				depth++
			case '}', ']':
				stack = stack[:len(stack)-1]
				depth--
				consumed()
			}
		case string:
			f := top()
			if f != nil && f.object && f.expectKey {
				f.expectKey = false
				f.key = v
				f.valSchema = schemaProperty(f.schema, v)
				if f.path == "" {
					f.valPath = v
				} else {
					f.valPath = f.path + "." + v
				}
				continue
			}
			consumed()
		case json.Number:
			s, p := prepareValue()
			raw := string(v)
			if isIntegerSchema(s) && looksFractional(raw) {
				if text, ok := exactIntegerText(raw); ok {
					end := dec.InputOffset()
					edits = append(edits, edit{start: end - int64(len(raw)), end: end, text: text})
					paths = append(paths, p)
				}
			}
			consumed()
		default: // bool, nil
			consumed()
		}
	}
	if depth != 0 || dec.More() {
		// Unbalanced or trailing garbage: not a single well-formed document; leave it alone.
		return arguments, nil
	}
	if len(edits) == 0 {
		return arguments, nil
	}
	var b strings.Builder
	b.Grow(len(arguments))
	var cursor int64
	for _, e := range edits {
		b.WriteString(arguments[cursor:e.start])
		b.WriteString(e.text)
		cursor = e.end
	}
	b.WriteString(arguments[cursor:])
	return b.String(), paths
}

// looksFractional reports whether a JSON number literal is written with a fraction or exponent
// (so a plain integer literal is never touched, even under an integer schema).
func looksFractional(raw string) bool {
	return strings.ContainsAny(raw, ".eE")
}

// exactIntegerText converts a JSON number literal to its exact integer text when the literal
// denotes an integer value (14380.0 → "14380", 1.438e4 → "14380", -5.0 → "-5"). It never
// rounds: 14380.5 yields ok=false.
func exactIntegerText(raw string) (string, bool) {
	r, ok := new(big.Rat).SetString(raw)
	if !ok || !r.IsInt() {
		return "", false
	}
	return r.Num().String(), true
}

// resolveSchema returns the schema as a map when its type can be determined locally, and nil
// when it is absent, not an object, or defers to $ref / a composition keyword we do not
// evaluate. Passing nil down the walk disables coercion beneath that node.
func resolveSchema(s any) map[string]any {
	m, ok := s.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	for _, k := range []string{"$ref", "anyOf", "oneOf", "allOf", "not"} {
		if _, present := m[k]; present {
			return nil
		}
	}
	return m
}

func schemaProperty(s map[string]any, key string) map[string]any {
	if s == nil {
		return nil
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		return nil
	}
	return resolveSchema(props[key])
}

func schemaItems(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	// Only the single-schema form of `items` is determinate; tuple form is not evaluated.
	return resolveSchema(s["items"])
}

// isIntegerSchema reports whether a resolved subschema declares exactly the integer type,
// optionally nullable. A type list that also admits "number" (or anything else) is ambiguous
// and returns false.
func isIntegerSchema(s map[string]any) bool {
	if s == nil {
		return false
	}
	switch t := s["type"].(type) {
	case string:
		return t == "integer"
	case []any:
		sawInteger := false
		for _, item := range t {
			name, ok := item.(string)
			if !ok {
				return false
			}
			switch name {
			case "integer":
				sawInteger = true
			case "null":
			default:
				return false
			}
		}
		return sawInteger
	}
	return false
}

// FindToolParams locates the parameters schema of the tool named toolName in an OpenAI-style
// request body, accepting both the Chat Completions shape (tools[].function.{name,parameters})
// and the Responses API shape (tools[].{name,parameters}). It fails closed (ok=false) when the
// name is empty, no tool matches, MORE than one tool matches (the schema cannot be chosen), or
// the matching tool carries no parameters object.
func FindToolParams(requestBody []byte, toolName string) (any, bool) {
	if toolName == "" || len(requestBody) == 0 {
		return nil, false
	}
	tools := gjson.GetBytes(requestBody, "tools")
	if !tools.IsArray() {
		return nil, false
	}
	var match gjson.Result
	matches := 0
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("function.name")
		params := tool.Get("function.parameters")
		if !name.Exists() {
			name = tool.Get("name")
			params = tool.Get("parameters")
		}
		if name.Type == gjson.String && name.Str == toolName {
			matches++
			match = params
		}
		return true
	})
	if matches != 1 || !match.Exists() || !match.IsObject() {
		return nil, false
	}
	var schema any
	if err := json.Unmarshal([]byte(match.Raw), &schema); err != nil {
		return nil, false
	}
	return schema, true
}
