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
	// ExtraSourceFormats adds client protocol identifiers that should be treated
	// as part of the OpenAI Responses family next to the built-in ones.
	ExtraSourceFormats []string `yaml:"extra_source_formats"`
	// DiagnosticsLogPath overrides where probe diagnostics are appended. An
	// empty value uses the platform temporary directory.
	DiagnosticsLogPath string `yaml:"diagnostics_log_path"`
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
)

func init() {
	strictNativeModels.Store([]string(nil))
	extraSourceFormats.Store([]string(nil))
	diagnosticsPath.Store("")
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
	return nil
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
	tools, ok := root["tools"].([]any)
	if !ok || len(tools) == 0 {
		return body, false, nil
	}
	changed := strictCompleteToolSearchSchemas(tools)
	if inlineRecursiveSchemaRefsForTools(tools) {
		changed = true
	}
	if filtered, stripped := stripUnsupportedCustomTools(tools); stripped {
		root["tools"] = filtered
		changed = true
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
		toolType, _ := tool["type"].(string)
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(toolType)), "tool_search") {
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
			if value, exists := property["type"]; exists {
				if widened, okWiden := widenTypeWithNull(value); okWiden {
					property["type"] = widened
				}
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

// inlineRecursiveSchemaRefsForTools expands local `$defs` / `definitions`
// references for function tools, chat-shaped function tools and namespace
// children. Re-entrant references collapse into an opaque object so the
// emitted schema stays finite.
func inlineRecursiveSchemaRefsForTools(tools []any) bool {
	changed := false
	for _, rawTool := range tools {
		if inlineToolSchemaRefs(rawTool) {
			changed = true
		}
	}
	return changed
}

func inlineToolSchemaRefs(rawTool any) bool {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return false
	}
	changed := false
	if function, okFunction := tool["function"].(map[string]any); okFunction {
		if parameters, exists := function["parameters"]; exists {
			if resolved, okResolve := inlineRecursiveSchemaRefs(parameters); okResolve {
				function["parameters"] = resolved
				changed = true
			}
		}
	} else if parameters, exists := tool["parameters"]; exists {
		if resolved, okResolve := inlineRecursiveSchemaRefs(parameters); okResolve {
			tool["parameters"] = resolved
			changed = true
		}
	}
	if toolType, _ := tool["type"].(string); strings.EqualFold(strings.TrimSpace(toolType), namespaceToolType) {
		if children, okChildren := tool["tools"].([]any); okChildren {
			for _, child := range children {
				if inlineToolSchemaRefs(child) {
					changed = true
				}
			}
		}
	}
	return changed
}

func inlineRecursiveSchemaRefs(schema any) (any, bool) {
	root, ok := schema.(map[string]any)
	if !ok || !schemaContainsLocalRef(root) {
		return schema, false
	}
	return inlineLocalRefsNonRecursive(root, root, nil), true
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

func inlineLocalRefsNonRecursive(node any, root map[string]any, stack []string) any {
	switch typed := node.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, inlineLocalRefsNonRecursive(item, root, stack))
		}
		return out
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			if match := localRefPattern.FindStringSubmatch(ref); match != nil {
				rest := copyWithoutKey(typed, "$ref")
				target := localRefTarget(root, match[1], match[2])
				if target == nil {
					return inlineLocalRefsNonRecursive(rest, root, stack)
				}
				if containsString(stack, ref) {
					elided := map[string]any{"type": "object"}
					for key, value := range rest {
						elided[key] = value
					}
					elided["description"] = elidedDescription(rest["description"], match[2])
					return elided
				}
				nextStack := make([]string, 0, len(stack)+1)
				nextStack = append(nextStack, stack...)
				nextStack = append(nextStack, ref)
				expanded, _ := inlineLocalRefsNonRecursive(target, root, nextStack).(map[string]any)
				resolvedRest, _ := inlineLocalRefsNonRecursive(rest, root, stack).(map[string]any)
				merged := make(map[string]any, len(expanded)+len(resolvedRest))
				for key, value := range expanded {
					merged[key] = value
				}
				// Sibling keys next to `$ref` win over the expanded definition.
				for key, value := range resolvedRest {
					merged[key] = value
				}
				return merged
			}
		}
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			if key == "$defs" || key == "definitions" {
				continue
			}
			out[key] = inlineLocalRefsNonRecursive(value, root, stack)
		}
		return out
	default:
		return node
	}
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

func elidedDescription(existing any, name string) string {
	prefix := ""
	if text, ok := existing.(string); ok && strings.TrimSpace(text) != "" {
		prefix = text + " "
	}
	return prefix + "(nested " + name + "; recursion elided)"
}
