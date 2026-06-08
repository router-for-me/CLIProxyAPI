package helps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
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
func ApplyPayloadConfigWithRequest(cfg *config.Config, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header) []byte {
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
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
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
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
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
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
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
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
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
				if !payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
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
	// 运行匹配的 JavaScript 请求前处理器规则以支持动态拦截修改请求载荷与请求头
	if len(cfg.Payload.JSHandler) > 0 {
		model = strings.TrimSpace(model)
		requestedModel = strings.TrimSpace(requestedModel)
		if model != "" || requestedModel != "" {
			candidates := payloadModelCandidates(model, requestedModel)
			reqID := ""
			if headers != nil {
				reqID = headers.Get("X-Request-Id")
				if reqID == "" {
					reqID = headers.Get("x-request-id")
				}
			}
			if reqID == "" {
				reqID = generate_request_id()
			}
			for i := range cfg.Payload.JSHandler {
				rule := &cfg.Payload.JSHandler[i]
				if payloadModelRulesMatch(rule.Models, protocol, fromProtocol, headers, out, root, candidates) {
					for _, script_path := range rule.Params {
						script_path = strings.TrimSpace(script_path)
						if script_path == "" {
							continue
						}
						processed, err_js := apply_js_before_request(script_path, out, reqID, model, protocol, headers)
						if err_js != nil {
							log.Warnf("执行 JavaScript 请求前处理器 [%s] 失败: %v", script_path, err_js)
							continue
						}
						out = processed
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

func payloadModelRulesMatch(rules []config.PayloadModelRule, protocol string, fromProtocol string, headers http.Header, payload []byte, root string, models []string) bool {
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
			if !payloadFromProtocolMatches(entry.FromProtocol, fromProtocol) {
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

func payloadFromProtocolMatches(pattern, fromProtocol string) bool {
	pattern = normalizePayloadFromProtocol(pattern)
	if pattern == "" {
		return true
	}
	fromProtocol = normalizePayloadFromProtocol(fromProtocol)
	if fromProtocol == "" {
		return false
	}
	return strings.EqualFold(pattern, fromProtocol)
}

func normalizePayloadFromProtocol(protocol string) string {
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

// js_cached_program 结构体表示已编译的 JS 脚本程序及其实时文件修改时间，供热重载比对使用。
type js_cached_program struct {
	program  *goja.Program // 已编译的 JavaScript 程序
	mod_time time.Time     // 文件最后修改时间
}

var (
	// js_programs_mu 读写锁，用于保护 js_programs_cache 的并发访问
	js_programs_mu sync.RWMutex
	// js_programs_cache 缓存已编译的 JS 脚本，避免重复解析编译
	js_programs_cache = make(map[string]js_cached_program)
)

// generate_request_id 生成带有当前时间前缀和十六进制后缀的唯一追踪 ID。
func generate_request_id() string {
	t := time.Now().Format("20060102150405")
	nano := time.Now().UnixNano()
	return fmt.Sprintf("%s-%x", t, nano&0xffffffff)
}

// get_js_program 读取 JavaScript 脚本并将其编译为 goja.Program，支持基于文件修改时间进行热重载，避免重复进行语法分析。
func get_js_program(path string) (*goja.Program, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	mod_time := info.ModTime()

	js_programs_mu.RLock()
	cached, exists := js_programs_cache[path]
	js_programs_mu.RUnlock()
	if exists && cached.mod_time.Equal(mod_time) {
		return cached.program, nil
	}

	js_programs_mu.Lock()
	defer js_programs_mu.Unlock()
	// 双重检查锁定
	if cached, exists = js_programs_cache[path]; exists && cached.mod_time.Equal(mod_time) {
		return cached.program, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	compiled, err := goja.Compile(path, string(data), false)
	if err != nil {
		return nil, fmt.Errorf("编译 JS 脚本 %s 失败: %w", path, err)
	}

	js_programs_cache[path] = js_cached_program{
		program:  compiled,
		mod_time: mod_time,
	}
	return compiled, nil
}

// apply_js_before_request 在新虚拟机中加载并运行匹配的 JS 脚本，触发 on_before_request 钩子函数拦截请求载荷。
func apply_js_before_request(script_path string, payload_bytes []byte, req_id string, model, protocol string, headers http.Header) ([]byte, error) {
	program, err := get_js_program(script_path)
	if err != nil {
		return nil, err
	}

	engine := new_js_engine()
	if err_run := engine.run_program(program); err_run != nil {
		return nil, err_run
	}

	// 拼装请求头 map
	headers_map := make(map[string]any)
	if headers != nil {
		for k, v := range headers {
			if len(v) > 0 {
				headers_map[k] = v[0]
			}
		}
	}

	// 组装统一的 ctx 参数对象，body 直接传递原始字符串以保证非 JSON 兼容的鲁棒性
	js_ctx := map[string]any{
		"id":       req_id,
		"body":     string(payload_bytes),
		"headers":  headers_map,
		"url":      "", // 暂时空缺，在拦截前请求时不需要，但保持占位
		"model":    model,
		"protocol": protocol,
	}

	// 触发执行钩子，限制 1 秒超时
	js_val, err_call := engine.call_function("on_before_request", 1*time.Second, js_ctx)
	if err_call != nil {
		// 如果函数本身没声明，call_function 会返回错误，我们在此处捕获若为不存在则透传，否则报出错误
		if strings.Contains(err_call.Error(), "不存在") {
			return payload_bytes, nil
		}
		return nil, err_call
	}

	if js_val == nil || goja.IsUndefined(js_val) || goja.IsNull(js_val) {
		return payload_bytes, nil
	}

	// 多态兼容数据获取
	exported := js_val.Export()
	if exported == nil {
		return payload_bytes, nil
	}

	// 情况 A：返回的是包装后的 ctx 对象
	if obj_map, ok := exported.(map[string]any); ok {
		// 从返回 of ctx 对象中提取修改后的请求头，并写回 headers
		if headers_val, exists_headers := obj_map["headers"]; exists_headers {
			update_header_from_any(headers, headers_val)
		}

		if body_val, exists := obj_map["body"]; exists {
			if body_str, ok_str := body_val.(string); ok_str {
				return []byte(body_str), nil
			}
		}
	}

	// 情况 B：返回的直接就是 body 字符串本身
	if body_str, ok := exported.(string); ok {
		return []byte(body_str), nil
	}

	return payload_bytes, nil
}

// header_to_map 将 Go 语言的 http.Header 对象转为 JS 易读的扁平 map 结构。
func header_to_map(h http.Header) map[string]string {
	m := make(map[string]string)
	if h == nil {
		return m
	}
	for k, v := range h {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}

// update_header_from_any 将 JS 修改后的扁平响应头 map 字段（支持 map[string]any 或 map[string]string 等多种类型）更新回 Go 语言的 http.Header。
// 针对值为 nil、空字符串的情况，会在 Go 端的 http.Header 中同步将其 Del 删除。
func update_header_from_any(h http.Header, val interface{}) {
	if h == nil || val == nil {
		return
	}
	rv := reflect.ValueOf(val)
	if rv.Kind() != reflect.Map {
		return
	}
	for _, key := range rv.MapKeys() {
		kStr := key.String()
		vVal := rv.MapIndex(key).Interface()
		if vVal == nil {
			h.Del(kStr)
		} else if val_str, ok := vVal.(string); ok {
			if strings.TrimSpace(val_str) == "" {
				h.Del(kStr)
			} else {
				h.Set(kStr, val_str)
			}
		} else {
			// 如果是非字符串但也非 nil，可以使用 fmt.Sprintf 兜底以确保容错性
			h.Set(kStr, fmt.Sprintf("%v", vVal))
		}
	}
}

// apply_js_after_response 在新虚拟机中加载并运行匹配的 JS 脚本，触发 on_after_response 钩子函数拦截并修改响应载荷、响应流分块或响应头。
func apply_js_after_response(script_path string, req_id string, model, protocol string, headers http.Header, req_body []byte, body_str string, chunk_str *string, resp_headers http.Header, is_stream bool, history_chunks []string) (string, *string, error) {
	program, err := get_js_program(script_path)
	if err != nil {
		return body_str, chunk_str, err
	}

	engine := new_js_engine()
	if err_run := engine.run_program(program); err_run != nil {
		return body_str, chunk_str, err_run
	}

	// 拼装请求头 map
	headers_map := make(map[string]any)
	if headers != nil {
		for k, v := range headers {
			if len(v) > 0 {
				headers_map[k] = v[0]
			}
		}
	}

	// 构造 req 上下文信息
	req_ctx := map[string]any{
		"body":    string(req_body),
		"headers": headers_map,
		"url":     "", // 暂时留空
	}

	// 构造 body 的值：当为流式响应时，body 必须为 null
	var body_val any = body_str
	if is_stream {
		body_val = nil
	}

	// 组装统一的 ctx 参数对象。响应头 headers 直接挂在根属性上，不需要 resp 对象。
	js_ctx := map[string]any{
		"id":       req_id,
		"body":     body_val,
		"req":      req_ctx,
		"protocol": protocol,
		"headers":  header_to_map(resp_headers),
	}

	if is_stream {
		if chunk_str != nil {
			js_ctx["chunk"] = *chunk_str
		} else {
			js_ctx["chunk"] = ""
		}
		js_ctx["history_chunks"] = history_chunks
	} else {
		js_ctx["chunk"] = nil
		js_ctx["history_chunks"] = nil
	}

	// 注入 js_ctx 到 JS VM 中
	_ = engine.vm.Set("js_ctx", js_ctx)

	// 如果是流式响应，对 history_chunks 执行锁定和深度冻结，确保 JS 内彻底只读
	if is_stream {
		_, _ = engine.vm.RunString(`
			(function() {
				if (js_ctx) {
					var list = js_ctx.history_chunks || [];
					Object.freeze(list);
					Object.defineProperty(js_ctx, "history_chunks", {
						value: list,
						writable: false,
						configurable: false
					});
				}
			})();
		`)
	}

	// 从全局中提取已锁定并深度冻结的对象作为实参，保证只读策略生效
	js_ctx_val := engine.vm.Get("js_ctx")

	// 触发执行钩子，限制 1 秒超时
	js_val, err_call := engine.call_function("on_after_response", 1*time.Second, js_ctx_val)
	if err_call != nil {
		// 如果函数本身没有声明，直接返回原始数据
		if strings.Contains(err_call.Error(), "不存在") {
			return body_str, chunk_str, nil
		}
		return body_str, chunk_str, err_call
	}

	if js_val == nil || goja.IsUndefined(js_val) || goja.IsNull(js_val) {
		return body_str, chunk_str, nil
	}

	exported := js_val.Export()
	if exported == nil {
		return body_str, chunk_str, nil
	}

	// 情况 A：返回的是包装后的 ctx 对象
	if obj_map, ok := exported.(map[string]any); ok {
		// 从返回的 ctx 根对象中直接提取修改后的响应头，并写回 resp_headers
		if headers_val, exists := obj_map["headers"]; exists {
			update_header_from_any(resp_headers, headers_val)
		}

		// 常规非流式响应：读取 body 属性并修改
		if !is_stream {
			if body_val, exists := obj_map["body"]; exists {
				if b_str, ok_str := body_val.(string); ok_str {
					return b_str, nil, nil
				}
			}
		} else {
			// 流式响应：读取 chunk 属性并修改（只提取 chunk，忽略 history_chunks 属性以防篡改）
			if chunk_val, exists := obj_map["chunk"]; exists {
				if c_str, ok_str := chunk_val.(string); ok_str {
					return body_str, &c_str, nil
				}
			}
		}
	}

	// 情况 B：返回的直接就是字符串本身
	if str_val, ok := exported.(string); ok {
		if !is_stream {
			return str_val, nil, nil
		} else {
			return body_str, &str_val, nil
		}
	}

	return body_str, chunk_str, nil
}

// ApplyJSAfterResponse 运行所有匹配的 JavaScript 处理器对非流式响应进行处理。
func ApplyJSAfterResponse(cfg *config.Config, req_id string, model, requested_model, protocol string, headers http.Header, req_body []byte, resp_body []byte, resp_headers http.Header) ([]byte, http.Header) {
	if cfg == nil || len(resp_body) == 0 || len(cfg.Payload.JSHandler) == 0 {
		return resp_body, resp_headers
	}

	model = strings.TrimSpace(model)
	requested_model = strings.TrimSpace(requested_model)
	if model == "" && requested_model == "" {
		return resp_body, resp_headers
	}

	candidates := payloadModelCandidates(model, requested_model)
	out := string(resp_body)

	for i := range cfg.Payload.JSHandler {
		rule := &cfg.Payload.JSHandler[i]
		if payloadModelRulesMatch(rule.Models, protocol, "", headers, req_body, "", candidates) {
			for _, script_path := range rule.Params {
				script_path = strings.TrimSpace(script_path)
				if script_path == "" {
					continue
				}
				processed_body, _, err_js := apply_js_after_response(script_path, req_id, model, protocol, headers, req_body, out, nil, resp_headers, false, nil)
				if err_js != nil {
					// 捕获异常，打印中文警告日志，降级使用未修改的数据
					log.Warnf("执行 JavaScript 响应处理器 [%s] 失败: %v", script_path, err_js)
					continue
				}
				out = processed_body
			}
		}
	}

	return []byte(out), resp_headers
}

// ApplyJSAfterResponseStream 运行所有匹配的 JavaScript 处理器对流式响应的单个分块进行处理。
func ApplyJSAfterResponseStream(cfg *config.Config, req_id string, model, requested_model, protocol string, headers http.Header, req_body []byte, history_chunks []string, current_chunk []byte, resp_headers http.Header) ([]byte, http.Header) {
	if cfg == nil || len(cfg.Payload.JSHandler) == 0 {
		return current_chunk, resp_headers
	}

	model = strings.TrimSpace(model)
	requested_model = strings.TrimSpace(requested_model)
	if model == "" && requested_model == "" {
		return current_chunk, resp_headers
	}

	candidates := payloadModelCandidates(model, requested_model)
	chunk_str := string(current_chunk)

	for i := range cfg.Payload.JSHandler {
		rule := &cfg.Payload.JSHandler[i]
		if payloadModelRulesMatch(rule.Models, protocol, "", headers, req_body, "", candidates) {
			for _, script_path := range rule.Params {
				script_path = strings.TrimSpace(script_path)
				if script_path == "" {
					continue
				}
				_, processed_chunk, err_js := apply_js_after_response(script_path, req_id, model, protocol, headers, req_body, "", &chunk_str, resp_headers, true, history_chunks)
				if err_js != nil {
					// 捕获异常，打印中文警告日志，降级使用原始分块数据
					log.Warnf("执行 JavaScript 流式响应处理器 [%s] 失败: %v", script_path, err_js)
					continue
				}
				if processed_chunk != nil {
					chunk_str = *processed_chunk
				}
			}
		}
	}

	return []byte(chunk_str), resp_headers
}
