package toolschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// codexComplexUnionBranchThreshold is the minimum number of union branches (oneOf / anyOf)
	// required before considering a pure constant union eligible for semantic enum normalization.
	codexComplexUnionBranchThreshold = 8
)

// codexNumericBoundKeywords lists the JSON Schema numeric bound keywords that
// the Codex backend rejects with HTTP 500 when set to an integer outside int64.
var codexNumericBoundKeywords = [...]string{
	"minimum",
	"maximum",
	"exclusiveMinimum",
	"exclusiveMaximum",
}

// IsCodexUserAgent reports whether headers contain a User-Agent indicating a Codex client.
func IsCodexUserAgent(headers http.Header) bool {
	if headers == nil {
		return false
	}
	ua := headerValueCaseInsensitive(headers, "User-Agent")
	if ua == "" {
		return false
	}
	return strings.Contains(strings.ToLower(ua), "codex")
}

func headerValueCaseInsensitive(headers http.Header, name string) string {
	for k, values := range headers {
		if strings.EqualFold(k, name) {
			for _, v := range values {
				if trimmed := strings.TrimSpace(v); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

// Keys are explicit schema paths relative to parameters.properties, not recursive field names.
var codexClientToolIntegerFields = map[string]map[string]struct{}{
	"exec_command": {
		"yield_time_ms":     struct{}{},
		"max_output_tokens": struct{}{},
		"timeout_ms":        struct{}{},
	},
	"write_stdin": {
		"session_id":        struct{}{},
		"yield_time_ms":     struct{}{},
		"max_output_tokens": struct{}{},
	},
	"sleep": {
		"duration_ms": struct{}{},
	},
	"wait_agent": {
		"timeout_ms": struct{}{},
	},
	"wait": {
		"yield_time_ms": struct{}{},
		"max_tokens":    struct{}{},
	},
	"tool_search": {
		"limit": struct{}{},
	},
	"test_sync_tool": {
		"sleep_before_ms":                 struct{}{},
		"sleep_after_ms":                  struct{}{},
		"participants":                    struct{}{},
		"timeout_ms":                      struct{}{},
		"barrier.properties.participants": struct{}{},
		"barrier.properties.timeout_ms":   struct{}{},
	},
	"create_goal": {
		"token_budget": struct{}{},
	},
	"get_channels": {
		"limit": struct{}{},
	},
	"list_threads": {
		"limit":              struct{}{},
		"max_chars_per_post": struct{}{},
	},
	"search_posts": {
		"limit":              struct{}{},
		"max_chars_per_post": struct{}{},
	},
	"read_thread": {
		"limit":              struct{}{},
		"max_chars_per_post": struct{}{},
	},
	"read_post": {
		"offset_chars": struct{}{},
		"limit_chars":  struct{}{},
	},
	"memories__list": {
		"max_results": struct{}{},
	},
	"memories__read": {
		"line_offset": struct{}{},
		"max_lines":   struct{}{},
	},
	"memories__search": {
		"context_lines": struct{}{},
		"max_results":   struct{}{},
	},
	"history__list_windows": {
		"limit": struct{}{},
	},
	"history__list_items": {
		"limit":              struct{}{},
		"max_chars_per_item": struct{}{},
	},
	"history__read_item": {
		"offset_chars": struct{}{},
		"limit_chars":  struct{}{},
	},
	"history__search_contents": {
		"limit": struct{}{},
	},
	"notes__list_files_by_prefix": {
		"max_results": struct{}{},
	},
	"notes__read_file": {
		"start_line": struct{}{},
		"stop_line":  struct{}{},
		// Codex declares signed line numbers in the first nullable union branch.
		"start_line.anyOf.0": struct{}{},
		"stop_line.anyOf.0":  struct{}{},
	},
	"notes__search_contents": {
		"max_matches_per_file": struct{}{},
		"max_files":            struct{}{},
	},
	"image_gen__imagegen": {
		"num_last_images_to_include": struct{}{},
	},
	"web__run": {
		"search_query.items.properties.recency": struct{}{},
		"image_query.items.properties.recency":  struct{}{},
		"open.items.properties.lineno":          struct{}{},
		"click.items.properties.id":             struct{}{},
		"screenshot.items.properties.pageno":    struct{}{},
		"weather.items.properties.duration":     struct{}{},
		"sports.items.properties.num_games":     struct{}{},
	},
}

func matchCodexTargetTool(toolName string) map[string]struct{} {
	baseName := strings.TrimSpace(toolName)
	if strings.HasPrefix(baseName, "functions__") {
		baseName = strings.TrimPrefix(baseName, "functions__")
	} else if strings.HasPrefix(baseName, "collab__") {
		baseName = strings.TrimPrefix(baseName, "collab__")
	}
	switch baseName {
	case "multi_agent_v1__wait_agent", "collaboration__wait_agent":
		baseName = "wait_agent"
	case "collaboration__get_channels", "collaboration__list_threads", "collaboration__search_posts",
		"collaboration__read_thread", "collaboration__read_post":
		baseName = strings.TrimPrefix(baseName, "collaboration__")
	}
	return codexClientToolIntegerFields[baseName]
}

func normalizeCodexToolFieldTypesDoc(doc *ojson.Doc, paramsPath string, params ojson.Result, targetFields map[string]struct{}) bool {
	if doc == nil || len(targetFields) == 0 {
		return false
	}
	properties := params.Get("properties")
	if !properties.Exists() || !properties.IsObject() {
		return false
	}
	changed := false
	for fieldName := range targetFields {
		prop := properties.Get(fieldName)
		if !prop.Exists() {
			continue
		}
		typeVal := prop.Get("type")
		if !typeVal.Exists() {
			continue
		}
		typePath := paramsPath + ".properties." + fieldName + ".type"
		if typeVal.Type == ojson.String && typeVal.String() == "number" {
			if errSet := doc.Set(typePath, "integer"); errSet == nil {
				changed = true
			}
		} else if typeVal.IsArray() {
			arrayItems := typeVal.Array()
			hasNumber := false
			seenTypes := make(map[string]struct{}, len(arrayItems))
			newTypes := make([]string, 0, len(arrayItems))
			for _, item := range arrayItems {
				itemStr := item.String()
				if itemStr == "number" {
					hasNumber = true
					itemStr = "integer"
				}
				if _, seen := seenTypes[itemStr]; !seen {
					seenTypes[itemStr] = struct{}{}
					newTypes = append(newTypes, itemStr)
				}
			}
			if hasNumber {
				if errSet := doc.Set(typePath, newTypes); errSet == nil {
					changed = true
				}
			}
		}
	}
	return changed
}

// NormalizeCodexToolIntegerTypes normalizes specified tool parameter declarations
// from number to integer for Codex clients across supported tool formats (OpenAI,
// Claude input_schema, Gemini function_declarations, and namespace tools).
func NormalizeCodexToolIntegerTypes(body []byte, headers http.Header) []byte {
	if len(body) == 0 || !IsCodexUserAgent(headers) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !NormalizeCodexToolIntegerTypesDoc(doc, headers) {
		return body
	}
	return doc.Bytes()
}

// NormalizeCodexToolIntegerTypesDoc normalizes specified tool parameter declarations
// from number to integer for Codex clients directly on doc in place.
func NormalizeCodexToolIntegerTypesDoc(doc *ojson.Doc, headers http.Header) bool {
	if doc == nil || doc.Len() == 0 || !IsCodexUserAgent(headers) {
		return false
	}

	changed := false

	// 1. Process top-level tools
	toolsResult := doc.Get("tools")
	if toolsResult.IsArray() {
		if normalizeToolIntegerTypesInArrayDoc(doc, toolsResult, "tools", "") {
			changed = true
		}
	}

	// 2. Process input[].additional_tools
	inputResult := doc.Get("input")
	if inputResult.IsArray() {
		for idx, item := range inputResult.Array() {
			if item.Get("type").String() == "additional_tools" {
				addTools := item.Get("tools")
				if addTools.IsArray() {
					path := fmt.Sprintf("input.%d.tools", idx)
					if normalizeToolIntegerTypesInArrayDoc(doc, addTools, path, "") {
						changed = true
					}
				}
			}
		}
	}

	if changed {
		log.Debugf("codex: normalized target tool number types to integer")
	}
	return changed
}

func normalizeToolIntegerTypesInArrayDoc(doc *ojson.Doc, tools ojson.Result, toolsPath, namespace string) bool {
	if !tools.IsArray() {
		return false
	}
	changed := false
	idx := 0
	tools.ForEach(func(_, tool ojson.Result) bool {
		toolPath := fmt.Sprintf("%s.%d", toolsPath, idx)
		idx++
		if normalizeToolIntegerTypesInElementDoc(doc, tool, toolPath, namespace) {
			changed = true
		}
		return true
	})
	return changed
}

func normalizeToolIntegerTypesInElementDoc(doc *ojson.Doc, tool ojson.Result, toolPath, namespace string) bool {
	// Handle namespace tools
	if tool.Get("type").String() == "namespace" {
		if namespace != "" {
			return false
		}
		namespace = tool.Get("name").String()
		if namespace == "" {
			return false
		}
		nested := tool.Get("tools")
		if nested.IsArray() {
			return normalizeToolIntegerTypesInArrayDoc(doc, nested, toolPath+".tools", namespace)
		}
		return false
	}

	// Handle Gemini function declarations
	for _, declKey := range []string{"function_declarations", "functionDeclarations"} {
		decls := tool.Get(declKey)
		if decls.IsArray() {
			return normalizeToolIntegerTypesInArrayDoc(doc, decls, toolPath+"."+declKey, namespace)
		}
	}

	// Standard function/custom tool or Claude tool
	toolName := tool.Get("name").String()
	paramPath := "parameters"
	params := tool.Get("parameters")

	if !params.Exists() || !params.IsObject() {
		if fnParams := tool.Get("function.parameters"); fnParams.Exists() && fnParams.IsObject() {
			paramPath = "function.parameters"
			params = fnParams
			if toolName == "" {
				toolName = tool.Get("function.name").String()
			}
		} else if inputSchema := tool.Get("input_schema"); inputSchema.Exists() && inputSchema.IsObject() {
			paramPath = "input_schema"
			params = inputSchema
		} else if jsonSchema := tool.Get("parametersJsonSchema"); jsonSchema.Exists() && jsonSchema.IsObject() {
			paramPath = "parametersJsonSchema"
			params = jsonSchema
		} else {
			return false
		}
	}

	if namespace != "" {
		toolName = namespace + "__" + toolName
	}
	targetFields := matchCodexTargetTool(toolName)
	if len(targetFields) == 0 {
		return false
	}

	return normalizeCodexToolFieldTypesDoc(doc, toolPath+"."+paramPath, params, targetFields)
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
	doc := ojson.NewDoc(body)
	if !NormalizeCodexToolSchemasDoc(doc) {
		return body
	}
	return doc.Bytes()
}

// NormalizeCodexToolSchemasDoc inspects function tools in doc and simplifies
// pure constant union combinations and sanitizes unsupported schema patterns in place.
func NormalizeCodexToolSchemasDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	tools := doc.Get("tools")
	if !tools.IsArray() {
		return false
	}
	changed := normalizeCodexToolListDoc(doc, tools, "tools")
	if changed {
		log.Debugf("codex: normalized tool schemas to prevent upstream failure")
	}
	return changed
}

func normalizeCodexToolListDoc(doc *ojson.Doc, tools ojson.Result, toolsPath string) bool {
	if !tools.IsArray() {
		return false
	}
	changed := false
	idx := 0
	tools.ForEach(func(_, tool ojson.Result) bool {
		toolPath := fmt.Sprintf("%s.%d", toolsPath, idx)
		idx++
		if normalizeCodexToolDoc(doc, tool, toolPath) {
			changed = true
		}
		return true
	})
	return changed
}

// NormalizeSingleCodexTool normalizes a single non-namespace tool declaration.
func NormalizeSingleCodexTool(tool ojson.Result) ([]byte, bool) {
	if !tool.IsObject() {
		return nil, false
	}
	doc := ojson.NewDocString(tool.Raw)
	if !normalizeCodexToolDoc(doc, tool, "") {
		return nil, false
	}
	return doc.Bytes(), true
}

func normalizeCodexToolDoc(doc *ojson.Doc, tool ojson.Result, toolPath string) bool {
	toolType := tool.Get("type").String()
	// Handle namespace tools (e.g. multi-agent nested tools)
	if toolType == "namespace" {
		nestedPath := "tools"
		if toolPath != "" {
			nestedPath = toolPath + ".tools"
		}
		return normalizeCodexToolListDoc(doc, tool.Get("tools"), nestedPath)
	}

	if toolType != "function" && toolType != "custom" {
		return false
	}

	params := tool.Get("parameters")
	if !params.Exists() || !params.IsObject() {
		return false
	}

	paramsPath := "parameters"
	if toolPath != "" {
		paramsPath = toolPath + ".parameters"
	}
	if !normalizeCodexParametersDoc(doc, paramsPath, params) {
		return false
	}

	log.Debugf("codex: normalized schema for tool %s to avoid upstream abort", tool.Get("name").String())
	return true
}

func normalizeCodexParametersDoc(doc *ojson.Doc, paramsPath string, params ojson.Result) bool {
	changed := false

	if sanitizedParams, patternChanged := stripIncompatiblePatternsFromJSON(params.Raw); patternChanged {
		if errSet := doc.SetRaw(paramsPath, sanitizedParams); errSet == nil {
			changed = true
			params = ojson.Parse(sanitizedParams)
		}
	}

	properties := params.Get("properties")
	if properties.Exists() && properties.IsObject() {
		properties.ForEach(func(propKey, propVal ojson.Result) bool {
			propPath := paramsPath + ".properties." + escapeCodexSjsonKey(propKey.String())
			if normalizeCodexPropertySchemaDoc(doc, propPath, propVal) {
				changed = true
			}
			return true
		})
	}

	return changed
}

func stripIncompatiblePatternsFromJSON(rawStr string) (string, bool) {
	if !strings.Contains(rawStr, `\p{`) && !strings.Contains(rawStr, `\P{`) && !strings.Contains(rawStr, `\u`) &&
		!strings.Contains(rawStr, `\\0`) && !strings.Contains(rawStr, "inimum") && !strings.Contains(rawStr, "aximum") {
		return rawStr, false
	}
	var root any
	dec := json.NewDecoder(strings.NewReader(rawStr))
	dec.UseNumber()
	if errDecode := dec.Decode(&root); errDecode != nil || root == nil {
		return rawStr, false
	}
	var dummy any
	if errEOF := dec.Decode(&dummy); errEOF != io.EOF {
		return rawStr, false
	}
	if !stripIncompatiblePatterns(root) {
		return rawStr, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(root); errEncode != nil {
		return rawStr, false
	}
	return strings.TrimSpace(buf.String()), true
}

func isNumericBoundOutOfInt64(number json.Number) bool {
	raw := strings.TrimSpace(number.String())
	if raw == "" {
		return false
	}
	if !strings.ContainsAny(raw, ".eE") {
		_, errParse := strconv.ParseInt(raw, 10, 64)
		return errors.Is(errParse, strconv.ErrRange)
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
				return !strings.HasPrefix(expStr, "-") && strings.Trim(mantissa, "0.") != ""
			}
			return false
		}
		exp = parsedExp
	}

	intPart := mantissa
	fracPart := ""
	if dotIdx := strings.IndexByte(mantissa, '.'); dotIdx >= 0 {
		intPart = mantissa[:dotIdx]
		fracPart = mantissa[dotIdx+1:]
		if strings.IndexByte(fracPart, '.') >= 0 {
			return false
		}
	}
	if intPart == "" && fracPart == "" {
		return false
	}
	for i := 0; i < len(intPart); i++ {
		if intPart[i] < '0' || intPart[i] > '9' {
			return false
		}
	}
	for i := 0; i < len(fracPart); i++ {
		if fracPart[i] < '0' || fracPart[i] > '9' {
			return false
		}
	}

	allDigits := intPart + fracPart
	sig := strings.TrimLeft(allDigits, "0")
	if sig == "" {
		return false
	}
	if exp < -int64(len(raw)) {
		return false
	}
	if exp > int64(len(raw))+20 {
		return true
	}
	trimmed := strings.TrimRight(sig, "0")
	trailingZeros := int64(len(sig) - len(trimmed))
	effectiveExp := exp - int64(len(fracPart)) + trailingZeros
	if effectiveExp < 0 {
		return false
	}
	if effectiveExp > 19 || int64(len(trimmed))+effectiveExp > 19 {
		return true
	}

	intStr := trimmed + strings.Repeat("0", int(effectiveExp))
	if negative {
		intStr = "-" + intStr
	}
	_, errParse := strconv.ParseInt(intStr, 10, 64)
	return errors.Is(errParse, strconv.ErrRange)
}

func stripIncompatiblePatterns(v any) bool {
	changed := false
	switch schema := v.(type) {
	case map[string]any:
		if patternVal, ok := schema["pattern"].(string); ok && util.HasUnsupportedUnicodePropertyEscape(patternVal) {
			delete(schema, "pattern")
			changed = true
		}
		for _, boundKey := range codexNumericBoundKeywords {
			if boundVal, ok := schema[boundKey].(json.Number); ok && isNumericBoundOutOfInt64(boundVal) {
				delete(schema, boundKey)
				changed = true
			}
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

func normalizeCodexPropertySchemaDoc(doc *ojson.Doc, propPath string, prop ojson.Result) bool {
	if !prop.IsObject() {
		return false
	}

	hasOneOf := prop.Get("oneOf").Exists()
	hasAnyOf := prop.Get("anyOf").Exists()
	// If both oneOf and anyOf are present on the same property, leave untouched to preserve compound constraints
	if hasOneOf && hasAnyOf {
		return false
	}

	unionName := ""
	if hasOneOf {
		unionName = "oneOf"
	} else if hasAnyOf {
		unionName = "anyOf"
	} else {
		return false
	}

	union := prop.Get(unionName)
	if !union.IsArray() || len(union.Array()) < codexComplexUnionBranchThreshold {
		return false
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
		return false
	}

	existingEnum := prop.Get("enum")
	if existingEnum.Exists() && existingEnum.IsArray() {
		existingEnumKeys := make([]string, 0, len(existingEnum.Array()))
		for _, v := range existingEnum.Array() {
			key, ok := canonicalJSONValueKey(v)
			if !ok {
				return false
			}
			existingEnumKeys = append(existingEnumKeys, key)
		}
		// Only remove the redundant union if existing enum is proven semantically identical
		if equalCanonicalSets(existingEnumKeys, constSemanticKeys) {
			return doc.Delete(propPath+"."+unionName) == nil
		}
		return false
	}

	// Migrate the pure const union to an enum using raw JSON tokens to avoid any numeric precision loss
	rawEnumJSON := "[" + strings.Join(constRawValues, ",") + "]"
	if errEnum := doc.SetRaw(propPath+".enum", rawEnumJSON); errEnum != nil {
		return false
	}
	if errDel := doc.Delete(propPath + "." + unionName); errDel != nil {
		return false
	}
	return true
}

func isPureConstBranch(branch ojson.Result) (canonicalKey string, rawJSON string, ok bool) {
	if !branch.IsObject() {
		return "", "", false
	}
	constVal := branch.Get("const")
	if !constVal.Exists() {
		return "", "", false
	}
	// Verify no other schema validation constraints exist in this branch
	valid := true
	branch.ForEach(func(k, _ ojson.Result) bool {
		key := k.String()
		if key != "const" && key != "description" && key != "title" {
			valid = false
			return false
		}
		return true
	})
	if !valid {
		return "", "", false
	}
	canonicalKey, ok = canonicalJSONValueKey(constVal)
	if !ok {
		return "", "", false
	}
	return canonicalKey, constVal.Raw, true
}

func canonicalJSONValueKey(val ojson.Result) (string, bool) {
	switch val.Type {
	case ojson.String:
		return "s:" + val.String(), true
	case ojson.Number:
		raw := strings.TrimSpace(val.Raw)
		var r big.Rat
		if _, ok := r.SetString(raw); ok {
			return "n:" + r.RatString(), true
		}
		return "n:" + raw, true
	case ojson.True:
		return "b:true", true
	case ojson.False:
		return "b:false", true
	case ojson.Null:
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
