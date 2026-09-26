package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

// Some native Responses upstreams validate Codex tool declarations more
// strictly than OpenAI does. OpenCode Go rejects the client-executed
// `tool_search` built-in unless every property is listed in `required`, and it
// rejects tool schemas that reference themselves through local `$defs`.
// Both workarounds below only run for models opted in through the plugin
// configuration, because OpenAI accepts the declarations exactly as Codex
// sends them.

// pluginConfig is the plugin-owned YAML configuration.
type pluginConfig struct {
	// StrictResponsesModels lists native Responses models (exact name or a "*"
	// wildcard) that need the strict-schema workarounds.
	StrictResponsesModels []string `yaml:"strict_responses_models"`
	// BridgeModels lists exact client model IDs whose Codex model-list entries
	// may advertise the client-executed search bridge.
	BridgeModels []string `yaml:"bridge_models"`
	// BridgeNativeModels lists exact models whose native Responses route uses
	// the ordinary-function search bridge.
	BridgeNativeModels []string `yaml:"bridge_native_models"`
	// ExtraSourceFormats adds client protocol identifiers that should be treated
	// as part of the OpenAI Responses family next to the built-in ones.
	ExtraSourceFormats []string `yaml:"extra_source_formats"`
	// DiagnosticsLogPath overrides where probe diagnostics are appended. An
	// empty value uses the platform temporary directory.
	DiagnosticsLogPath string `yaml:"diagnostics_log_path"`
	// MaxActiveToolBytes bounds serialized active tool declarations.
	MaxActiveToolBytes int `yaml:"max_active_tool_bytes"`
	// MaxRequestStates bounds concurrently retained request states.
	MaxRequestStates int `yaml:"max_request_states"`
	// MaxStateBytes bounds the sum of retained request-state payloads.
	MaxStateBytes int `yaml:"max_state_bytes"`
	// MaxSchemaExpansionBytes bounds one strict schema expansion.
	MaxSchemaExpansionBytes int `yaml:"max_schema_expansion_bytes"`
	// MaxSchemaExpansionNodes bounds expanded schema nodes.
	MaxSchemaExpansionNodes int `yaml:"max_schema_expansion_nodes"`
	// MaxSchemaDepth bounds schema and namespace recursion.
	MaxSchemaDepth int `yaml:"max_schema_depth"`
	// StripCustomTools explicitly enables the lossy custom-tool compatibility
	// workaround. It is off by default.
	StripCustomTools bool `yaml:"strip_custom_tools"`
}

// pluginConfigRequest is the host envelope delivered on register and
// reconfigure calls. The plugin-owned YAML sits in config_yaml.
type pluginConfigRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

var (
	strictNativeModels atomic.Value
	extraSourceFormats atomic.Value
	diagnosticsPath    atomic.Value
	bridgeModels       atomic.Value
	bridgeNativeModels atomic.Value
	toolBudget         atomic.Value
	stateBudget        atomic.Value
	schemaBudget       atomic.Value
	stripCustomTools   atomic.Value
)

func init() {
	strictNativeModels.Store([]string(nil))
	extraSourceFormats.Store([]string(nil))
	diagnosticsPath.Store("")
	bridgeModels.Store([]string(nil))
	bridgeNativeModels.Store([]string(nil))
	toolBudget.Store(pluginBudget{MaxToolBytes: 262144})
	stateBudget.Store(pluginStateBudget{MaxStates: 512, MaxBytes: 33554432})
	schemaBudget.Store(pluginSchemaBudget{MaxBytes: 65536, MaxNodes: 10000, MaxDepth: 64})
	stripCustomTools.Store(false)
}

// configurePlugin decodes the host envelope and applies the plugin config.
func configurePlugin(raw []byte) error {
	request := pluginConfigRequest{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return fmt.Errorf("decode plugin configure request: %w", errUnmarshal)
		}
	}
	return applyPluginConfig(request.ConfigYAML)
}

