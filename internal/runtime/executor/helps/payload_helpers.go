package helps

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

var payloadConfigEvaluations atomic.Int64

// PayloadConfigEvaluationCount returns the cumulative number of payload rule
// engine evaluations across the process.
func PayloadConfigEvaluationCount() int64 {
	return payloadConfigEvaluations.Load()
}

// ApplyPayloadConfigWithRoot behaves like applyPayloadConfig but treats all parameter
// paths as relative to the provided root path and restricts matches to the given
// protocol when supplied. Defaults are checked
// against the original payload when provided. requestedModel carries the client-visible
// model name before alias resolution so payload rules can target aliases precisely.
// requestPath is the inbound HTTP request path (when available) used for endpoint-scoped gates.
func ApplyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte, requestedModel string, requestPath string) []byte {
	return ApplyPayloadConfigWithRequest(cfg, model, protocol, "", root, payload, original, requestedModel, requestPath, nil)
}

// ApplyPayloadConfigWithRootDoc applies payload config in place on doc with parameter paths relative to root.
func ApplyPayloadConfigWithRootDoc(cfg *config.Config, model, protocol, root string, doc *ojson.Doc, original []byte, requestedModel string, requestPath string) {
	ApplyPayloadConfigWithRequestDoc(cfg, model, protocol, "", root, doc, original, requestedModel, requestPath, nil)
}

// ApplyPayloadConfigWithRequest applies payload config using source protocol and request header gates.
func ApplyPayloadConfigWithRequest(cfg *config.Config, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header) []byte {
	out, _ := ApplyPayloadConfigWithRequestTracked(cfg, model, protocol, fromProtocol, root, payload, original, requestedModel, requestPath, headers, "")
	return out
}

// ApplyPayloadConfigWithRequestDoc applies payload config in place on doc using source protocol and request header gates.
func ApplyPayloadConfigWithRequestDoc(cfg *config.Config, model, protocol, fromProtocol, root string, doc *ojson.Doc, original []byte, requestedModel string, requestPath string, headers http.Header) {
	_ = ApplyPayloadConfigWithRequestTrackedDoc(cfg, model, protocol, fromProtocol, root, doc, original, requestedModel, requestPath, headers, "")
}

// ApplyPayloadConfigWithRequestForExecutor applies payload config with explicit target executor context.
func ApplyPayloadConfigWithRequestForExecutor(cfg *config.Config, targetExecutor, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header) []byte {
	out, _ := ApplyPayloadConfigWithTrackedPathsForExecutor(cfg, targetExecutor, model, protocol, fromProtocol, root, payload, original, requestedModel, requestPath, headers)
	return out
}

// ApplyPayloadConfigWithRequestForExecutorDoc applies payload config in place on doc with explicit target executor context.
func ApplyPayloadConfigWithRequestForExecutorDoc(cfg *config.Config, targetExecutor, model, protocol, fromProtocol, root string, doc *ojson.Doc, original []byte, requestedModel string, requestPath string, headers http.Header) {
	_ = ApplyPayloadConfigWithTrackedPathsForExecutorDoc(cfg, targetExecutor, model, protocol, fromProtocol, root, doc, original, requestedModel, requestPath, headers)
}

func isCodexTargetExecutor(targetExecutor string) bool {
	te := strings.ToLower(strings.TrimSpace(targetExecutor))
	return te == "codex" || te == "codex-websockets" || te == "codex_websockets"
}

// ApplyPayloadConfigWithTrackedPaths applies payload config and reports which
// tracked paths (or their descendants) were targeted by an applied rule.
func ApplyPayloadConfigWithTrackedPaths(cfg *config.Config, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header, trackedPaths ...string) ([]byte, map[string]bool) {
	return ApplyPayloadConfigWithTrackedPathsForExecutor(cfg, "", model, protocol, fromProtocol, root, payload, original, requestedModel, requestPath, headers, trackedPaths...)
}

