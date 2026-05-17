package helps

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ApplyPayloadConfigWithRoot behaves like applyPayloadConfig but treats all parameter
// paths as relative to the provided root path (for example, "request" for Gemini CLI)
// and restricts matches to the given protocol when supplied. Defaults are checked
// against the original payload when provided. requestedModel carries the client-visible
// model name before alias resolution so payload rules can target aliases precisely.
// requestPath is the inbound HTTP request path (when available) used for endpoint-scoped gates.
func ApplyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte, requestedModel string, requestPath string) []byte {
	return ApplyPayloadConfigWithRequest(cfg, model, protocol, "", root, payload, original, requestedModel, requestPath, nil)
}

// ApplyPayloadConfigWithRequest applies payload config using source protocol and request header gates.
func ApplyPayloadConfigWithRequest(cfg *config.Config, model, protocol, formProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}
	out := payload

	// Apply disable-image-generation filtering before payload rules so config payload
	// overrides can explicitly re-enable image_generation when desired.
	if cfg.DisableImageGeneration != config.DisableImageGenerationOff {
		if cfg.DisableImageGeneration != config.DisableImageGenerationChat || !isImagesEndpointRequestPath(requestPath) {
			out = removeToolTypeFromPayloadWithRoot(out, root, "image_generation")
			out = removeToolChoiceFromPayloadWithRoot(out, root, "image_generation")
		}
	}

	rules := cfg.Payload
	hasPayloadRules := len(rules.Default) != 0 || len(rules.DefaultRaw) != 0 || len(rules.Override) != 0 || len(rules.OverrideRaw) != 0 || len(rules.Filter) != 0
	if hasPayloadRules {
		model = strings.TrimSpace(model)
		requestedModel = strings.TrimSpace(requestedModel)
		if model != "" || requestedModel != "" {
			candidates := payloadModelCandidates(model, requestedModel)
			source := original
			if len(source) == 0 {
				source = payload
			}
			appliedDefaults := make(map[string]struct{})
			// Apply default rules: first write wins per field across all matching rules.
			for i := range rules.Default {
				rule := &rules.Default[i]
				if !payloadModelRulesMatch(rule.Models, protocol, formProtocol, headers, out, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePaths(out, fullPath) {
						if gjson.GetBytes(source, resolvedPath).Exists() {
							continue
						}
						if _, ok := appliedDefaults[resolvedPath]; ok {
							continue
						}
						updated, errSet := sjson.SetBytes(out, resolvedPath, value)
						if errSet != nil {
							continue
						}
						out = updated
						appliedDefaults[resolvedPath] = struct{}{}
					}
				}
			}
			// Apply default raw rules: first write wins per field across all matching rules.
			for i := range rules.DefaultRaw {
				rule := &rules.DefaultRaw[i]
				if !payloadModelRulesMatch(rule.Models, protocol, formProtocol, headers, out, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePaths(out, fullPath) {
						if gjson.GetBytes(source, resolvedPath).Exists() {
							continue
						}
						if _, ok := appliedDefaults[resolvedPath]; ok {
							continue
						}
						rawValue, ok := payloadRawValue(value)
						if !ok {
							continue
						}
						updated, errSet := sjson.SetRawBytes(out, resolvedPath, rawValue)
						if errSet != nil {
							continue
						}
						out = updated
						appliedDefaults[resolvedPath] = struct{}{}
					}
				}
			}
			// Apply override rules: last write wins per field across all matching rules.
			for i := range rules.Override {
				rule := &rules.Override[i]
				if !payloadModelRulesMatch(rule.Models, protocol, formProtocol, headers, out, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePaths(out, fullPath) {
						updated, errSet := sjson.SetBytes(out, resolvedPath, value)
						if errSet != nil {
							continue
						}
						out = updated
					}
				}
			}
			// Apply override raw rules: last write wins per field across all matching rules.
			for i := range rules.OverrideRaw {
				rule := &rules.OverrideRaw[i]
				if !payloadModelRulesMatch(rule.Models, protocol, formProtocol, headers, out, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					rawValue, ok := payloadRawValue(value)
					if !ok {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePaths(out, fullPath) {
						updated, errSet := sjson.SetRawBytes(out, resolvedPath, rawValue)
						if errSet != nil {
							continue
						}
						out = updated
					}
				}
			}
			// Apply filter rules: remove matching paths from payload.
			for i := range rules.Filter {
				rule := &rules.Filter[i]
				if !payloadModelRulesMatch(rule.Models, protocol, formProtocol, headers, out, root, candidates) {
					continue
				}
				for _, path := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					resolvedPaths := resolvePayloadRulePaths(out, fullPath)
					for i := len(resolvedPaths) - 1; i >= 0; i-- {
						resolvedPath := resolvedPaths[i]
						updated, errDel := sjson.DeleteBytes(out, resolvedPath)
						if errDel != nil {
							continue
						}
						out = updated
					}
				}
			}
		}
	}
	return out
}

func isImagesEndpointRequestPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if path == "/v1/images/generations" || path == "/v1/images/edits" {
		return true
	}
	// Be tolerant of prefix routers that may report a longer matched route.
	if strings.HasSuffix(path, "/v1/images/generations") || strings.HasSuffix(path, "/v1/images/edits") {
		return true
	}
	if strings.HasSuffix(path, "/images/generations") || strings.HasSuffix(path, "/images/edits") {
		return true
	}
	return false
}

func payloadModelRulesMatch(rules []config.PayloadModelRule, protocol string, formProtocol string, headers http.Header, payload []byte, root string, models []string) bool {
	if len(rules) == 0 || len(models) == 0 {
		return false
	}
	for _, model := range models {
		for _, entry := range rules {
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				continue
			}
			if ep := strings.TrimSpace(entry.Protocol); ep != "" && protocol != "" && !strings.EqualFold(ep, protocol) {
				continue
			}
			if !payloadFormProtocolMatches(entry.FormProtocol, formProtocol) {
				continue
			}
			if !payloadHeadersMatch(headers, entry.Headers) {
				continue
			}
			if !matchModelPattern(name, model) {
				continue
			}
			if payloadModelRuleConditionsMatch(payload, root, entry) {
				return true
			}
		}
	}
	return false
}

func payloadModelRuleConditionsMatch(payload []byte, root string, rule config.PayloadModelRule) bool {
	if !payloadMatchConditionsMatch(payload, root, rule.Match) {
		return false
	}
	if !payloadNotMatchConditionsMatch(payload, root, rule.NotMatch) {
		return false
	}
	if !payloadExistConditionsMatch(payload, root, rule.Exist) {
		return false
	}
	if !payloadNotExistConditionsMatch(payload, root, rule.NotExist) {
		return false
	}
	return true
}

func payloadMatchConditionsMatch(payload []byte, root string, conditions []map[string]any) bool {
	for _, condition := range conditions {
		for path, value := range condition {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if !payloadPathMatchesValue(payload, buildPayloadPath(root, path), value) {
				return false
			}
		}
	}
	return true
}

func payloadNotMatchConditionsMatch(payload []byte, root string, conditions []map[string]any) bool {
	for _, condition := range conditions {
		for path, value := range condition {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if payloadPathMatchesValue(payload, buildPayloadPath(root, path), value) {
				return false
			}
		}
	}
	return true
}

func payloadExistConditionsMatch(payload []byte, root string, paths []string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if !payloadPathExists(payload, buildPayloadPath(root, path)) {
			return false
		}
	}
	return true
}

func payloadNotExistConditionsMatch(payload []byte, root string, paths []string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if payloadPathExists(payload, buildPayloadPath(root, path)) {
			return false
		}
	}
	return true
}

func payloadPathMatchesValue(payload []byte, path string, value any) bool {
	for _, resolvedPath := range resolvePayloadRulePaths(payload, path) {
		result := gjson.GetBytes(payload, resolvedPath)
		if !result.Exists() {
			continue
		}
		if payloadResultEquals(result, value) {
			return true
		}
	}
	return false
}

func payloadPathExists(payload []byte, path string) bool {
	for _, resolvedPath := range resolvePayloadRulePaths(payload, path) {
		result := gjson.GetBytes(payload, resolvedPath)
		if result.Exists() && result.Type != gjson.Null {
			return true
		}
	}
	return false
}

func payloadResultEquals(result gjson.Result, value any) bool {
	actual, ok := normalizedPayloadResult(result)
	if !ok {
		return false
	}
	expected, ok := normalizedPayloadValue(value)
	if !ok {
		return false
	}
	return reflect.DeepEqual(actual, expected)
}

func normalizedPayloadResult(result gjson.Result) (any, bool) {
	if !result.Exists() {
		return nil, false
	}
	raw := strings.TrimSpace(result.Raw)
	if raw == "" {
		encoded, errMarshal := json.Marshal(result.Value())
		if errMarshal != nil {
			return nil, false
		}
		raw = string(encoded)
	}
	return normalizedPayloadJSON([]byte(raw))
}

func normalizedPayloadValue(value any) (any, bool) {
	encoded, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, false
	}
	return normalizedPayloadJSON(encoded)
}

func normalizedPayloadJSON(data []byte) (any, bool) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, false
	}
	var out any
	if errUnmarshal := json.Unmarshal(data, &out); errUnmarshal != nil {
		return nil, false
	}
	return out, true
}