// applyPluginConfig decodes the plugin configuration and records the models that
// need strict-schema workarounds plus the optional protocol and diagnostics
// overrides.
func applyPluginConfig(configYAML []byte) error {
	config := pluginConfig{}
	if len(bytes.TrimSpace(configYAML)) > 0 {
		if errUnmarshal := yaml.Unmarshal(configYAML, &config); errUnmarshal != nil {
			return fmt.Errorf("decode plugin config: %w", errUnmarshal)
		}
	}
	strictNativeModels.Store(trimmedList(config.StrictResponsesModels))
	extraSourceFormats.Store(trimmedList(config.ExtraSourceFormats))
	diagnosticsPath.Store(strings.TrimSpace(config.DiagnosticsLogPath))
	bridgeModels.Store(trimmedList(config.BridgeModels))
	bridgeNativeModels.Store(trimmedList(config.BridgeNativeModels))
	toolBudget.Store(pluginBudget{MaxToolBytes: positiveOrDefault(config.MaxActiveToolBytes, 262144)})
	stateBudget.Store(pluginStateBudget{
		MaxStates: positiveOrDefault(config.MaxRequestStates, 512),
		MaxBytes:  positiveOrDefault(config.MaxStateBytes, 33554432),
	})
	schemaBudget.Store(pluginSchemaBudget{
		MaxBytes: positiveOrDefault(config.MaxSchemaExpansionBytes, 65536),
		MaxNodes: positiveOrDefault(config.MaxSchemaExpansionNodes, 10000),
		MaxDepth: positiveOrDefault(config.MaxSchemaDepth, 64),
	})
	stripCustomTools.Store(config.StripCustomTools)
	return nil
}

type pluginBudget struct {
	MaxToolBytes int
}

type pluginStateBudget struct {
	MaxStates int
	MaxBytes  int
}

type pluginSchemaBudget struct {
	MaxBytes int
	MaxNodes int
	MaxDepth int
}

func positiveOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func configuredToolBudget() pluginBudget {
	budget, _ := toolBudget.Load().(pluginBudget)
	return budget
}

func configuredStateBudget() pluginStateBudget {
	budget, _ := stateBudget.Load().(pluginStateBudget)
	return budget
}

func configuredSchemaBudget() pluginSchemaBudget {
	budget, _ := schemaBudget.Load().(pluginSchemaBudget)
	return budget
}

func exactModelMatches(values []string, candidates ...string) bool {
	for _, value := range values {
		expected := strings.ToLower(strings.TrimSpace(value))
		if expected == "" {
			continue
		}
		for _, candidate := range candidates {
			if strings.EqualFold(strings.TrimSpace(candidate), expected) {
				return true
			}
		}
	}
	return false
}

func bridgeModelMatches(candidates ...string) bool {
	values, _ := bridgeModels.Load().([]string)
	return exactModelMatches(values, candidates...)
}

func bridgeNativeModelMatches(candidates ...string) bool {
	values, _ := bridgeNativeModels.Load().([]string)
	return exactModelMatches(values, candidates...)
}

func shouldStripCustomTools() bool {
	enabled, _ := stripCustomTools.Load().(bool)
	return enabled
}

// trimmedList drops blank entries and surrounding whitespace.
func trimmedList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// configuredExtraSourceFormat reports whether a client protocol was explicitly
// opted into the Responses family by configuration.
func configuredExtraSourceFormat(format string) bool {
	values, _ := extraSourceFormats.Load().([]string)
	if len(values) == 0 {
		return false
	}
	trimmed := strings.ToLower(strings.TrimSpace(format))
	if trimmed == "" {
		return false
	}
	for _, value := range values {
		if strings.ToLower(value) == trimmed {
			return true
		}
	}
	return false
}

// diagnosticsLogPath returns the configured probe log path, or the platform
// temporary directory when none is configured.
func diagnosticsLogPath() string {
	if configured, _ := diagnosticsPath.Load().(string); configured != "" {
		return configured
	}
	return filepath.Join(os.TempDir(), "codex-tool-search-shim-structure.jsonl")
}

// strictNativeModelMatches reports whether any candidate matches a configured
// entry. Matching is case-insensitive; "*" is a wildcard, so `muse-spark-*`
// matches a prefix and `*muse-spark*` matches a substring.
func strictNativeModelMatches(candidates ...string) bool {
	models, _ := strictNativeModels.Load().([]string)
	if len(models) == 0 {
		return false
	}
	for _, model := range models {
		pattern := strings.ToLower(model)
		if pattern == "" {
			continue
		}
		for _, candidate := range candidates {
			value := strings.ToLower(strings.TrimSpace(candidate))
			if value == "" {
				continue
			}
			if matchModelPattern(pattern, value) {
				return true
			}
		}
	}
	return false
}

func matchModelPattern(pattern, value string) bool {
	if !strings.Contains(pattern, "*") {
		return value == pattern
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		if index == 0 {
			if !strings.HasPrefix(value, part) {
				return false
			}
			position = len(part)
			continue
		}
		offset := strings.Index(value[position:], part)
		if offset < 0 {
			return false
		}
		position += offset + len(part)
	}
	if last := parts[len(parts)-1]; last != "" {
		return strings.HasSuffix(value, last)
	}
	return true
}