// ApplyPayloadConfigWithTrackedPathsDoc applies payload config in place on doc and reports which
// tracked paths (or their descendants) were targeted by an applied rule.
func ApplyPayloadConfigWithTrackedPathsDoc(cfg *config.Config, model, protocol, fromProtocol, root string, doc *ojson.Doc, original []byte, requestedModel string, requestPath string, headers http.Header, trackedPaths ...string) map[string]bool {
	return ApplyPayloadConfigWithTrackedPathsForExecutorDoc(cfg, "", model, protocol, fromProtocol, root, doc, original, requestedModel, requestPath, headers, trackedPaths...)
}

// ApplyPayloadConfigWithTrackedPathsForExecutor applies payload config with target executor awareness.
// When headers indicate a Codex client and the target executor is not Codex or Codex WebSocket,
// it normalizes tool parameter integer types to satisfy client-side integer deserialization (#6237).
// For Codex and Codex WebSocket targets, integer normalization is skipped (#6244).
func ApplyPayloadConfigWithTrackedPathsForExecutor(cfg *config.Config, targetExecutor, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header, trackedPaths ...string) ([]byte, map[string]bool) {
	if len(payload) == 0 {
		return payload, make(map[string]bool)
	}
	doc := ojson.NewDoc(payload)
	touched, mutated := applyPayloadConfigWithTrackedPathsForExecutorDoc(cfg, targetExecutor, model, protocol, fromProtocol, root, doc, original, requestedModel, requestPath, headers, trackedPaths...)
	if !mutated {
		return payload, touched
	}
	return doc.Bytes(), touched
}

// ApplyPayloadConfigWithTrackedPathsForExecutorDoc applies payload config in place on doc with target executor awareness.
func ApplyPayloadConfigWithTrackedPathsForExecutorDoc(cfg *config.Config, targetExecutor, model, protocol, fromProtocol, root string, doc *ojson.Doc, original []byte, requestedModel, requestPath string, headers http.Header, trackedPaths ...string) map[string]bool {
	touched, _ := applyPayloadConfigWithTrackedPathsForExecutorDoc(cfg, targetExecutor, model, protocol, fromProtocol, root, doc, original, requestedModel, requestPath, headers, trackedPaths...)
	return touched
}

func applyPayloadConfigWithTrackedPathsForExecutorDoc(cfg *config.Config, targetExecutor, model, protocol, fromProtocol, root string, doc *ojson.Doc, original []byte, requestedModel, requestPath string, headers http.Header, trackedPaths ...string) (map[string]bool, bool) {
	return applyPayloadConfigWithTrackedPathsForExecutorAndAuthDoc(cfg, targetExecutor, model, protocol, fromProtocol, root, doc, original, requestedModel, requestPath, headers, nil, trackedPaths...)
}

