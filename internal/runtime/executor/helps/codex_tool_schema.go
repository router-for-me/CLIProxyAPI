package helps

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	toolschema "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/tool-schema"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// codexComplexUnionBranchThreshold is the minimum number of union branches (oneOf / anyOf)
	// required before considering a pure constant union eligible for semantic enum normalization.
	codexComplexUnionBranchThreshold = 8
)

// codexNumericBoundKeywords lists the JSON Schema numeric bound keywords that
// the Codex backend rejects with HTTP 500 when set to an integer outside int64.
// sanitizeCodexNumericBounds decides how each one is rewritten.
var codexNumericBoundKeywords = [...]string{
	"minimum",
	"maximum",
	"exclusiveMinimum",
	"exclusiveMaximum",
}

// IsCodexUserAgent reports whether headers contain a User-Agent indicating a Codex client.
func IsCodexUserAgent(headers http.Header) bool {
	return toolschema.IsCodexUserAgent(headers)
}

// NormalizeCodexToolIntegerTypes normalizes specified tool parameter declarations
// from number to integer for Codex clients across supported tool formats.
func NormalizeCodexToolIntegerTypes(body []byte, headers http.Header) []byte {
	return toolschema.NormalizeCodexToolIntegerTypes(body, headers)
}

// NormalizeCodexToolSchemas inspects function tools in a Codex request payload
// and simplifies pure constant union combinations (e.g. large oneOf branch sets
// representing enums with descriptions, as emitted by MCP servers) into semantically
// equivalent enum lists, and sanitizes unsupported schema patterns.
// It does not normalize tool integer types because upstream OpenAI Codex models
// strictly validate reserved tool schemas (e.g. collaboration.wait_agent).
func NormalizeCodexToolSchemas(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
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

	log.Debugf("codex: normalized schema for tool %s to avoid upstream abort", tool.Get("name").String())
	return updatedTool, true
}

