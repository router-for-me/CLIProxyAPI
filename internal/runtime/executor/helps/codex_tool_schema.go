package helps

import (
	"math/big"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// codexComplexUnionBranchThreshold is the minimum number of union branches (oneOf / anyOf)
	// required before considering a pure constant union eligible for semantic enum normalization.
	codexComplexUnionBranchThreshold = 8
)

// NormalizeCodexToolSchemas inspects function tools in a Codex request payload
// and simplifies pure constant union combinations (e.g. large oneOf branch sets
// representing enums with descriptions, as emitted by MCP servers) into semantically
// equivalent enum lists.
// Only unions mathematically proven to be semantically equivalent to enum definitions
// are modified; all other structures, property names, types, and constraints remain untouched.
func NormalizeCodexToolSchemas(body []byte) []byte {
	updatedTools, changed := normalizeCodexToolList(gjson.GetBytes(body, "tools"))
	if !changed {
		return body
	}
	out, errSet := sjson.SetRawBytes(body, "tools", updatedTools)
	if errSet != nil {
		return body
	}
	log.Debugf("codex: normalized tool schemas to prevent upstream failure")
	return out
}

// normalizeCodexToolList batches changed elements so a long request (or a wide
// namespace) is copied once rather than once per tool. Copy the gaps verbatim
// to retain array formatting and unchanged declarations.
func normalizeCodexToolList(tools gjson.Result) ([]byte, bool) {
	if !tools.IsArray() {
		return nil, false
	}
	var out []byte
	offset := 0
	tools.ForEach(func(_, tool gjson.Result) bool {
		updated, changed := normalizeCodexTool(tool)
		if !changed {
			return true
		}
		if out == nil {
			out = make([]byte, 0, len(tools.Raw))
		}
		// ForEach indexes share the source of the containing array's index.
		start := tool.Index - tools.Index
		out = append(out, tools.Raw[offset:start]...)
		out = append(out, updated...)
		offset = start + len(tool.Raw)
		return true
	})
	if out == nil {
		return nil, false
	}
	return append(out, tools.Raw[offset:]...), true
}

func normalizeCodexTool(tool gjson.Result) ([]byte, bool) {
	toolType := tool.Get("type").String()
	// Handle namespace tools (e.g. multi-agent nested tools)
	if toolType == "namespace" {
		updatedTools, changed := normalizeCodexToolList(tool.Get("tools"))
		if !changed {
			return nil, false
		}
		updated, errSet := sjson.SetRawBytes([]byte(tool.Raw), "tools", updatedTools)
		return updated, errSet == nil
	}

	if toolType != "function" && toolType != "custom" {
		return nil, false
	}

	params := tool.Get("parameters")
	if !params.Exists() || !params.IsObject() {
		return nil, false
	}

	rawTool := []byte(tool.Raw)
	updatedParams, paramsChanged := normalizeCodexParameters(params)
	if !paramsChanged {
		return nil, false
	}

	updatedTool, errSet := sjson.SetRawBytes(rawTool, "parameters", updatedParams)
	if errSet != nil {
		return nil, false
	}

	log.Debugf("codex: simplified complex schema unions for tool %s to avoid upstream abort", tool.Get("name").String())
	return updatedTool, true
}

func normalizeCodexParameters(params gjson.Result) ([]byte, bool) {
	rawParams := []byte(params.Raw)
	changed := false

	properties := params.Get("properties")
	if properties.Exists() && properties.IsObject() {
		for propName, propVal := range properties.Map() {
			updatedProp, propChanged := normalizeCodexPropertySchema(propVal)
			if propChanged {
				escapedKey := escapeCodexSjsonKey(propName)
				var errSet error
				rawParams, errSet = sjson.SetRawBytes(rawParams, "properties."+escapedKey, updatedProp)
				if errSet == nil {
					changed = true
				}
			}
		}
	}

	return rawParams, changed
}