func applyPayloadConfigWithTrackedPathsForExecutorAndAuthDoc(cfg *config.Config, targetExecutor, model, protocol, fromProtocol, root string, doc *ojson.Doc, original []byte, requestedModel, requestPath string, headers http.Header, auth *cliproxyauth.Auth, trackedPaths ...string) (map[string]bool, bool) {
	payloadConfigEvaluations.Add(1)
	touched := make(map[string]bool)
	if doc == nil || doc.Len() == 0 {
		return touched, false
	}
	mutated := false
	if IsCodexUserAgent(headers) && !isCodexTargetExecutor(targetExecutor) {
		if NormalizeCodexToolIntegerTypesDoc(doc, headers) {
			mutated = true
		}
	}
	if cfg == nil {
		return touched, mutated
	}

	markTouched := func(resolvedPath string) {
		for _, tp := range trackedPaths {
			tp = strings.TrimSpace(tp)
			if tp != "" && payloadRuleTargetsPath(resolvedPath, tp) {
				touched[tp] = true
			}
		}
	}

	var strippedToolChoicePath string
	// Apply disable-image-generation filtering before payload rules so config payload
	// overrides can explicitly re-enable image_generation when desired.
	if shouldStripImageGeneration(cfg.DisableImageGeneration, requestPath) {
		if removeToolTypeFromPayloadWithRootDoc(doc, root, "image_generation") {
			mutated = true
		}
		if removeToolChoiceFromPayloadWithRootDoc(doc, root, "image_generation") {
			mutated = true
			strippedToolChoicePath = buildPayloadPath(root, "tool_choice")
		}
	}

	rules := cfg.Payload
	hasPayloadRules := len(rules.Default) != 0 || len(rules.DefaultRaw) != 0 || len(rules.Override) != 0 || len(rules.OverrideRaw) != 0 || len(rules.Filter) != 0
	if hasPayloadRules {
		model = strings.TrimSpace(model)
		requestedModel = strings.TrimSpace(requestedModel)
		if model != "" || requestedModel != "" {
			toolsPath := buildPayloadPath(root, "tools")
			toolChoicePath := buildPayloadPath(root, "tool_choice")
			autoInjectedToolIdx := -1
			if isCodexTargetExecutor(targetExecutor) &&
				cfg.DisableImageGeneration == config.DisableImageGenerationOff &&
				len(original) > 0 &&
				!rawToolsContainImageGeneration(original, toolsPath) {
				autoInjectedToolIdx = findLastImageGenerationToolIndexDoc(doc, toolsPath)
			}
			origTools := ojson.GetBytesNoCopy(original, toolsPath)
			originalHadEmptyTools := len(original) > 0 && origTools.Exists() && origTools.IsArray() && len(origTools.Array()) == 0
			userWroteToolChoice := false
			noteRuleWrite := func(resolvedPath string) {
				markTouched(resolvedPath)
				if toolChoicePath != "" && payloadRuleTargetsPath(resolvedPath, toolChoicePath) {
					userWroteToolChoice = true
					return
				}
				if autoInjectedToolIdx < 0 || toolsPath == "" || !payloadRuleTargetsPath(resolvedPath, toolsPath) {
					return
				}
				if resolvedPath == toolsPath {
					autoInjectedToolIdx = -1
					return
				}
				rest := strings.TrimPrefix(resolvedPath, toolsPath+".")
				if idx := strings.IndexByte(rest, '.'); idx >= 0 {
					rest = rest[:idx]
				}
				if elemIdx, errIdx := strconv.Atoi(rest); errIdx == nil && elemIdx == autoInjectedToolIdx {
					autoInjectedToolIdx = -1
				}
			}
			noteRuleDelete := func(resolvedPath string) {
				if autoInjectedToolIdx < 0 || toolsPath == "" || !payloadRuleTargetsPath(resolvedPath, toolsPath) {
					return
				}
				if resolvedPath == toolsPath {
					autoInjectedToolIdx = -1
					return
				}
				rest := strings.TrimPrefix(resolvedPath, toolsPath+".")
				if strings.Contains(rest, ".") {
					return
				}
				if delIdx, errIdx := strconv.Atoi(rest); errIdx == nil {
					if delIdx == autoInjectedToolIdx {
						autoInjectedToolIdx = -1
					} else if delIdx >= 0 && delIdx < autoInjectedToolIdx {
						autoInjectedToolIdx--
					}
				}
			}
			candidates := payloadModelCandidates(model, requestedModel)
			appliedDefaults := make(map[string]struct{})
			var createdByDefault map[string]struct{}
			var initialArrayLengths map[string]int
			sourcePathExists := func(resolvedPath string) bool {
				if len(original) > 0 {
					return ojson.GetBytesNoCopy(original, resolvedPath).Exists()
				}
				if strippedToolChoicePath != "" && resolvedPath == strippedToolChoicePath {
					return true
				}
				if createdByDefault != nil || initialArrayLengths != nil {
					if _, created := createdByDefault[resolvedPath]; created {
						return false
					}
					for idx := 0; idx < len(resolvedPath); idx++ {
						if resolvedPath[idx] == '.' && (idx == 0 || resolvedPath[idx-1] != '\\') {
							prefix := resolvedPath[:idx]
							if _, created := createdByDefault[prefix]; created {
								return false
							}
							if initLen, ok := initialArrayLengths[prefix]; ok {
								nextEnd := len(resolvedPath)
								for j := idx + 1; j < len(resolvedPath); j++ {
									if resolvedPath[j] == '.' && resolvedPath[j-1] != '\\' {
										nextEnd = j
										break
									}
								}
								if arrIdx, errIdx := strconv.Atoi(resolvedPath[idx+1 : nextEnd]); errIdx == nil && arrIdx >= initLen {
									return false
								}
							}
						}
					}
				}
				return doc.Get(resolvedPath).Exists()
			}
			recordDefaultCreation := func(resolvedPath string) {
				if len(original) > 0 {
					return
				}
				if createdByDefault == nil {
					createdByDefault = make(map[string]struct{})
				}
				for idx := 0; idx < len(resolvedPath); idx++ {
					if resolvedPath[idx] == '.' && (idx == 0 || resolvedPath[idx-1] != '\\') {
						prefix := resolvedPath[:idx]
						prefixNode := doc.Get(prefix)
						if !prefixNode.Exists() {
							createdByDefault[prefix] = struct{}{}
						} else if prefixNode.IsArray() {
							if initialArrayLengths == nil {
								initialArrayLengths = make(map[string]int)
							}
							if _, recorded := initialArrayLengths[prefix]; !recorded {
								initialArrayLengths[prefix] = len(prefixNode.Array())
							}
						}
					}
				}
				if !doc.Get(resolvedPath).Exists() {
					createdByDefault[resolvedPath] = struct{}{}
				}
			}

			// Apply default rules: first write wins per field across all matching rules.
			for i := range rules.Default {
				rule := &rules.Default[i]
				if !payloadModelRulesMatchDoc(rule.Models, protocol, fromProtocol, headers, doc, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePathsDoc(doc, fullPath) {
						if sourcePathExists(resolvedPath) {
							continue
						}
						if _, ok := appliedDefaults[resolvedPath]; ok {
							continue
						}
						recordDefaultCreation(resolvedPath)
						if errSet := doc.Set(resolvedPath, value); errSet != nil {
							continue
						}
						mutated = true
						appliedDefaults[resolvedPath] = struct{}{}
						noteRuleWrite(resolvedPath)
					}
				}
			}
			// Apply default raw rules: first write wins per field across all matching rules.
			for i := range rules.DefaultRaw {
				rule := &rules.DefaultRaw[i]
				if !payloadModelRulesMatchDoc(rule.Models, protocol, fromProtocol, headers, doc, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePathsDoc(doc, fullPath) {
						if sourcePathExists(resolvedPath) {
							continue
						}
						if _, ok := appliedDefaults[resolvedPath]; ok {
							continue
						}
						rawValue, ok := payloadRawValue(value)
						if !ok {
							continue
						}
						recordDefaultCreation(resolvedPath)
						if errSet := doc.SetRawBytes(resolvedPath, rawValue); errSet != nil {
							continue
						}
						mutated = true
						appliedDefaults[resolvedPath] = struct{}{}
						noteRuleWrite(resolvedPath)
					}
				}
			}
			// Apply override rules: last write wins per field across all matching rules.
			for i := range rules.Override {
				rule := &rules.Override[i]
				if !payloadModelRulesMatchDoc(rule.Models, protocol, fromProtocol, headers, doc, root, candidates) {
					continue
				}
				for path, value := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					for _, resolvedPath := range resolvePayloadRulePathsDoc(doc, fullPath) {
						changed, applied := setPayloadValueIfDifferentDocTracked(doc, resolvedPath, value)
						if changed {
							mutated = true
						}
						if applied {
							noteRuleWrite(resolvedPath)
						}
					}
				}
			}
			// Apply override raw rules: last write wins per field across all matching rules.
			for i := range rules.OverrideRaw {
				rule := &rules.OverrideRaw[i]
				if !payloadModelRulesMatchDoc(rule.Models, protocol, fromProtocol, headers, doc, root, candidates) {
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
					for _, resolvedPath := range resolvePayloadRulePathsDoc(doc, fullPath) {
						changed, applied := setPayloadRawValueIfDifferentDocTracked(doc, resolvedPath, rawValue)
						if changed {
							mutated = true
						}
						if applied {
							noteRuleWrite(resolvedPath)
						}
					}
				}
			}
			// Apply filter rules: remove matching paths from payload.
			for i := range rules.Filter {
				rule := &rules.Filter[i]
				if !payloadModelRulesMatchDoc(rule.Models, protocol, fromProtocol, headers, doc, root, candidates) {
					continue
				}
				for _, path := range rule.Params {
					fullPath := buildPayloadPath(root, path)
					if fullPath == "" {
						continue
					}
					resolvedPaths := resolvePayloadRulePathsDoc(doc, fullPath)
					for idx := len(resolvedPaths) - 1; idx >= 0; idx-- {
						resolvedPath := resolvedPaths[idx]
						existed := doc.Get(resolvedPath).Exists()
						if errDel := doc.Delete(resolvedPath); errDel != nil {
							continue
						}
						if existed {
							mutated = true
							noteRuleDelete(resolvedPath)
						}
						markTouched(resolvedPath)
					}
				}
			}
			if autoInjectedToolIdx >= 0 && !(userWroteToolChoice && docToolChoiceSelectsImageGeneration(doc, toolChoicePath)) {
				if codexConfiguredPayloadIncompatibleWithAutoImageGenDoc(doc, root, model, auth, headers) {
					if retractAutoInjectedImageGenerationToolAtIndexDoc(doc, toolsPath, autoInjectedToolIdx, originalHadEmptyTools) {
						mutated = true
					}
				}
			}
		}
	}
	return touched, mutated
}