func normalizeCodexParameters(params gjson.Result) ([]byte, bool) {
	rawParams := []byte(params.Raw)
	changed := false

	if sanitizedParams, patternChanged := stripIncompatiblePatternsFromJSON(rawParams); patternChanged {
		rawParams = sanitizedParams
		changed = true
		params = gjson.ParseBytes(rawParams)
	}

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

// stripIncompatiblePatternsFromJSON recursively removes pattern attributes that strict
// upstream validators reject: unsupported Unicode property escapes (\p{...} / \P{...})
// and the octal NUL escape (\0), and rewrites numeric bounds (minimum, maximum,
// exclusiveMinimum, exclusiveMaximum) outside the signed 64-bit integer range
// (#6579) so they keep the tool contract's strength without tripping the backend.
// See util.HasUnsupportedUnicodePropertyEscape for the regex predicate and upstream
// errors, sanitizeCodexNumericBounds for the bound semantics.
// It is schema-aware: only subschemas under known JSON Schema keyword locations are visited,
// preventing accidental deletion of keys inside user data (e.g. description, default, enum).
func stripIncompatiblePatternsFromJSON(raw []byte) ([]byte, bool) {
	rawStr := string(raw)
	// The fast path avoids parsing when no candidate escape or numeric bound is present.
	// Patterns arrive JSON-escaped, so a literal backslash before '0' is `\\0` in the raw bytes.
	// "inimum" and "aximum" match minimum, maximum, exclusiveMinimum, and exclusiveMaximum.
	if !strings.Contains(rawStr, `\p{`) && !strings.Contains(rawStr, `\P{`) && !strings.Contains(rawStr, `\u`) &&
		!strings.Contains(rawStr, `\\0`) && !strings.Contains(rawStr, "inimum") && !strings.Contains(rawStr, "aximum") {
		return raw, false
	}
	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil || root == nil {
		return raw, false
	}
	// Verify no trailing garbage
	var dummy any
	if err := dec.Decode(&dummy); err != io.EOF {
		return raw, false
	}
	if !stripIncompatiblePatterns(root) {
		return raw, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return raw, false
	}
	return bytes.TrimSpace(buf.Bytes()), true
}

// codexNumericBoundClass describes how a numeric bound literal relates to the
// int64 range.
type codexNumericBoundClass uint8

const (
	// codexBoundUnrestricted covers fractional values and literals that do not
	// describe a finite integer: the backend accepts them, so they stay as written.
	codexBoundUnrestricted codexNumericBoundClass = iota
	codexBoundInRange
	codexBoundAboveInt64
	codexBoundBelowInt64
)

// int64 edge literals used when an out-of-range bound has to be rewritten
// instead of dropped.
const (
	codexMinInt64Literal = "-9223372036854775808"
	codexMaxInt64Literal = "9223372036854775807"
)

// classifyCodexNumericBound inspects a JSON number literal with bounded work —
// mantissa digit count and exponent decide the int64-range relation, nothing is
// materialized — and reports whether the value is an integer outside int64 and
// in which direction it leaves the range.
func classifyCodexNumericBound(number json.Number) codexNumericBoundClass {
	raw := strings.TrimSpace(number.String())
	if raw == "" {
		return codexBoundUnrestricted
	}
	if !strings.ContainsAny(raw, ".eE") {
		if _, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil {
			return codexBoundInRange
		} else if errors.Is(errParse, strconv.ErrRange) {
			if strings.HasPrefix(raw, "-") {
				return codexBoundBelowInt64
			}
			return codexBoundAboveInt64
		}
		return codexBoundUnrestricted
	}

	s := raw
	negative := false
	if strings.HasPrefix(s, "-") {
		negative = true
		s = s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}

	mantissa := s
	var exp int64
	if idx := strings.IndexAny(s, "eE"); idx >= 0 {
		mantissa = s[:idx]
		expStr := s[idx+1:]
		parsedExp, errExp := strconv.ParseInt(expStr, 10, 64)
		if errExp != nil {
			if errors.Is(errExp, strconv.ErrRange) {
				// Magnitude far beyond int64 unless the exponent is negative
				// (tiny value) or the mantissa is zero.
				if strings.HasPrefix(expStr, "-") || strings.Trim(mantissa, "0.") == "" {
					return codexBoundUnrestricted
				}
				if negative {
					return codexBoundBelowInt64
				}
				return codexBoundAboveInt64
			}
			return codexBoundUnrestricted
		}
		exp = parsedExp
	}

	intPart := mantissa
	fracPart := ""
	if dotIdx := strings.IndexByte(mantissa, '.'); dotIdx >= 0 {
		intPart = mantissa[:dotIdx]
		fracPart = mantissa[dotIdx+1:]
		if strings.IndexByte(fracPart, '.') >= 0 {
			return codexBoundUnrestricted
		}
	}
	if intPart == "" && fracPart == "" {
		return codexBoundUnrestricted
	}
	for i := 0; i < len(intPart); i++ {
		if intPart[i] < '0' || intPart[i] > '9' {
			return codexBoundUnrestricted
		}
	}
	for i := 0; i < len(fracPart); i++ {
		if fracPart[i] < '0' || fracPart[i] > '9' {
			return codexBoundUnrestricted
		}
	}

	allDigits := intPart + fracPart
	sig := strings.TrimLeft(allDigits, "0")
	if sig == "" {
		return codexBoundUnrestricted
	}
	if exp < -int64(len(raw)) {
		return codexBoundUnrestricted
	}
	if exp > int64(len(raw))+20 {
		if negative {
			return codexBoundBelowInt64
		}
		return codexBoundAboveInt64
	}
	trimmed := strings.TrimRight(sig, "0")
	trailingZeros := int64(len(sig) - len(trimmed))
	effectiveExp := exp - int64(len(fracPart)) + trailingZeros
	if effectiveExp < 0 {
		return codexBoundUnrestricted
	}
	if int64(len(trimmed))+effectiveExp > 19 {
		if negative {
			return codexBoundBelowInt64
		}
		return codexBoundAboveInt64
	}

	intStr := trimmed + strings.Repeat("0", int(effectiveExp))
	if negative {
		intStr = "-" + intStr
	}
	if _, errParse := strconv.ParseInt(intStr, 10, 64); errParse != nil {
		if errors.Is(errParse, strconv.ErrRange) {
			if negative {
				return codexBoundBelowInt64
			}
			return codexBoundAboveInt64
		}
		return codexBoundUnrestricted
	}
	return codexBoundInRange
}

// codexNumericBoundDomainKind mirrors the instance types numeric bounds can bind.
type codexNumericBoundDomainKind uint8

const (
	codexDomainInteger codexNumericBoundDomainKind = iota
	codexDomainNumber
	codexDomainOther
)

// codexNumericBoundDomain reports which instance types a schema's numeric bounds
// can constrain. Schemas without a usable type are treated as numeric because
// JSON Schema bounds bind whatever numeric instances appear, while explicitly
// non-numeric types make any numeric bound a dead constraint.
func codexNumericBoundDomain(typeVal any) codexNumericBoundDomainKind {
	switch t := typeVal.(type) {
	case string:
		switch t {
		case "integer":
			return codexDomainInteger
		case "number":
			return codexDomainNumber
		}
		return codexDomainOther
	case []any:
		integer, number := false, false
		for _, item := range t {
			if name, ok := item.(string); ok {
				switch name {
				case "integer":
					integer = true
				case "number":
					number = true
				}
			}
		}
		if number {
			return codexDomainNumber
		}
		if integer {
			return codexDomainInteger
		}
		return codexDomainOther
	}
	return codexDomainNumber
}

// sanitizeCodexNumericBounds rewrites the schema's out-of-int64 bound keywords so
// the Codex backend no longer answers with an opaque HTTP 500 that cools the
// whole credential (#6579) while the tool contract keeps its strength (review on
// #6591):
//   - a bound no int64 can violate (upper bound above MaxInt64, lower bound below
//     MinInt64) is a tautology over integer schemas and is dropped; on numeric
//     schemas bigger JSON numbers still exist, so the bound clamps to the nearest
//     int64 edge instead — an exclusive bound rewrites to the non-exclusive edge,
//     since the original bound admits the edge value;
//   - opposite-side bounds (upper bound below MinInt64, lower bound above
//     MaxInt64) admit no int64 at all; clamping or dropping would widen the
//     contract, so the schema collapses to an in-range unsatisfiable pair;
//   - explicitly non-numeric schemas cannot bind numeric instances, so dead
//     out-of-range bounds are simply removed.
func sanitizeCodexNumericBounds(schema map[string]any) bool {
	domain := codexNumericBoundDomain(schema["type"])
	oppositeSide := false
	changed := false
	for _, boundKey := range codexNumericBoundKeywords {
		boundVal, ok := schema[boundKey].(json.Number)
		if !ok {
			continue
		}
		class := classifyCodexNumericBound(boundVal)
		if class != codexBoundAboveInt64 && class != codexBoundBelowInt64 {
			continue
		}
		upper := boundKey == "maximum" || boundKey == "exclusiveMaximum"
		switch {
		case domain == codexDomainOther:
			delete(schema, boundKey)
		case (upper && class == codexBoundAboveInt64) || (!upper && class == codexBoundBelowInt64):
			// Tautology direction: no int64 value can violate the bound.
			switch {
			case domain == codexDomainInteger:
				delete(schema, boundKey)
			case boundKey == "exclusiveMaximum":
				rewriteCodexExclusiveBound(schema, "maximum", boundKey, codexMaxInt64Literal)
			case boundKey == "exclusiveMinimum":
				rewriteCodexExclusiveBound(schema, "minimum", boundKey, codexMinInt64Literal)
			case upper:
				schema[boundKey] = json.Number(codexMaxInt64Literal)
			default:
				schema[boundKey] = json.Number(codexMinInt64Literal)
			}
		default:
			// Opposite side: clamp the bound to its own edge; the contradicting
			// pair below keeps the schema unsatisfiable.
			if upper {
				schema[boundKey] = json.Number(codexMinInt64Literal)
			} else {
				schema[boundKey] = json.Number(codexMaxInt64Literal)
			}
			oppositeSide = true
		}
		changed = true
	}
	if oppositeSide && domain != codexDomainOther {
		schema["minimum"] = json.Number(codexMaxInt64Literal)
		schema["maximum"] = json.Number(codexMinInt64Literal)
	}
	return changed
}

// rewriteCodexExclusiveBound rewrites an out-of-int64 exclusive bound on a numeric
// schema to the non-exclusive int64 edge: the tautological bound admits the edge
// value, so an exclusive rewrite would reject a value the original contract allows.
// A non-exclusive keyword already present dominates the tautology, so the exclusive
// keyword is just removed.
func rewriteCodexExclusiveBound(schema map[string]any, edgeKey, exclusiveKey, edgeLiteral string) {
	delete(schema, exclusiveKey)
	if _, exists := schema[edgeKey]; !exists {
		schema[edgeKey] = json.Number(edgeLiteral)
	}
}

func stripIncompatiblePatterns(v any) bool {
	changed := false
	switch schema := v.(type) {
	case map[string]any:
		if patternVal, ok := schema["pattern"].(string); ok && util.HasUnsupportedUnicodePropertyEscape(patternVal) {
			delete(schema, "pattern")
			changed = true
		}
		if sanitizeCodexNumericBounds(schema) {
			changed = true
		}

		// Inspect regex keys under patternProperties
		if patternProps, ok := schema["patternProperties"].(map[string]any); ok {
			for patternKey, subSchema := range patternProps {
				if util.HasUnsupportedUnicodePropertyEscape(patternKey) {
					delete(patternProps, patternKey)
					changed = true
				} else if stripIncompatiblePatterns(subSchema) {
					changed = true
				}
			}
		}

		for _, mapKey := range util.SchemaMapKeywords {
			if mapKey == "patternProperties" {
				continue
			}
			if subMap, ok := schema[mapKey].(map[string]any); ok {
				for _, subSchema := range subMap {
					if stripIncompatiblePatterns(subSchema) {
						changed = true
					}
				}
			}
		}

		for _, valKey := range util.SchemaValueKeywords {
			if val, exists := schema[valKey]; exists {
				switch sub := val.(type) {
				case map[string]any:
					if stripIncompatiblePatterns(sub) {
						changed = true
					}
				case []any:
					for _, item := range sub {
						if stripIncompatiblePatterns(item) {
							changed = true
						}
					}
				}
			}
		}
	case []any:
		for _, item := range schema {
			if stripIncompatiblePatterns(item) {
				changed = true
			}
		}
	}
	return changed
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