func payloadFormProtocolMatches(pattern, formProtocol string) bool {
	pattern = normalizePayloadFormProtocol(pattern)
	if pattern == "" {
		return true
	}
	formProtocol = normalizePayloadFormProtocol(formProtocol)
	if formProtocol == "" {
		return false
	}
	return strings.EqualFold(pattern, formProtocol)
}

func normalizePayloadFormProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "openai-response", "openai-responses", "response":
		return "responses"
	case "gemini-cli":
		return "gemini"
	default:
		return protocol
	}
}

func payloadHeadersMatch(headers http.Header, rules map[string]string) bool {
	if len(rules) == 0 {
		return true
	}
	for key, pattern := range rules {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values := payloadHeaderValues(headers, key)
		if len(values) == 0 {
			return false
		}
		matched := false
		for _, value := range values {
			if matchModelPattern(pattern, value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func payloadHeaderValues(headers http.Header, key string) []string {
	if headers == nil {
		return nil
	}
	var values []string
	for headerKey, headerValues := range headers {
		if strings.EqualFold(headerKey, key) {
			values = append(values, headerValues...)
		}
	}
	return values
}

func payloadModelCandidates(model, requestedModel string) []string {
	model = strings.TrimSpace(model)
	requestedModel = strings.TrimSpace(requestedModel)
	if model == "" && requestedModel == "" {
		return nil
	}
	candidates := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	addCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, value)
	}
	if model != "" {
		addCandidate(model)
	}
	if requestedModel != "" {
		parsed := thinking.ParseSuffix(requestedModel)
		base := strings.TrimSpace(parsed.ModelName)
		if base != "" {
			addCandidate(base)
		}
		if parsed.HasSuffix {
			addCandidate(requestedModel)
		}
	}
	return candidates
}

// buildPayloadPath combines an optional root path with a relative parameter path.
// When root is empty, the parameter path is used as-is. When root is non-empty,
// the parameter path is treated as relative to root.
func buildPayloadPath(root, path string) string {
	r := strings.TrimSpace(root)
	p := strings.TrimSpace(path)
	if r == "" {
		return p
	}
	if p == "" {
		return r
	}
	if strings.HasPrefix(p, ".") {
		p = p[1:]
	}
	return r + "." + p
}

func resolvePayloadRulePaths(payload []byte, path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if !strings.Contains(path, "#(") {
		return []string{path}
	}
	parts := splitPayloadRulePath(path)
	if len(parts) == 0 {
		return nil
	}
	paths := []string{""}
	for _, part := range parts {
		query, allMatches, ok := parsePayloadQueryPathPart(part)
		if !ok {
			for i := range paths {
				paths[i] = appendPayloadPathPart(paths[i], part)
			}
			continue
		}
		nextPaths := make([]string, 0, len(paths))
		for _, basePath := range paths {
			array := payloadValueAtPath(payload, basePath)
			if !array.Exists() || !array.IsArray() {
				continue
			}
			for index, item := range array.Array() {
				if !payloadQueryMatches(item, query) {
					continue
				}
				nextPaths = append(nextPaths, appendPayloadPathPart(basePath, strconv.Itoa(index)))
				if !allMatches {
					break
				}
			}
		}
		paths = nextPaths
		if len(paths) == 0 {
			return nil
		}
	}
	return paths
}

func splitPayloadRulePath(path string) []string {
	var parts []string
	start := 0
	depth := 0
	var quote byte
	escaped := false
	for i := 0; i < len(path); i++ {
		ch := path[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '(' {
			depth++
			continue
		}
		if ch == ')' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if ch == '.' && depth == 0 {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	parts = append(parts, path[start:])
	return parts
}

func parsePayloadQueryPathPart(part string) (string, bool, bool) {
	if !strings.HasPrefix(part, "#(") {
		return "", false, false
	}
	closeIndex := findPayloadQueryClose(part)
	if closeIndex < 0 {
		return "", false, false
	}
	suffix := part[closeIndex+1:]
	if suffix != "" && suffix != "#" {
		return "", false, false
	}
	return strings.TrimSpace(part[2:closeIndex]), suffix == "#", true
}

func findPayloadQueryClose(part string) int {
	var quote byte
	escaped := false
	depth := 1
	for i := 2; i < len(part); i++ {
		ch := part[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '(' {
			depth++
			continue
		}
		if ch == ')' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func appendPayloadPathPart(path, part string) string {
	if path == "" {
		return part
	}
	if part == "" {
		return path
	}
	return path + "." + part
}

func payloadValueAtPath(payload []byte, path string) gjson.Result {
	if path == "" {
		return gjson.ParseBytes(payload)
	}
	return gjson.GetBytes(payload, path)
}

func payloadQueryMatches(item gjson.Result, query string) bool {
	for _, orPart := range splitPayloadLogical(query, "||") {
		if payloadQueryAndMatches(item, orPart) {
			return true
		}
	}
	return false
}

func payloadQueryAndMatches(item gjson.Result, query string) bool {
	parts := splitPayloadLogical(query, "&&")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !payloadQueryTermMatches(item, part) {
			return false
		}
	}
	return true
}

func splitPayloadLogical(query, operator string) []string {
	var parts []string
	start := 0
	var quote byte
	escaped := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if strings.HasPrefix(query[i:], operator) {
			parts = append(parts, strings.TrimSpace(query[start:i]))
			i += len(operator) - 1
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(query[start:]))
	return parts
}

func payloadQueryTermMatches(item gjson.Result, term string) bool {
	term = strings.TrimSpace(term)
	if term == "" || item.Raw == "" {
		return false
	}
	wrapped := make([]byte, 0, len(item.Raw)+2)
	wrapped = append(wrapped, '[')
	wrapped = append(wrapped, item.Raw...)
	wrapped = append(wrapped, ']')
	return gjson.GetBytes(wrapped, "#("+term+")").Exists()
}

func removeToolTypeFromPayloadWithRoot(payload []byte, root string, toolType string) []byte {
	if len(payload) == 0 {
		return payload
	}
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		return payload
	}
	toolsPath := buildPayloadPath(root, "tools")
	return removeToolTypeFromToolsArray(payload, toolsPath, toolType)
}

func removeToolChoiceFromPayloadWithRoot(payload []byte, root string, toolType string) []byte {
	if len(payload) == 0 {
		return payload
	}
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		return payload
	}
	toolChoicePath := buildPayloadPath(root, "tool_choice")
	return removeToolChoiceFromPayload(payload, toolChoicePath, toolType)
}

func removeToolChoiceFromPayload(payload []byte, toolChoicePath string, toolType string) []byte {
	choice := gjson.GetBytes(payload, toolChoicePath)
	if !choice.Exists() {
		return payload
	}
	if choice.Type == gjson.String {
		if strings.EqualFold(strings.TrimSpace(choice.String()), toolType) {
			updated, errDel := sjson.DeleteBytes(payload, toolChoicePath)
			if errDel == nil {
				return updated
			}
		}
		return payload
	}
	if choice.Type != gjson.JSON {
		return payload
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	if strings.EqualFold(choiceType, toolType) {
		updated, errDel := sjson.DeleteBytes(payload, toolChoicePath)
		if errDel == nil {
			return updated
		}
		return payload
	}
	if strings.EqualFold(choiceType, "tool") {
		name := strings.TrimSpace(choice.Get("name").String())
		if strings.EqualFold(name, toolType) {
			updated, errDel := sjson.DeleteBytes(payload, toolChoicePath)
			if errDel == nil {
				return updated
			}
		}
	}
	return payload
}

func removeToolTypeFromToolsArray(payload []byte, toolsPath string, toolType string) []byte {
	tools := gjson.GetBytes(payload, toolsPath)
	if !tools.Exists() || !tools.IsArray() {
		return payload
	}
	removed := false
	filtered := []byte(`[]`)
	for _, tool := range tools.Array() {
		if tool.Get("type").String() == toolType {
			removed = true
			continue
		}
		updated, errSet := sjson.SetRawBytes(filtered, "-1", []byte(tool.Raw))
		if errSet != nil {
			continue
		}
		filtered = updated
	}
	if !removed {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, toolsPath, filtered)
	if errSet != nil {
		return payload
	}
	return updated
}

func payloadRawValue(value any) ([]byte, bool) {
	if value == nil {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		raw, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return nil, false
		}
		return raw, true
	}
}

func PayloadRequestedModel(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return strings.TrimSpace(v)
	case []byte:
		if len(v) == 0 {
			return fallback
		}
		trimmed := strings.TrimSpace(string(v))
		if trimmed == "" {
			return fallback
		}
		return trimmed
	default:
		return fallback
	}
}

func PayloadRequestPath(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestPathMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

// matchModelPattern performs simple wildcard matching where '*' matches zero or more characters.
// Examples:
//
//	"*-5" matches "gpt-5"
//	"gpt-*" matches "gpt-5" and "gpt-4"
//	"gemini-*-pro" matches "gemini-2.5-pro" and "gemini-3-pro".
func matchModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	// Iterative glob-style matcher supporting only '*' wildcard.
	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && (pattern[pi] == model[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