// normalizeStrictNativeBody strict-completes `tool_search` declarations and
// inlines local schema references. It reports false when nothing changed.
func normalizeStrictNativeBody(body []byte) ([]byte, bool, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return body, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]any
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return body, false, nil
	}
	changed, errNormalize := normalizeStrictToolArrays(root)
	if errNormalize != nil {
		return body, false, errNormalize
	}
	if !changed {
		return body, false, nil
	}
	out, errMarshal := json.Marshal(root)
	if errMarshal != nil {
		return body, false, fmt.Errorf("marshal strict-normalized body: %w", errMarshal)
	}
	return out, true, nil
}

func normalizeStrictToolArrays(root map[string]any) (bool, error) {
	changed := false
	if rawTools, exists := root["tools"]; exists {
		tools, ok := rawTools.([]any)
		if !ok {
			return false, fmt.Errorf("strict tools field is not an array")
		}
		normalized, toolChanged, errNormalize := normalizeStrictToolArray(tools)
		if errNormalize != nil {
			return false, errNormalize
		}
		if toolChanged {
			root["tools"] = normalized
			changed = true
		}
	}
	input, ok := root["input"].([]any)
	if !ok {
		return changed, nil
	}
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem || stringField(item, "type") != "additional_tools" {
			continue
		}
		rawTools, exists := item["tools"]
		if !exists {
			continue
		}
		tools, okTools := rawTools.([]any)
		if !okTools {
			return false, fmt.Errorf("strict additional_tools field is not an array")
		}
		normalized, toolChanged, errNormalize := normalizeStrictToolArray(tools)
		if errNormalize != nil {
			return false, errNormalize
		}
		if toolChanged {
			item["tools"] = normalized
			changed = true
		}
	}
	return changed, nil
}

func normalizeStrictToolArray(tools []any) ([]any, bool, error) {
	if errDepth := validateToolArrayDepth(tools, 1); errDepth != nil {
		return tools, false, errDepth
	}
	changed := strictCompleteToolSearchSchemas(tools)
	if errInline := inlineToolSchemaRefsForTools(tools); errInline != nil {
		return tools, false, errInline
	}
	if shouldStripCustomTools() {
		if filtered, stripped := stripUnsupportedCustomTools(tools); stripped {
			tools = filtered
			changed = true
		}
	}
	return tools, changed, nil
}

func validateToolArrayDepth(tools []any, depth int) error {
	if depth > configuredSchemaBudget().MaxDepth {
		return strictSchemaError("schema_depth_exceeded")
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), namespaceToolType) {
			children, okChildren := tool["tools"].([]any)
			if !okChildren {
				continue
			}
			if errChild := validateToolArrayDepth(children, depth+1); errChild != nil {
				return errChild
			}
		}
	}
	return nil
}

// strictCompleteToolSearchSchemas rewrites client-executed `tool_search`
// declarations so every declared property is also required. Properties that
// were optional widen to accept null, which keeps them optional for the model
// while satisfying strict-mode validation. `type` and `execution` stay
// untouched so the client still receives a locally resolved `tool_search_call`.
func strictCompleteToolSearchSchemas(tools []any) bool {
	changed := false
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), namespaceToolType) {
			if children, okChildren := tool["tools"].([]any); okChildren {
				if strictCompleteToolSearchSchemas(children) {
					changed = true
				}
			}
			continue
		}
		toolType, _ := tool["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(toolType), "tool_search") || isServerExecutedItem(tool) {
			continue
		}
		schema, ok := tool["parameters"].(map[string]any)
		if !ok {
			continue
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok || len(properties) == 0 {
			continue
		}
		required := stringSet(schema["required"])
		keys := make([]string, 0, len(properties))
		for key := range properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		completionNeeded := false
		for _, key := range keys {
			if !required[key] {
				completionNeeded = true
				break
			}
		}
		if !completionNeeded && strictAdditionalPropertiesFalse(schema) {
			continue
		}
		for _, key := range keys {
			if required[key] {
				continue
			}
			property, ok := properties[key].(map[string]any)
			if !ok {
				continue
			}
			if _, alreadyNullable := nullableSchema(property); !alreadyNullable {
				original := make(map[string]any, len(property)+1)
				for key, value := range property {
					original[key] = value
				}
				replacement := map[string]any{"anyOf": []any{original, map[string]any{"type": "null"}}}
				if description, exists := property["description"]; exists {
					replacement["description"] = description
				}
				properties[key] = replacement
			}
		}
		ordered := make([]any, 0, len(keys))
		for _, key := range keys {
			ordered = append(ordered, key)
		}
		schema["required"] = ordered
		schema["additionalProperties"] = false
		changed = true
	}
	return changed
}

