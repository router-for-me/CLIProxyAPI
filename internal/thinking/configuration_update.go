package thinking

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func isResponsesFormat(format string) bool {
	return format == "codex" || format == "openai-response"
}

// extractConfigurationUpdateConfig returns the last nonempty Responses effort update.
func extractConfigurationUpdateConfig(body []byte) ThinkingConfig {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return ThinkingConfig{}
	}
	return extractConfigurationUpdateConfigFromInput(ojson.GetBytesNoCopy(body, "input"))
}

func extractConfigurationUpdateConfigDoc(doc *ojson.Doc, docValid bool) ThinkingConfig {
	if doc == nil || !docValid {
		return ThinkingConfig{}
	}
	return extractConfigurationUpdateConfigFromInput(doc.Get("input"))
}

func extractConfigurationUpdateConfigFromInput(input ojson.Result) ThinkingConfig {
	if !input.IsArray() {
		return ThinkingConfig{}
	}

	var effort string
	input.ForEach(func(_, item ojson.Result) bool {
		if item.Get("type").String() == "configuration_update" {
			value := item.Get("reasoning.effort")
			if value.Type == ojson.String {
				if normalized := strings.ToLower(strings.TrimSpace(value.String())); normalized != "" {
					effort = normalized
				}
			}
		}
		return true
	})
	switch effort {
	case "":
		return ThinkingConfig{}
	case "none":
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	case "auto":
		return ThinkingConfig{Mode: ModeAuto, Budget: -1}
	default:
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(effort)}
	}
}

// normalizeNativeResponsesEffortDoc validates each effort without collapsing the
// baseline or update timeline. A valid model suffix replaces the baseline later.
func normalizeNativeResponsesEffortDoc(doc *ojson.Doc, docValid bool, modelInfo *registry.ModelInfo, applier ProviderApplier, fromFormat, toFormat string, skipBaseline bool) (bool, error) {
	if doc == nil || !docValid || IsUserDefinedModel(modelInfo) || modelInfo.Thinking == nil || len(modelInfo.Thinking.Levels) == 0 {
		return false, nil
	}

	var paths []string
	if !skipBaseline {
		paths = append(paths, "reasoning.effort")
	}
	if input := doc.Get("input"); input.IsArray() {
		input.ForEach(func(index, item ojson.Result) bool {
			if item.Get("type").String() == "configuration_update" {
				paths = append(paths, "input."+strconv.FormatInt(index.Int(), 10)+".reasoning.effort")
			}
			return true
		})
	}

	modified := false
	for _, path := range paths {
		effort := doc.Get(path)
		if effort.Type != ojson.String || strings.TrimSpace(effort.String()) == "" || HasLevel(modelInfo.Thinking.Levels, effort.String()) {
			continue
		}
		config := ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(strings.ToLower(strings.TrimSpace(effort.String())))}
		validated, errValidate := ValidateConfig(config, modelInfo, fromFormat, toFormat, false)
		if errValidate != nil {
			return modified, errValidate
		}

		// Use the provider's output rules, including ModeNone with a minimum Level.
		normalized := ojson.NewDocString("{}")
		normalizedValid := true
		if _, errApply := invokeProviderApplierOnDoc(applier, normalized, &normalizedValid, *validated, modelInfo); errApply != nil {
			return modified, errApply
		}
		value := normalized.Get("reasoning.effort")
		if value.Type == ojson.String && value.String() != effort.String() {
			if errSet := doc.Set(path, value.String()); errSet != nil {
				return modified, errSet
			}
			modified = true
		}
	}
	return modified, nil
}

// CollapseConfigurationUpdates removes every configuration_update input item.
// Unless keepTopLevelEffort is true, the last nonempty update effort is also
// promoted into the top-level reasoning.effort. Targets that reject
// configuration_update items entirely (for example Sign in with ChatGPT
// subscriptions) use it to degrade to the classic request-level effort,
// accepting the resulting prompt-cache loss. Keep the top-level effort when
// an explicit model thinking suffix already resolved the operator's override
// into it. A body without array input or without any configuration_update
// item is returned unchanged, and the top-level effort is only touched when
// an update carries a usable effort.
func CollapseConfigurationUpdates(body []byte, keepTopLevelEffort ...bool) []byte {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !collapseConfigurationUpdatesDoc(doc, true, keepTopLevelEffort...) {
		return body
	}
	return doc.Bytes()
}

// CollapseConfigurationUpdatesDoc removes every configuration_update input item
// on doc and optionally promotes the last nonempty update effort to reasoning.effort.
func CollapseConfigurationUpdatesDoc(doc *ojson.Doc, keepTopLevelEffort ...bool) {
	_ = collapseConfigurationUpdatesDoc(doc, true, keepTopLevelEffort...)
}

func collapseConfigurationUpdatesDoc(doc *ojson.Doc, docValid bool, keepTopLevelEffort ...bool) bool {
	if doc == nil || !docValid {
		return false
	}
	effort := ""
	if !(len(keepTopLevelEffort) > 0 && keepTopLevelEffort[0]) {
		effort = reasoningEffortFromConfig(extractConfigurationUpdateConfigDoc(doc, docValid))
	}
	modified := stripConfigurationUpdatesDoc(doc, docValid)
	if effort != "" {
		if doc.Set("reasoning.effort", effort) == nil {
			modified = true
		}
	}
	return modified
}

// StripConfigurationUpdates removes configuration_update input items without changing reasoning.
func StripConfigurationUpdates(body []byte) []byte {
	return stripConfigurationUpdates(body)
}

// StripConfigurationUpdatesDoc removes configuration_update input items on doc.
func StripConfigurationUpdatesDoc(doc *ojson.Doc) {
	_ = stripConfigurationUpdatesDoc(doc, true)
}

// stripConfigurationUpdates removes unsupported Responses input items without
// modifying other input items or introducing an input field.
func stripConfigurationUpdates(body []byte) []byte {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !stripConfigurationUpdatesDoc(doc, true) {
		return body
	}
	return doc.Bytes()
}

func stripConfigurationUpdatesDoc(doc *ojson.Doc, docValid bool) bool {
	if doc == nil || !docValid {
		return false
	}
	input := doc.Get("input")
	if !input.IsArray() {
		return false
	}

	var kept []string
	removed := false
	input.ForEach(func(_, item ojson.Result) bool {
		if item.Get("type").String() == "configuration_update" {
			removed = true
		} else {
			kept = append(kept, item.Raw)
		}
		return true
	})
	if !removed {
		return false
	}
	return doc.SetRaw("input", "["+strings.Join(kept, ",")+"]") == nil
}

// stripResponsesEffort leaves summary and unrelated reasoning fields intact.
func stripResponsesEffort(body []byte) []byte {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !stripResponsesEffortDoc(doc, true) {
		return body
	}
	return doc.Bytes()
}

func stripResponsesEffortDoc(doc *ojson.Doc, docValid bool) bool {
	if doc == nil || !docValid || !doc.Get("reasoning.effort").Exists() {
		return false
	}
	if errDelete := doc.Delete("reasoning.effort"); errDelete != nil {
		return false
	}
	if reasoning := doc.Get("reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
		_ = doc.Delete("reasoning")
	}
	return true
}
