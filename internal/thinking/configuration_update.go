package thinking

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func isResponsesFormat(format string) bool {
	return format == "codex" || format == "openai-response"
}

// extractConfigurationUpdateConfig returns the last nonempty Responses effort update.
func extractConfigurationUpdateConfig(body []byte) ThinkingConfig {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ThinkingConfig{}
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return ThinkingConfig{}
	}

	var effort string
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "configuration_update" {
			value := item.Get("reasoning.effort")
			if value.Type == gjson.String {
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
	updated := stripConfigurationUpdates(body)
	if len(keepTopLevelEffort) > 0 && keepTopLevelEffort[0] {
		return updated
	}
	effort := reasoningEffortFromConfig(extractConfigurationUpdateConfig(body))
	if effort == "" {
		return updated
	}
	collapsed, errSet := sjson.SetBytes(updated, "reasoning.effort", effort)
	if errSet != nil {
		return updated
	}
	return collapsed
}

// StripConfigurationUpdates removes configuration_update input items without changing reasoning.
func StripConfigurationUpdates(body []byte) []byte {
	return stripConfigurationUpdates(body)
}

// stripConfigurationUpdates removes unsupported Responses input items without
// modifying other input items or introducing an input field.
func stripConfigurationUpdates(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}

	var kept []string
	removed := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "configuration_update" {
			removed = true
		} else {
			kept = append(kept, item.Raw)
		}
		return true
	})
	if !removed {
		return body
	}
	updated, errSet := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(kept, ",")+"]"))
	if errSet != nil {
		return body
	}
	return updated
}

// stripResponsesEffort leaves summary and unrelated reasoning fields intact.
func stripResponsesEffort(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) || !gjson.GetBytes(body, "reasoning.effort").Exists() {
		return body
	}
	result, errDelete := sjson.DeleteBytes(body, "reasoning.effort")
	if errDelete != nil {
		return body
	}
	if reasoning := gjson.GetBytes(result, "reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
		result, _ = sjson.DeleteBytes(result, "reasoning")
	}
	return result
}