func nullableSchema(schema map[string]any) (any, bool) {
	entries, ok := schema["anyOf"].([]any)
	if !ok {
		return nil, false
	}
	for _, entry := range entries {
		if candidate, okCandidate := entry.(map[string]any); okCandidate && stringField(candidate, "type") == "null" {
			return entry, true
		}
	}
	return nil, false
}

// stripUnsupportedCustomTools removes freeform `custom` tools, which some
// native Responses upstreams reject outright. Codex only learns about
// apply_patch from this declaration, so dropping it degrades to shell edits
// instead of breaking every request.
func stripUnsupportedCustomTools(tools []any) ([]any, bool) {
	out := make([]any, 0, len(tools))
	changed := false
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			out = append(out, rawTool)
			continue
		}
		if toolType, _ := tool["type"].(string); strings.EqualFold(strings.TrimSpace(toolType), "custom") {
			changed = true
			continue
		}
		if children, okChildren := tool["tools"].([]any); okChildren {
			if filtered, childChanged := stripUnsupportedCustomTools(children); childChanged {
				tool["tools"] = filtered
				changed = true
			}
		}
		out = append(out, rawTool)
	}
	return out, changed
}

func stringSet(value any) map[string]bool {
	out := map[string]bool{}
	items, ok := value.([]any)
	if !ok {
		return out
	}
	for _, item := range items {
		if name, okName := item.(string); okName {
			out[name] = true
		}
	}
	return out
}

func strictAdditionalPropertiesFalse(schema map[string]any) bool {
	value, exists := schema["additionalProperties"]
	if !exists {
		return false
	}
	flag, ok := value.(bool)
	return ok && !flag
}

// widenTypeWithNull returns a type that also accepts null, which is OpenAI's
// documented way to spell an optional strict-mode field.
func widenTypeWithNull(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || trimmed == "null" {
			return nil, false
		}
		return []any{typed, "null"}, true
	case []any:
		for _, entry := range typed {
			if name, ok := entry.(string); ok && strings.TrimSpace(name) == "null" {
				return nil, false
			}
		}
		widened := make([]any, 0, len(typed)+1)
		widened = append(widened, typed...)
		widened = append(widened, "null")
		return widened, true
	default:
		return nil, false
	}
}

var localRefPattern = regexp.MustCompile(`^#/(\$defs|definitions)/([^/]+)$`)

// inlineToolSchemaRefsForTools applies strict reference policy to every tool
// array without silently deleting unresolved or recursive constraints.
func inlineToolSchemaRefsForTools(tools []any) error {
	for _, rawTool := range tools {
		if _, errInline := inlineToolSchemaRefWithPolicy(rawTool, 1); errInline != nil {
			return errInline
		}
	}
	return nil
}

func inlineRecursiveSchemaRefsForTools(tools []any) bool {
	return inlineToolSchemaRefsForTools(tools) == nil && inlineAnyToolSchemaRefs(tools)
}

func inlineAnyToolSchemaRefs(tools []any) bool {
	changed := false
	for _, rawTool := range tools {
		if inlineToolSchemaRefs(rawTool) {
			changed = true
		}
	}
	return changed
}

func inlineToolSchemaRefs(rawTool any) bool {
	resolved, errInline := inlineToolSchemaRefWithPolicy(rawTool, 1)
	return errInline == nil && resolved
}

func inlineToolSchemaRefWithPolicy(rawTool any, depth int) (bool, error) {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return false, nil
	}
	if depth > configuredSchemaBudget().MaxDepth {
		return false, strictSchemaError("schema_depth_exceeded")
	}
	changed := false
	if function, okFunction := tool["function"].(map[string]any); okFunction {
		if parameters, exists := function["parameters"]; exists {
			resolved, didChange, errInline := inlineRecursiveSchemaRefsWithPolicy(parameters, depth)
			if errInline != nil {
				return false, errInline
			}
			if didChange {
				function["parameters"] = resolved
				changed = true
			}
		}
	} else if parameters, exists := tool["parameters"]; exists {
		resolved, didChange, errInline := inlineRecursiveSchemaRefsWithPolicy(parameters, depth)
		if errInline != nil {
			return false, errInline
		}
		if didChange {
			tool["parameters"] = resolved
			changed = true
		}
	}
	if toolType, _ := tool["type"].(string); strings.EqualFold(strings.TrimSpace(toolType), namespaceToolType) {
		if children, okChildren := tool["tools"].([]any); okChildren {
			for _, child := range children {
				childChanged, errInline := inlineToolSchemaRefWithPolicy(child, depth+1)
				if errInline != nil {
					return false, errInline
				}
				changed = changed || childChanged
			}
		}
	}
	return changed, nil
}