func findLastImageGenerationToolIndexDoc(doc *ojson.Doc, toolsPath string) int {
	if doc == nil {
		return -1
	}
	tools := doc.Get(toolsPath)
	if !tools.Exists() || !tools.IsArray() {
		return -1
	}
	items := tools.Array()
	for idx := len(items) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(items[idx].Get("type").String()) == "image_generation" {
			return idx
		}
	}
	return -1
}

func rawToolsContainImageGeneration(raw []byte, toolsPath string) bool {
	if len(raw) == 0 {
		return false
	}
	tools := ojson.GetBytesNoCopy(raw, toolsPath)
	if !tools.Exists() || !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == "image_generation" {
			return true
		}
	}
	return false
}

func docToolChoiceSelectsImageGeneration(doc *ojson.Doc, toolChoicePath string) bool {
	if doc == nil || toolChoicePath == "" {
		return false
	}
	choice := doc.Get(toolChoicePath)
	if !choice.Exists() {
		return false
	}
	if choice.Type == ojson.String {
		return strings.EqualFold(strings.TrimSpace(choice.String()), "image_generation")
	}
	if !choice.IsObject() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(choice.Get("type").String())) {
	case "image_generation":
		return true
	case "tool":
		return strings.EqualFold(strings.TrimSpace(choice.Get("name").String()), "image_generation")
	case "allowed_tools":
		allowed := choice.Get("tools")
		if !allowed.IsArray() {
			return false
		}
		for _, tool := range allowed.Array() {
			if strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "image_generation") {
				return true
			}
		}
	}
	return false
}

