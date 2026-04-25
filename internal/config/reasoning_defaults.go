package config

import (
	"fmt"
	"sort"
	"strings"
)

const (
	ReasoningIngressFormatOpenAI = "openai"
	ReasoningIngressFormatClaude = "claude"
	ReasoningIngressFormatGemini = "gemini"
)

const (
	ReasoningIngressPolicyMissingOnly   = "missing_only"
	ReasoningIngressPolicyForceOverride = "force_override"
)

const (
	ReasoningModeEffort         = "effort"
	ReasoningModeLevel          = "level"
	ReasoningModeAdaptiveEffort = "adaptive_effort"
	ReasoningModeDisabled       = "disabled"
)

// ReasoningIngressModeSpec describes one selectable mode and its allowed values.
type ReasoningIngressModeSpec struct {
	Mode       string   `json:"mode"`
	FieldPaths []string `json:"field-paths,omitempty"`
	Values     []string `json:"values"`
}

// ReasoningIngressFormatSpec describes one protocol-family ingress configuration.
type ReasoningIngressFormatSpec struct {
	Format    string                     `json:"format"`
	AppliesTo []string                   `json:"applies-to,omitempty"`
	Policies  []string                   `json:"policies"`
	Modes     []ReasoningIngressModeSpec `json:"modes"`
}

var reasoningIngressCatalog = map[string]ReasoningIngressFormatSpec{
	ReasoningIngressFormatOpenAI: {
		Format:    ReasoningIngressFormatOpenAI,
		AppliesTo: []string{"POST /v1/chat/completions", "POST /v1/responses", "POST /v1/responses/compact", "GET /v1/responses/ws"},
		Policies:  []string{ReasoningIngressPolicyMissingOnly, ReasoningIngressPolicyForceOverride},
		Modes: []ReasoningIngressModeSpec{
			{
				Mode:       ReasoningModeEffort,
				FieldPaths: []string{"reasoning_effort", "reasoning.effort"},
				Values:     []string{"none", "auto", "minimal", "low", "medium", "high", "xhigh", "max"},
			},
		},
	},
	ReasoningIngressFormatClaude: {
		Format:    ReasoningIngressFormatClaude,
		AppliesTo: []string{"POST /v1/messages"},
		Policies:  []string{ReasoningIngressPolicyMissingOnly, ReasoningIngressPolicyForceOverride},
		Modes: []ReasoningIngressModeSpec{
			{
				Mode:       ReasoningModeAdaptiveEffort,
				FieldPaths: []string{"thinking.type", "output_config.effort"},
				Values:     []string{"low", "medium", "high", "max"},
			},
			{
				Mode:       ReasoningModeDisabled,
				FieldPaths: []string{"thinking.type"},
				Values:     []string{"disabled"},
			},
		},
	},
	ReasoningIngressFormatGemini: {
		Format:    ReasoningIngressFormatGemini,
		AppliesTo: []string{"POST /v1beta/models/*:generateContent", "POST /v1beta/models/*:streamGenerateContent"},
		Policies:  []string{ReasoningIngressPolicyMissingOnly, ReasoningIngressPolicyForceOverride},
		Modes: []ReasoningIngressModeSpec{
			{
				Mode:       ReasoningModeLevel,
				FieldPaths: []string{"generationConfig.thinkingConfig.thinkingLevel"},
				Values:     []string{"none", "auto", "minimal", "low", "medium", "high"},
			},
		},
	},
}

// ReasoningIngressFormatCatalog returns format specs for management display.
func ReasoningIngressFormatCatalog() map[string]ReasoningIngressFormatSpec {
	out := make(map[string]ReasoningIngressFormatSpec, len(reasoningIngressCatalog))
	for key, spec := range reasoningIngressCatalog {
		specCopy := spec
		specCopy.AppliesTo = append([]string(nil), spec.AppliesTo...)
		specCopy.Policies = append([]string(nil), spec.Policies...)
		specCopy.Modes = append([]ReasoningIngressModeSpec(nil), spec.Modes...)
		for i := range specCopy.Modes {
			specCopy.Modes[i].FieldPaths = append([]string(nil), spec.Modes[i].FieldPaths...)
			specCopy.Modes[i].Values = append([]string(nil), spec.Modes[i].Values...)
		}
		out[key] = specCopy
	}
	return out
}

