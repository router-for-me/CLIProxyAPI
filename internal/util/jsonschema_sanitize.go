package util

import (
	"bytes"
	"encoding/json"
	"strings"
)

// SanitizeGeminiSchemaJSON removes JSON-Schema keywords that are not supported by
// Gemini Code Assist / Antigravity function schema parsing (notably "$ref").
//
// It also attempts to inline local "$ref" targets when they reference "#/$defs/*"
// or "#/definitions/*". If inlining is not possible, the "$ref" key is removed.
//
// If the input isn't valid JSON, the original bytes are returned unchanged.
func SanitizeGeminiSchemaJSON(raw []byte) []byte {
	// Fast path: avoid allocations for common schemas.
	if !bytes.Contains(raw, []byte(`"$ref"`)) &&
		!bytes.Contains(raw, []byte(`"$defs"`)) &&
		!bytes.Contains(raw, []byte(`"definitions"`)) &&
		!bytes.Contains(raw, []byte(`"$schema"`)) {
		return raw
	}

	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return raw
	}

	root = sanitizeSchemaNode(root, root)

	out, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return out
}

func sanitizeSchemaNode(node any, root any) any {
	switch v := node.(type) {
	case map[string]any:
		// Attempt to inline "$ref" if possible.
		if refVal, ok := v["$ref"]; ok {
			if refStr, okStr := refVal.(string); okStr && strings.TrimSpace(refStr) != "" {
				if resolved, okResolved := resolveLocalSchemaRef(root, refStr); okResolved {
					if copied, okMap := deepCopyJSON(resolved).(map[string]any); okMap {
						v = copied
					} else {
						// Resolved to non-object, drop the ref.
						delete(v, "$ref")
					}
				} else {
					delete(v, "$ref")
				}
			} else {
				delete(v, "$ref")
			}
		}

		// Remove unsupported schema meta keys.
		delete(v, "$schema")
		delete(v, "$defs")
		delete(v, "definitions")

		// Recurse through remaining keys.
		for key, val := range v {
			v[key] = sanitizeSchemaNode(val, root)
		}
		return v
	case []any:
		for i := range v {
			v[i] = sanitizeSchemaNode(v[i], root)
		}
		return v
	default:
		return node
	}
}

func resolveLocalSchemaRef(root any, ref string) (any, bool) {
	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, false
	}
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}

	var defs any
	var prefix string
	switch {
	case strings.HasPrefix(ref, "#/$defs/"):
		defs = rootMap["$defs"]
		prefix = "#/$defs/"
	case strings.HasPrefix(ref, "#/definitions/"):
		defs = rootMap["definitions"]
		prefix = "#/definitions/"
	default:
		return nil, false
	}
	defMap, ok := defs.(map[string]any)
	if !ok {
		return nil, false
	}
	key := strings.TrimPrefix(ref, prefix)
	key = strings.Trim(key, "/")
	if key == "" {
		return nil, false
	}

	// Support nested defs: #/$defs/a/b/c
	parts := strings.Split(key, "/")
	var cur any = defMap
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		n, ok := m[p]
		if !ok {
			return nil, false
		}
		cur = n
	}
	return cur, true
}

func deepCopyJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopyJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = deepCopyJSON(x[i])
		}
		return out
	default:
		return v
	}
}