func codexConfiguredPayloadIncompatibleWithAutoImageGenDoc(doc *ojson.Doc, root, fallbackModel string, auth *cliproxyauth.Auth, headers http.Header) bool {
	if doc == nil {
		return false
	}
	configuredModel := strings.TrimSpace(doc.Get(buildPayloadPath(root, "model")).String())
	if configuredModel == "" {
		configuredModel = strings.TrimSpace(fallbackModel)
	}
	modelName := strings.TrimSpace(thinking.ParseSuffix(configuredModel).ModelName)
	if CodexResponsesLiteRequestForAuthDoc(doc, modelName, auth, headers) {
		return true
	}
	if strings.HasSuffix(strings.ToLower(modelName), "spark") {
		return true
	}
	return CodexModelUsesResponsesLite(modelName) && CodexRequestHasReasoningForHostedImageDoc(doc, configuredModel)
}

func retractAutoInjectedImageGenerationToolAtIndexDoc(doc *ojson.Doc, toolsPath string, targetIdx int, originalHadEmptyTools bool) bool {
	tools := doc.Get(toolsPath)
	if !tools.Exists() || !tools.IsArray() {
		return false
	}
	toolItems := tools.Array()
	if targetIdx < 0 || targetIdx >= len(toolItems) {
		return false
	}
	if strings.TrimSpace(toolItems[targetIdx].Get("type").String()) != "image_generation" {
		return false
	}
	if len(toolItems) == 1 && !originalHadEmptyTools {
		return doc.Delete(toolsPath) == nil
	}
	return doc.Delete(toolsPath+"."+strconv.Itoa(targetIdx)) == nil
}