func normalizeCodexPropertySchema(prop gjson.Result) ([]byte, bool) {
	if !prop.IsObject() {
		return nil, false
	}

	hasOneOf := prop.Get("oneOf").Exists()
	hasAnyOf := prop.Get("anyOf").Exists()
	// If both oneOf and anyOf are present on the same property, leave untouched to preserve compound constraints
	if hasOneOf && hasAnyOf {
		return nil, false
	}

	unionName := ""
	if hasOneOf {
		unionName = "oneOf"
	} else if hasAnyOf {
		unionName = "anyOf"
	} else {
		return nil, false
	}

	union := prop.Get(unionName)
	if !union.IsArray() || len(union.Array()) < codexComplexUnionBranchThreshold {
		return nil, false
	}

	branches := union.Array()
	constRawValues := make([]string, 0, len(branches))
	constSemanticKeys := make([]string, 0, len(branches))
	seenSemanticKeys := make(map[string]struct{}, len(branches))
	pureConsts := true

	for _, branch := range branches {
		canonicalKey, rawJSON, ok := isPureConstBranch(branch)
		if !ok {
			pureConsts = false
			break
		}
		if _, seen := seenSemanticKeys[canonicalKey]; seen {
			// Duplicate semantic value in oneOf violates exclusivity; keep original schema
			pureConsts = false
			break
		}
		seenSemanticKeys[canonicalKey] = struct{}{}
		constSemanticKeys = append(constSemanticKeys, canonicalKey)
		constRawValues = append(constRawValues, rawJSON)
	}

	// Only transform if every branch is proven to be a pure, unique const definition
	if !pureConsts || len(constRawValues) == 0 {
		return nil, false
	}

	rawProp := []byte(prop.Raw)
	existingEnum := prop.Get("enum")
	if existingEnum.Exists() && existingEnum.IsArray() {
		existingEnumKeys := make([]string, 0, len(existingEnum.Array()))
		for _, v := range existingEnum.Array() {
			key, ok := canonicalJSONValueKey(v)
			if !ok {
				return nil, false
			}
			existingEnumKeys = append(existingEnumKeys, key)
		}
		// Only remove the redundant union if existing enum is proven semantically identical
		if equalCanonicalSets(existingEnumKeys, constSemanticKeys) {
			rawProp, _ = sjson.DeleteBytes(rawProp, unionName)
			return rawProp, true
		}
		return nil, false
	}

	// Migrate the pure const union to an enum using raw JSON tokens to avoid any numeric precision loss
	rawEnumJSON := []byte("[" + strings.Join(constRawValues, ",") + "]")
	rawProp, errEnum := sjson.SetRawBytes(rawProp, "enum", rawEnumJSON)
	if errEnum != nil {
		return nil, false
	}
	rawProp, _ = sjson.DeleteBytes(rawProp, unionName)
	return rawProp, true
}

func isPureConstBranch(branch gjson.Result) (canonicalKey string, rawJSON string, ok bool) {
	if !branch.IsObject() {
		return "", "", false
	}
	constVal := branch.Get("const")
	if !constVal.Exists() {
		return "", "", false
	}
	// Verify no other schema validation constraints exist in this branch
	for key := range branch.Map() {
		if key != "const" && key != "description" && key != "title" {
			return "", "", false
		}
	}
	key, ok := canonicalJSONValueKey(constVal)
	if !ok {
		return "", "", false
	}
	return key, constVal.Raw, true
}

func canonicalJSONValueKey(val gjson.Result) (string, bool) {
	switch val.Type {
	case gjson.String:
		return "s:" + val.String(), true
	case gjson.Number:
		raw := strings.TrimSpace(val.Raw)
		var r big.Rat
		if _, ok := r.SetString(raw); ok {
			return "n:" + r.RatString(), true
		}
		return "n:" + raw, true
	case gjson.True:
		return "b:true", true
	case gjson.False:
		return "b:false", true
	case gjson.Null:
		return "null", true
	default:
		return "", false
	}
}

func equalCanonicalSets(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	setA := make(map[string]struct{}, len(a))
	for _, v := range a {
		setA[v] = struct{}{}
	}
	for _, v := range b {
		if _, ok := setA[v]; !ok {
			return false
		}
	}
	return len(setA) == len(a)
}

// escapeCodexSjsonKey escapes dots, colons, and backslashes in property keys so that sjson treats
// keys containing dots (e.g. "my.field") or colons (e.g. ":action") as a single literal key rather
// than nested paths or control syntax.
func escapeCodexSjsonKey(key string) string {
	key = strings.ReplaceAll(key, `\`, `\\`)
	key = strings.ReplaceAll(key, `.`, `\.`)
	key = strings.ReplaceAll(key, `:`, `\:`)
	return key
}
