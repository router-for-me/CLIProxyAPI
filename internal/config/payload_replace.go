package config

import (
	"fmt"

	"github.com/MoeForge/moego/json"
	"gopkg.in/yaml.v3"
)

// PayloadReplaceRule applies ordered literal replacements to existing user text.
type PayloadReplaceRule struct {
	Models       []PayloadModelRule   `yaml:"models" json:"models"`
	Target       PayloadReplaceTarget `yaml:"target" json:"target"`
	Replacements []PayloadReplacement `yaml:"replacements" json:"replacements"`
}

// PayloadReplaceTarget selects one-based user positions in the final upstream body.
// All user messages count, including messages without editable text.
type PayloadReplaceTarget struct {
	Role      string `yaml:"role" json:"role"`
	Positions []int  `yaml:"positions" json:"positions"`
	All       bool   `yaml:"all" json:"all"`
}

// UnmarshalYAML rejects fractional positions instead of allowing YAML integer coercion.
func (t *PayloadReplaceTarget) UnmarshalYAML(node *yaml.Node) error {
	var value struct {
		Role      string      `yaml:"role"`
		Positions []yaml.Node `yaml:"positions"`
		All       bool        `yaml:"all"`
	}
	if errDecode := node.Decode(&value); errDecode != nil {
		return errDecode
	}
	target := PayloadReplaceTarget{Role: value.Role, All: value.All}
	if value.Positions != nil {
		target.Positions = make([]int, len(value.Positions))
	}
	for i := range value.Positions {
		position := &value.Positions[i]
		if position.Kind == yaml.AliasNode {
			position = position.Alias
		}
		if position == nil || position.Kind != yaml.ScalarNode || position.Tag != "!!int" {
			return fmt.Errorf("payload replacement positions must be integers")
		}
		if errDecode := position.Decode(&target.Positions[i]); errDecode != nil {
			return errDecode
		}
	}
	*t = target
	return nil
}

// PayloadReplacement is a single literal source-to-destination mapping.
// The enclosing slice, not map iteration order, determines replacement order.
type PayloadReplacement map[string]string

// UnmarshalYAML prevents YAML from coercing numeric or boolean operands to strings.
func (r *PayloadReplacement) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return fmt.Errorf("payload replacement must contain exactly one string mapping")
	}
	from, to := node.Content[0], node.Content[1]
	if from.Kind != yaml.ScalarNode || from.Tag != "!!str" || to.Kind != yaml.ScalarNode || to.Tag != "!!str" {
		return fmt.Errorf("payload replacement source and destination must be strings")
	}
	*r = PayloadReplacement{from.Value: to.Value}
	return nil
}

// UnmarshalJSON enforces the same string-only mapping contract as YAML.
func (r *PayloadReplacement) UnmarshalJSON(data []byte) error {
	var values map[string]any
	if errDecode := json.Unmarshal(data, &values); errDecode != nil {
		return fmt.Errorf("decode payload replacement: %w", errDecode)
	}
	if len(values) != 1 {
		return fmt.Errorf("payload replacement must contain exactly one string mapping")
	}
	for from, value := range values {
		to, ok := value.(string)
		if !ok {
			return fmt.Errorf("payload replacement destination must be a string")
		}
		*r = PayloadReplacement{from: to}
	}
	return nil
}

// Validate rejects unsafe implicit targets and ambiguous replacement entries.
func (r PayloadReplaceRule) Validate() error {
	if r.Target.Role != "user" {
		return fmt.Errorf("target.role must be user")
	}
	if r.Target.All {
		if len(r.Target.Positions) != 0 {
			return fmt.Errorf("target.all and target.positions are mutually exclusive")
		}
	} else if len(r.Target.Positions) == 0 {
		return fmt.Errorf("target.positions must not be empty unless target.all is true")
	}
	for _, position := range r.Target.Positions {
		if position < 1 {
			return fmt.Errorf("target.positions must contain positive one-based positions")
		}
	}
	if len(r.Replacements) == 0 {
		return fmt.Errorf("replacements must not be empty")
	}
	for _, replacement := range r.Replacements {
		if len(replacement) != 1 {
			return fmt.Errorf("each replacement must contain exactly one string mapping")
		}
		if _, empty := replacement[""]; empty {
			return fmt.Errorf("replacement source must not be empty")
		}
	}
	return nil
}