// ApplyPayloadConfigWithRequestTracked applies payload config and reports whether
// an applied rule targeted trackedPath or one of its descendants.
func ApplyPayloadConfigWithRequestTracked(cfg *config.Config, model, protocol, fromProtocol, root string, payload, original []byte, requestedModel string, requestPath string, headers http.Header, trackedPath string) ([]byte, bool) {
	out, touched := ApplyPayloadConfigWithTrackedPaths(cfg, model, protocol, fromProtocol, root, payload, original, requestedModel, requestPath, headers, trackedPath)
	return out, touched[trackedPath]
}

// ApplyPayloadConfigWithRequestTrackedDoc applies payload config in place on doc and reports whether
// an applied rule targeted trackedPath or one of its descendants.
func ApplyPayloadConfigWithRequestTrackedDoc(cfg *config.Config, model, protocol, fromProtocol, root string, doc *ojson.Doc, original []byte, requestedModel string, requestPath string, headers http.Header, trackedPath string) bool {
	touched := ApplyPayloadConfigWithTrackedPathsDoc(cfg, model, protocol, fromProtocol, root, doc, original, requestedModel, requestPath, headers, trackedPath)
	return touched[trackedPath]
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

// shouldStripImageGeneration reports whether the built-in image_generation tool must be
// removed from the outbound payload for the given mode and request path.
//   - All: strip on every endpoint.
//   - Chat: strip only on non-images endpoints; keep it on /v1/images/* endpoints.
//   - Off / Passthrough: never strip. Off injects the tool elsewhere; Passthrough forwards
//     the client payload untouched.
func shouldStripImageGeneration(mode config.DisableImageGenerationMode, requestPath string) bool {
	switch mode {
	case config.DisableImageGenerationAll:
		return true
	case config.DisableImageGenerationChat:
		return !isImagesEndpointRequestPath(requestPath)
	default:
		return false
	}
}

func payloadModelRulesMatchDoc(rules []config.PayloadModelRule, protocol string, fromProtocol string, headers http.Header, doc *ojson.Doc, root string, models []string) bool {
	if len(rules) == 0 || len(models) == 0 || doc == nil {
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
			if payloadModelRuleConditionsMatchDoc(doc, root, entry) {
				return true
			}
		}
	}
	return false
}

func payloadModelRuleConditionsMatchDoc(doc *ojson.Doc, root string, rule config.PayloadModelRule) bool {
	if !payloadMatchConditionsMatchDoc(doc, root, rule.Match) {
		return false
	}
	if !payloadNotMatchConditionsMatchDoc(doc, root, rule.NotMatch) {
		return false
	}
	if !payloadExistConditionsMatchDoc(doc, root, rule.Exist) {
		return false
	}
	if !payloadNotExistConditionsMatchDoc(doc, root, rule.NotExist) {
		return false
	}
	return true
}

func payloadMatchConditionsMatchDoc(doc *ojson.Doc, root string, conditions []map[string]any) bool {
	for _, condition := range conditions {
		for path, value := range condition {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if !payloadPathMatchesValueDoc(doc, buildPayloadPath(root, path), value) {
				return false
			}
		}
	}
	return true
}

func payloadNotMatchConditionsMatchDoc(doc *ojson.Doc, root string, conditions []map[string]any) bool {
	for _, condition := range conditions {
		for path, value := range condition {
			if strings.TrimSpace(path) == "" {
				continue
			}
			if payloadPathMatchesValueDoc(doc, buildPayloadPath(root, path), value) {
				return false
			}
		}
	}
	return true
}

func payloadExistConditionsMatchDoc(doc *ojson.Doc, root string, paths []string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if !payloadPathExistsDoc(doc, buildPayloadPath(root, path)) {
			return false
		}
	}
	return true
}

func payloadNotExistConditionsMatchDoc(doc *ojson.Doc, root string, paths []string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if payloadPathExistsDoc(doc, buildPayloadPath(root, path)) {
			return false
		}
	}
	return true
}

func payloadPathMatchesValueDoc(doc *ojson.Doc, path string, value any) bool {
	for _, resolvedPath := range resolvePayloadRulePathsDoc(doc, path) {
		result := doc.Get(resolvedPath)
		if !result.Exists() {
			continue
		}
		if payloadResultEquals(result, value) {
			return true
		}
	}
	return false
}