// NormalizeReasoningOnIngressByFormat validates and normalizes ingress defaults.
func NormalizeReasoningOnIngressByFormat(input map[string]ReasoningIngressDefault) (map[string]ReasoningIngressDefault, error) {
	if len(input) == 0 {
		return nil, nil
	}
	out := make(map[string]ReasoningIngressDefault, len(input))
	for rawFormat, rawEntry := range input {
		format, entry, err := NormalizeReasoningOnIngressEntry(rawFormat, rawEntry)
		if err != nil {
			return nil, err
		}
		out[format] = entry
	}
	return out, nil
}

// NormalizeReasoningOnIngressEntry validates and normalizes one ingress entry.
func NormalizeReasoningOnIngressEntry(format string, entry ReasoningIngressDefault) (string, ReasoningIngressDefault, error) {
	format = normalizeReasoningIngressFormat(format)
	if format == "" {
		return "", ReasoningIngressDefault{}, fmt.Errorf("format is required")
	}
	spec, ok := reasoningIngressCatalog[format]
	if !ok {
		return "", ReasoningIngressDefault{}, fmt.Errorf("unsupported format %q", format)
	}

	policy := normalizeReasoningIngressPolicy(entry.Policy)
	if policy == "" {
		return "", ReasoningIngressDefault{}, fmt.Errorf(
			"format %q policy is required (supported: %s)",
			format,
			strings.Join(spec.Policies, ", "),
		)
	}
	if !containsString(spec.Policies, policy) {
		return "", ReasoningIngressDefault{}, fmt.Errorf(
			"format %q unsupported policy %q (supported: %s)",
			format,
			policy,
			strings.Join(spec.Policies, ", "),
		)
	}

	mode := strings.ToLower(strings.TrimSpace(entry.Mode))
	if mode == "" {
		return "", ReasoningIngressDefault{}, fmt.Errorf("format %q mode is required", format)
	}
	modeSpec, ok := findIngressModeSpec(spec, mode)
	if !ok {
		return "", ReasoningIngressDefault{}, fmt.Errorf(
			"format %q unsupported mode %q (supported: %s)",
			format,
			mode,
			strings.Join(specModes(spec), ", "),
		)
	}

	value := strings.ToLower(strings.TrimSpace(entry.Value))
	if value == "" && mode == ReasoningModeDisabled {
		value = "disabled"
	}
	if value == "" {
		return "", ReasoningIngressDefault{}, fmt.Errorf("format %q mode %q value is required", format, mode)
	}
	if !containsString(modeSpec.Values, value) {
		return "", ReasoningIngressDefault{}, fmt.Errorf(
			"format %q mode %q unsupported value %q (supported: %s)",
			format,
			mode,
			value,
			strings.Join(modeSpec.Values, ", "),
		)
	}

	return format, ReasoningIngressDefault{
		Policy: policy,
		Mode:   mode,
		Value:  value,
	}, nil
}

// ResolveReasoningOnIngressEntry resolves one normalized entry by format.
func ResolveReasoningOnIngressEntry(defaults map[string]ReasoningIngressDefault, format string) (ReasoningIngressDefault, bool) {
	if len(defaults) == 0 {
		return ReasoningIngressDefault{}, false
	}
	format = normalizeReasoningIngressFormat(format)
	if format == "" {
		return ReasoningIngressDefault{}, false
	}
	entry, ok := defaults[format]
	if !ok {
		return ReasoningIngressDefault{}, false
	}
	return entry, true
}

func normalizeReasoningIngressFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case ReasoningIngressFormatOpenAI, ReasoningIngressFormatClaude, ReasoningIngressFormatGemini:
		return format
	default:
		return ""
	}
}

func normalizeReasoningIngressPolicy(policy string) string {
	policy = strings.ToLower(strings.TrimSpace(policy))
	switch policy {
	case ReasoningIngressPolicyMissingOnly, ReasoningIngressPolicyForceOverride:
		return policy
	default:
		return ""
	}
}

func findIngressModeSpec(spec ReasoningIngressFormatSpec, mode string) (ReasoningIngressModeSpec, bool) {
	for _, item := range spec.Modes {
		if item.Mode == mode {
			return item, true
		}
	}
	return ReasoningIngressModeSpec{}, false
}

func specModes(spec ReasoningIngressFormatSpec) []string {
	modes := make([]string, 0, len(spec.Modes))
	for _, item := range spec.Modes {
		modes = append(modes, item.Mode)
	}
	sort.Strings(modes)
	return modes
}

func containsString(values []string, target string) bool {
	for _, item := range values {
		if item == target {
			return true
		}
	}
	return false
}