func inlineRecursiveSchemaRefs(schema any) (any, bool) {
	resolved, changed, errInline := inlineRecursiveSchemaRefsWithPolicy(schema, 1)
	if errInline != nil {
		return schema, false
	}
	return resolved, changed
}

func inlineRecursiveSchemaRefsWithPolicy(schema any, depth int) (any, bool, error) {
	root, ok := schema.(map[string]any)
	if !ok || !schemaContainsLocalRef(root) {
		return schema, false, nil
	}
	budget := configuredSchemaBudget()
	state := refExpansionState{budget: budget}
	resolved, errInline := inlineLocalRefs(root, root, nil, depth, &state)
	if errInline != nil {
		return schema, false, errInline
	}
	encoded, errMarshal := json.Marshal(resolved)
	if errMarshal != nil {
		return schema, false, strictSchemaError("schema_encoding_failed")
	}
	if len(encoded) > budget.MaxBytes {
		return schema, false, strictSchemaError("schema_bytes_exceeded")
	}
	return resolved, true, nil
}

type refExpansionState struct {
	budget pluginSchemaBudget
	nodes  int
}

func (state *refExpansionState) consume() error {
	state.nodes++
	if state.nodes > state.budget.MaxNodes {
		return strictSchemaError("schema_nodes_exceeded")
	}
	return nil
}

func schemaContainsLocalRef(node any) bool {
	switch typed := node.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok && localRefPattern.MatchString(ref) {
			return true
		}
		for _, value := range typed {
			if schemaContainsLocalRef(value) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if schemaContainsLocalRef(item) {
				return true
			}
		}
	}
	return false
}

func inlineLocalRefs(node any, root map[string]any, stack []string, depth int, state *refExpansionState) (any, error) {
	if errConsume := state.consume(); errConsume != nil {
		return nil, errConsume
	}
	if depth > state.budget.MaxDepth {
		return nil, strictSchemaError("schema_depth_exceeded")
	}
	switch typed := node.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			resolved, errInline := inlineLocalRefs(item, root, stack, depth+1, state)
			if errInline != nil {
				return nil, errInline
			}
			out = append(out, resolved)
		}
		return out, nil
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			match := localRefPattern.FindStringSubmatch(ref)
			if match == nil {
				return nil, strictSchemaError("unsupported_schema_reference")
			}
			if containsString(stack, ref) {
				return nil, strictSchemaError("recursive_schema_reference")
			}
			target := localRefTarget(root, match[1], decodeJSONPointerToken(match[2]))
			if target == nil {
				return nil, strictSchemaError("missing_schema_reference")
			}
			rest := copyWithoutKey(typed, "$ref")
			expanded, errExpand := inlineLocalRefs(target, root, append(append([]string(nil), stack...), ref), depth+1, state)
			if errExpand != nil {
				return nil, errExpand
			}
			resolvedRest, errRest := inlineLocalRefs(rest, root, stack, depth+1, state)
			if errRest != nil {
				return nil, errRest
			}
			expandedMap, okExpanded := expanded.(map[string]any)
			restMap, okRest := resolvedRest.(map[string]any)
			if !okExpanded || !okRest {
				return nil, strictSchemaError("invalid_schema_reference")
			}
			merged := make(map[string]any, len(expandedMap)+len(restMap))
			for key, value := range expandedMap {
				merged[key] = value
			}
			for key, value := range restMap {
				merged[key] = value
			}
			return merged, nil
		}
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			if key == "$defs" || key == "definitions" {
				continue
			}
			resolved, errInline := inlineLocalRefs(value, root, stack, depth+1, state)
			if errInline != nil {
				return nil, errInline
			}
			out[key] = resolved
		}
		return out, nil
	default:
		return node, nil
	}
}

func strictSchemaError(code string) error {
	return fmt.Errorf("strict schema policy: %s", code)
}

func decodeJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	return strings.ReplaceAll(token, "~0", "~")
}

func localRefTarget(root map[string]any, container, name string) map[string]any {
	definitions, ok := root[container].(map[string]any)
	if !ok {
		return nil
	}
	target, _ := definitions[name].(map[string]any)
	return target
}

func copyWithoutKey(source map[string]any, key string) map[string]any {
	out := make(map[string]any, len(source))
	for existingKey, value := range source {
		if existingKey == key {
			continue
		}
		out[existingKey] = value
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