func payloadPathExistsDoc(doc *ojson.Doc, path string) bool {
	for _, resolvedPath := range resolvePayloadRulePathsDoc(doc, path) {
		result := doc.Get(resolvedPath)
		if result.Exists() && result.Type != ojson.Null {
			return true
		}
	}
	return false
}

func payloadPathMatchesValue(payload []byte, path string, value any) bool {
	for _, resolvedPath := range resolvePayloadRulePaths(payload, path) {
		result := ojson.GetBytesNoCopy(payload, resolvedPath)
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
		result := ojson.GetBytesNoCopy(payload, resolvedPath)
		if result.Exists() && result.Type != ojson.Null {
			return true
		}
	}
	return false
}

func payloadResultEquals(result ojson.Result, value any) bool {
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

func normalizedPayloadResult(result ojson.Result) (any, bool) {
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

func payloadRuleTargetsPath(path, trackedPath string) bool {
	if trackedPath == "" || path == "" {
		return false
	}
	return path == trackedPath || strings.HasPrefix(path, trackedPath+".") || strings.HasPrefix(trackedPath, path+".")
}

func resolvePayloadRulePathsDoc(doc *ojson.Doc, path string) []string {
	if doc == nil {
		return nil
	}
	return resolvePayloadRulePathsFromGetter(doc.Get, path)
}

func resolvePayloadRulePaths(payload []byte, path string) []string {
	return resolvePayloadRulePathsFromGetter(func(p string) ojson.Result {
		return payloadValueAtPath(payload, p)
	}, path)
}

func resolvePayloadRulePathsFromGetter(get func(string) ojson.Result, path string) []string {
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
			array := get(basePath)
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

func payloadValueAtPath(payload []byte, path string) ojson.Result {
	if path == "" {
		return util.ParseGJSONBytesNoCopy(payload)
	}
	return ojson.GetBytesNoCopy(payload, path)
}

func payloadQueryMatches(item ojson.Result, query string) bool {
	for _, orPart := range splitPayloadLogical(query, "||") {
		if payloadQueryAndMatches(item, orPart) {
			return true
		}
	}
	return false
}

func payloadQueryAndMatches(item ojson.Result, query string) bool {
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

func payloadQueryTermMatches(item ojson.Result, term string) bool {
	term = strings.TrimSpace(term)
	if term == "" || item.Raw == "" {
		return false
	}
	wrapped := make([]byte, 0, len(item.Raw)+2)
	wrapped = append(wrapped, '[')
	wrapped = append(wrapped, item.Raw...)
	wrapped = append(wrapped, ']')
	return ojson.GetBytesNoCopy(wrapped, "#("+term+")").Exists()
}

func removeToolTypeFromPayloadWithRootDoc(doc *ojson.Doc, root string, toolType string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		return false
	}
	toolsPath := buildPayloadPath(root, "tools")
	return removeToolTypeFromToolsArrayDoc(doc, toolsPath, toolType)
}

func removeToolChoiceFromPayloadWithRootDoc(doc *ojson.Doc, root string, toolType string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		return false
	}
	toolChoicePath := buildPayloadPath(root, "tool_choice")
	return removeToolChoiceFromPayloadDoc(doc, toolChoicePath, toolType)
}

func removeToolChoiceFromPayloadDoc(doc *ojson.Doc, toolChoicePath string, toolType string) bool {
	choice := doc.Get(toolChoicePath)
	if !choice.Exists() {
		return false
	}
	if choice.Type == ojson.String {
		if strings.EqualFold(strings.TrimSpace(choice.String()), toolType) {
			return doc.Delete(toolChoicePath) == nil
		}
		return false
	}
	if choice.Type != ojson.JSON {
		return false
	}
	choiceType := strings.TrimSpace(choice.Get("type").String())
	if strings.EqualFold(choiceType, toolType) {
		return doc.Delete(toolChoicePath) == nil
	}
	if strings.EqualFold(choiceType, "tool") {
		name := strings.TrimSpace(choice.Get("name").String())
		if strings.EqualFold(name, toolType) {
			return doc.Delete(toolChoicePath) == nil
		}
	}
	return false
}

func removeToolTypeFromToolsArrayDoc(doc *ojson.Doc, toolsPath string, toolType string) bool {
	tools := doc.Get(toolsPath)
	if !tools.Exists() || !tools.IsArray() {
		return false
	}
	toolItems := tools.Array()
	removed := false
	for _, tool := range toolItems {
		if tool.Get("type").String() == toolType {
			removed = true
			break
		}
	}
	if !removed {
		return false
	}
	filtered := make([][]byte, 0, len(toolItems))
	for _, tool := range toolItems {
		if tool.Get("type").String() != toolType {
			filtered = append(filtered, []byte(tool.Raw))
		}
	}
	return doc.SetRawBytes(toolsPath, JoinRawJSONArray(filtered)) == nil
}

func removeToolTypeFromToolsArray(payload []byte, toolsPath string, toolType string) []byte {
	if len(payload) == 0 {
		return payload
	}
	doc := ojson.NewDoc(payload)
	if !removeToolTypeFromToolsArrayDoc(doc, toolsPath, toolType) {
		return payload
	}
	return doc.Bytes()
}

func setPayloadValueIfDifferent(payload []byte, path string, value any) []byte {
	updated, _ := setPayloadValueIfDifferentTracked(payload, path, value)
	return updated
}

func setPayloadValueIfDifferentTracked(payload []byte, path string, value any) ([]byte, bool) {
	current := ojson.GetBytesNoCopy(payload, path)
	switch typed := value.(type) {
	case string:
		if current.Type == ojson.String && current.String() == typed {
			return payload, true
		}
	case bool:
		if (typed && current.Type == ojson.True) || (!typed && current.Type == ojson.False) {
			return payload, true
		}
	case nil:
		if current.Raw == "null" {
			return payload, true
		}
	default:
		expectedJSON, errSet := ojson.SetBytes([]byte(`{}`), "value", value)
		if errSet != nil {
			return payload, false
		}
		expected := ojson.GetBytesNoCopy(expectedJSON, "value")
		if expected.Raw == "" {
			return payload, false
		}
		if len(current.Indexes) == 0 && current.Raw == expected.Raw {
			return payload, true
		}
		updated, errSet := ojson.SetRawBytes(payload, path, []byte(expected.Raw))
		if errSet != nil {
			return payload, false
		}
		return updated, true
	}
	updated, errSet := ojson.SetBytes(payload, path, value)
	if errSet != nil {
		return payload, false
	}
	return updated, true
}

func setPayloadValueIfDifferentDocTracked(doc *ojson.Doc, path string, value any) (bool, bool) {
	if doc == nil {
		return false, false
	}
	current := doc.Get(path)
	switch typed := value.(type) {
	case string:
		if len(current.Indexes) == 0 && current.Type == ojson.String && current.String() == typed {
			return false, true
		}
	case bool:
		if len(current.Indexes) == 0 && ((typed && current.Type == ojson.True) || (!typed && current.Type == ojson.False)) {
			return false, true
		}
	case nil:
		if len(current.Indexes) == 0 && current.Raw == "null" {
			return false, true
		}
	default:
		expectedJSON, errSet := ojson.SetBytes([]byte(`{}`), "value", value)
		if errSet != nil {
			return false, false
		}
		expected := ojson.GetBytesNoCopy(expectedJSON, "value")
		if expected.Raw == "" {
			return false, false
		}
		if len(current.Indexes) == 0 && current.Raw == expected.Raw {
			return false, true
		}
		if errSet := doc.SetRaw(path, expected.Raw); errSet != nil {
			return false, false
		}
		return true, true
	}
	if errSet := doc.Set(path, value); errSet != nil {
		return false, false
	}
	return true, true
}

func setPayloadRawValueIfDifferentDocTracked(doc *ojson.Doc, path string, value []byte) (bool, bool) {
	if doc == nil {
		return false, false
	}
	current := doc.Get(path)
	if current.Exists() && len(current.Indexes) == 0 && current.Raw == string(value) {
		return false, true
	}
	if errSet := doc.SetRawBytes(path, value); errSet != nil {
		return false, false
	}
	return true, true
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
