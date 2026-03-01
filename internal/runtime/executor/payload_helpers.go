package executor

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyPayloadConfigWithRoot behaves like applyPayloadConfig but treats all parameter
// paths as relative to the provided root path (for example, "request" for Gemini CLI)
// and restricts matches to the given protocol when supplied. Defaults are checked
// against the original payload when provided. requestedModel carries the client-visible
// model name before alias resolution so payload rules can target aliases precisely.
func applyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte, requestedModel string) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}
	rules := cfg.Payload
	if len(rules.Default) == 0 && len(rules.DefaultRaw) == 0 && len(rules.Override) == 0 && len(rules.OverrideRaw) == 0 && len(rules.Filter) == 0 {
		return payload
	}
	model = strings.TrimSpace(model)
	requestedModel = strings.TrimSpace(requestedModel)
	if model == "" && requestedModel == "" {
		return payload
	}
	candidates := payloadModelCandidates(model, requestedModel)
	out := payload
	source := original
	if len(source) == 0 {
		source = payload
	}
	appliedDefaults := make(map[string]struct{})
	// Apply default rules: first write wins per field across all matching rules.
	for i := range rules.Default {
		rule := &rules.Default[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			if gjson.GetBytes(source, fullPath).Exists() {
				continue
			}
			if _, ok := appliedDefaults[fullPath]; ok {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
			appliedDefaults[fullPath] = struct{}{}
		}
	}
	// Apply default raw rules: first write wins per field across all matching rules.
	for i := range rules.DefaultRaw {
		rule := &rules.DefaultRaw[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			if gjson.GetBytes(source, fullPath).Exists() {
				continue
			}
			if _, ok := appliedDefaults[fullPath]; ok {
				continue
			}
			rawValue, ok := payloadRawValue(value)
			if !ok {
				continue
			}
			updated, errSet := sjson.SetRawBytes(out, fullPath, rawValue)
			if errSet != nil {
				continue
			}
			out = updated
			appliedDefaults[fullPath] = struct{}{}
		}
	}
	// Apply override rules: last write wins per field across all matching rules.
	for i := range rules.Override {
		rule := &rules.Override[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			targetPaths := expandPayloadQueryPaths(out, fullPath)
			for j := range targetPaths {
				updated, errSet := sjson.SetBytes(out, targetPaths[j], value)
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
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
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
			targetPaths := expandPayloadQueryPaths(out, fullPath)
			for j := range targetPaths {
				updated, errSet := sjson.SetRawBytes(out, targetPaths[j], rawValue)
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
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for _, path := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			targetPaths := expandPayloadQueryPaths(out, fullPath)
			// Delete in reverse to avoid array index shifts when removing elements.
			for j := len(targetPaths) - 1; j >= 0; j-- {
				updated, errDel := sjson.DeleteBytes(out, targetPaths[j])
				if errDel != nil {
					continue
				}
				out = updated
			}
		}
	}
	return out
}

// expandPayloadQueryPaths resolves one or more gjson array query path segments
// (#(...)) into concrete sjson mutation paths.
// If no query segment exists, it returns fullPath.
func expandPayloadQueryPaths(doc []byte, fullPath string) []string {
	fullPath = strings.TrimSpace(fullPath)
	if fullPath == "" {
		return nil
	}
	paths := []string{fullPath}
	for {
		expandedAny := false
		next := make([]string, 0, len(paths))
		for i := range paths {
			path := strings.TrimSpace(paths[i])
			if path == "" {
				continue
			}
			queryStart := strings.Index(path, "#(")
			if queryStart == -1 {
				next = append(next, path)
				continue
			}
			queryEnd := payloadQueryEnd(path, queryStart+2)
			if queryEnd == -1 {
				continue
			}
			matches := expandSinglePayloadQueryPath(doc, path, queryStart, queryEnd)
			if len(matches) == 0 {
				continue
			}
			next = append(next, matches...)
			expandedAny = true
		}
		if len(next) == 0 {
			return nil
		}
		if !expandedAny {
			return next
		}
		paths = next
	}
}

func expandSinglePayloadQueryPath(doc []byte, path string, queryStart, queryEnd int) []string {
	prefixPath := strings.TrimSuffix(path[:queryStart], ".")
	suffixPath := ""
	if queryEnd+1 < len(path) {
		if path[queryEnd+1] != '.' {
			return nil
		}
		suffixPath = strings.TrimPrefix(path[queryEnd+1:], ".")
	}
	queryExpr := strings.TrimSpace(path[queryStart+2 : queryEnd])
	if queryExpr == "" {
		return nil
	}

	arrayNode := gjson.ParseBytes(doc)
	if prefixPath != "" {
		arrayNode = gjson.GetBytes(doc, prefixPath)
	}
	if !arrayNode.Exists() || !arrayNode.IsArray() {
		return nil
	}

	matches := make([]string, 0, len(arrayNode.Array()))
	arrayNode.ForEach(func(index, item gjson.Result) bool {
		if !payloadQueryItemMatches(item.Raw, queryExpr) {
			return true
		}
		idx := strconv.FormatInt(index.Int(), 10)
		basePath := idx
		if prefixPath != "" {
			basePath = prefixPath + "." + idx
		}
		if suffixPath != "" {
			basePath += "." + suffixPath
		}
		matches = append(matches, basePath)
		return true
	})
	return matches
}

func payloadQueryEnd(path string, start int) int {
	inQuote := byte(0)
	escaped := false
	for i := start; i < len(path); i++ {
		ch := path[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if inQuote != 0 {
			if ch == inQuote {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == ')' {
			return i
		}
	}
	return -1
}

func payloadQueryItemMatches(itemRaw, expr string) bool {
	itemRaw = strings.TrimSpace(itemRaw)
	if itemRaw == "" || !gjson.Valid(itemRaw) {
		return false
	}
	wrapped := "[" + itemRaw + "]"
	query := "#(" + expr + ")"
	return gjson.Get(wrapped, query).Exists()
}

func payloadModelRulesMatch(rules []config.PayloadModelRule, protocol string, models []string) bool {
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
			if matchModelPattern(name, model) {
				return true
			}
		}
	}
	return false
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

func payloadRequestedModel(opts cliproxyexecutor.Options, fallback string) string {
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
