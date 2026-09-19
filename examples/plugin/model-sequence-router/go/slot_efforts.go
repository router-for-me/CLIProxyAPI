package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"gopkg.in/yaml.v3"
)

// effortOrder lists the discrete effort levels in ascending canonical order.
var effortOrder = []thinking.ThinkingLevel{
	thinking.LevelMinimal, thinking.LevelLow, thinking.LevelMedium,
	thinking.LevelHigh, thinking.LevelXHigh, thinking.LevelMax,
}

// effortTier refines one rotation slot's answer to a single requested level.
// Model names another model of the slot's own provider, and Effort names the
// level that model receives. An unset field inherits from the slot: an unset
// Model keeps the slot's model, and an unset Effort forwards the caller's
// suffix unchanged.
type effortTier struct {
	Model  string                 `yaml:"model"`
	Effort thinking.ThinkingLevel `yaml:"effort"`
}

// UnmarshalYAML accepts a bare level as shorthand for an effort-only tier and
// a mapping carrying model, effort, or both. Decoding is strict because the
// plugin configuration as a whole is decoded non-strictly, so an unrecognized
// key would otherwise be discarded in silence.
func (t *effortTier) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		level, errLevel := parseEffortLevel(node.Value)
		if errLevel != nil {
			return errLevel
		}
		*t = effortTier{Effort: level}
		return nil
	case yaml.MappingNode:
		return t.decodeMapping(node)
	default:
		return fmt.Errorf("effort tier must be a level or a mapping of model and effort")
	}
}

// decodeMapping admits the model and effort keys of one tier mapping, then
// decodes the admitted values. Key matching is case-insensitive to agree with
// the field binding the decoder performs.
func (t *effortTier) decodeMapping(node *yaml.Node) error {
	var hasModel, hasEffort bool
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := strings.ToLower(strings.TrimSpace(node.Content[index].Value))
		switch key {
		case "model":
			hasModel = true
		case "effort":
			hasEffort = true
		case "provider":
			return fmt.Errorf("effort tier must not name a provider; a tier refines only a model of its slot's provider")
		default:
			return fmt.Errorf("effort tier has unrecognized key %q", key)
		}
	}
	type plainTier effortTier
	value := plainTier{}
	if errDecode := node.Decode(&value); errDecode != nil {
		return errDecode
	}
	value.Model = strings.TrimSpace(value.Model)
	if hasModel && value.Model == "" {
		return fmt.Errorf("effort tier model must not be blank")
	}
	if hasEffort {
		level, errLevel := parseEffortLevel(string(value.Effort))
		if errLevel != nil {
			return errLevel
		}
		value.Effort = level
	}
	*t = effortTier(value)
	return nil
}

// parseEffortLevel reads one discrete level spelling, case-insensitively.
func parseEffortLevel(raw string) (thinking.ThinkingLevel, error) {
	level, isLevel := thinking.ParseLevelSuffix(strings.TrimSpace(raw))
	if !isLevel {
		return "", fmt.Errorf("effort %q must be one of %s", raw, effortLevelNames())
	}
	return level, nil
}

// emittedModel resolves the model string this tier emits from its slot model
// and the caller's requested suffix. A suffix configured on the effective model
// outranks the tier's effort, which outranks the caller's request.
func (t effortTier) emittedModel(slotModel string, requested string) string {
	var model string
	if t.Model != "" {
		model = t.Model
	} else {
		model = slotModel
	}
	_, _, modelHasSuffix := parseSupportedEffortSuffix(model)
	var effort string
	if t.Effort != "" {
		effort = string(t.Effort)
	} else {
		effort = requested
	}
	var emitted string
	if effort != "" && !modelHasSuffix {
		emitted = model + "(" + effort + ")"
	} else {
		emitted = model
	}
	return emitted
}

// validateEfforts rejects tier maps a rotation slot cannot honor. The max level
// is reserved: a tier emits max for a max request and for no other, so a model
// reaches its own native ceiling exactly when the caller asks for it.
func validateEfforts(model string, efforts map[thinking.ThinkingLevel]effortTier) error {
	for key, tier := range efforts {
		level, isLevel := thinking.ParseLevelSuffix(string(key))
		if !isLevel || level != key {
			return fmt.Errorf("efforts key %q must be one of %s", string(key), effortLevelNames())
		}
		if tier.Model == "" && tier.Effort == "" {
			return fmt.Errorf("efforts[%s] must state a model, an effort, or both", key)
		}
		_, outgoing, hasSuffix := parseSupportedEffortSuffix(tier.emittedModel(model, string(key)))
		emitted, emitsLevel := thinking.ParseLevelSuffix(outgoing)
		emitsMax := hasSuffix && emitsLevel && emitted == thinking.LevelMax
		if key != thinking.LevelMax && emitsMax {
			return fmt.Errorf("efforts[%s] emits max; max answers a max request only", key)
		}
		if key == thinking.LevelMax && !emitsMax {
			return fmt.Errorf("efforts[max] emits %q; a max request must reach the model's own max", outgoing)
		}
	}
	return validateEffortMonotonicity(efforts)
}

// validateEffortMonotonicity requires the level-to-effort mapping to be
// non-decreasing across the canonical order, so a higher request never receives
// a lower effort. A level without a tier maps to itself. A tier naming another
// model is exempt, because no ordering relates two distinct models.
func validateEffortMonotonicity(efforts map[thinking.ThinkingLevel]effortTier) error {
	previousRank := -1
	previousOutgoing := thinking.ThinkingLevel("")
	for _, level := range effortOrder {
		tier, hasTier := efforts[level]
		if hasTier && tier.Model != "" {
			continue
		}
		var outgoing thinking.ThinkingLevel
		if hasTier && tier.Effort != "" {
			outgoing = tier.Effort
		} else {
			outgoing = level
		}
		rank := slices.Index(effortOrder, outgoing)
		if rank < 0 {
			return fmt.Errorf("efforts[%s] maps to unknown level %q", level, outgoing)
		}
		if rank < previousRank {
			return fmt.Errorf("efforts[%s] maps to %q below the %q reached by the preceding level; the mapping must not decrease", level, outgoing, previousOutgoing)
		}
		previousRank, previousOutgoing = rank, outgoing
	}
	return nil
}

// effortLevelNames renders the accepted level spellings for error messages.
func effortLevelNames() string {
	names := make([]string, 0, len(effortOrder))
	for _, level := range effortOrder {
		names = append(names, string(level))
	}
	return strings.Join(names, ", ")
}

// effectiveModel resolves the model string emitted for one requested suffix.
// Numeric budgets, the special values none and auto, and an absent suffix carry
// no discrete level, so they never match a tier and forward unchanged on the
// slot's own model.
func (t compiledTarget) effectiveModel(rawSuffix string) string {
	level, isLevel := thinking.ParseLevelSuffix(rawSuffix)
	tier, hasTier := t.Efforts[level]
	if !isLevel || !hasTier {
		return (effortTier{}).emittedModel(t.Model, rawSuffix)
	}
	return tier.emittedModel(t.Model, rawSuffix)
}
