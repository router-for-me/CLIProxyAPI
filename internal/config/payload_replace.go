package config

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/json"
	"gopkg.in/yaml.v3"
)

// PayloadReplaceRule applies ordered literal and Go RE2 regex replacements to existing strings.
type PayloadReplaceRule struct {
	Models       []PayloadModelRule   `yaml:"models" json:"models"`
	Target       PayloadReplaceTarget `yaml:"target" json:"target"`
	Replacements []PayloadReplacement `yaml:"replacements" json:"replacements"`
}

// PayloadReplaceTarget selects either JSON paths or one-based positions of a role
// in the final upstream body. Paths are mutually exclusive with role selectors.
// Messages without editable text still count; a top-level system field counts as one message.
type PayloadReplaceTarget struct {
	Role      string   `yaml:"role" json:"role"`
	Positions []int    `yaml:"positions" json:"positions"`
	All       bool     `yaml:"all" json:"all"`
	Paths     []string `yaml:"paths,omitempty" json:"paths,omitempty"`
}

// UnmarshalYAML rejects fractional positions and non-string paths instead of allowing YAML coercion.
func (t *PayloadReplaceTarget) UnmarshalYAML(node *yaml.Node) error {
	var value struct {
		Role      string      `yaml:"role"`
		Positions []yaml.Node `yaml:"positions"`
		All       bool        `yaml:"all"`
		Paths     []yaml.Node `yaml:"paths"`
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
	if value.Paths != nil {
		target.Paths = make([]string, len(value.Paths))
	}
	for i := range value.Paths {
		path, ok := resolveYAMLStringScalar(&value.Paths[i])
		if !ok {
			return fmt.Errorf("payload replacement paths must be strings")
		}
		target.Paths[i] = path
	}
	*t = target
	return nil
}

// PayloadReplacement is either a single literal source-to-destination mapping
// ({"from": "to"}) or a Go RE2 regex rule ({"pattern": "...", "replacement": "..."}).
// The enclosing slice, not map iteration order, determines replacement order.
type PayloadReplacement map[string]string

func resolveYAMLStringScalar(node *yaml.Node) (string, bool) {
	if node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", false
	}
	return node.Value, true
}

// UnmarshalYAML prevents YAML from coercing numeric or boolean operands to strings.
func (r *PayloadReplacement) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("payload replacement must be a mapping")
	}
	switch len(node.Content) {
	case 2:
		from, okFrom := resolveYAMLStringScalar(node.Content[0])
		to, okTo := resolveYAMLStringScalar(node.Content[1])
		if !okFrom || !okTo {
			return fmt.Errorf("payload replacement source and destination must be strings")
		}
		*r = PayloadReplacement{from: to}
		return nil
	case 4:
		parsed := make(PayloadReplacement, 2)
		for i := 0; i < len(node.Content); i += 2 {
			key, okKey := resolveYAMLStringScalar(node.Content[i])
			val, okVal := resolveYAMLStringScalar(node.Content[i+1])
			if !okKey || !okVal {
				return fmt.Errorf("payload replacement pattern and replacement must be strings")
			}
			if _, exists := parsed[key]; exists {
				return fmt.Errorf("payload replacement contains duplicate keys")
			}
			parsed[key] = val
		}
		if _, hasPattern := parsed["pattern"]; !hasPattern {
			return fmt.Errorf("payload replacement must contain either one string mapping or pattern and replacement")
		}
		if _, hasReplacement := parsed["replacement"]; !hasReplacement {
			return fmt.Errorf("payload replacement must contain either one string mapping or pattern and replacement")
		}
		*r = parsed
		return nil
	default:
		return fmt.Errorf("payload replacement must contain either one string mapping or pattern and replacement")
	}
}

// UnmarshalJSON enforces the same string-only mapping contract as YAML.
func (r *PayloadReplacement) UnmarshalJSON(data []byte) error {
	var values map[string]any
	if errDecode := json.Unmarshal(data, &values); errDecode != nil {
		return fmt.Errorf("decode payload replacement: %w", errDecode)
	}
	switch len(values) {
	case 1:
		for from, value := range values {
			to, ok := value.(string)
			if !ok {
				return fmt.Errorf("payload replacement destination must be a string")
			}
			*r = PayloadReplacement{from: to}
		}
		return nil
	case 2:
		rawPattern, hasPattern := values["pattern"]
		rawReplacement, hasReplacement := values["replacement"]
		if !hasPattern || !hasReplacement {
			return fmt.Errorf("payload replacement must contain either one string mapping or pattern and replacement")
		}
		pattern, okPattern := rawPattern.(string)
		replacement, okReplacement := rawReplacement.(string)
		if !okPattern || !okReplacement {
			return fmt.Errorf("payload replacement pattern and replacement must be strings")
		}
		*r = PayloadReplacement{"pattern": pattern, "replacement": replacement}
		return nil
	default:
		return fmt.Errorf("payload replacement must contain either one string mapping or pattern and replacement")
	}
}

// Resolve classifies a replacement entry as a literal ("from": "to") or
// Go RE2 pattern ({"pattern": "...", "replacement": "..."}) pair.
func (r PayloadReplacement) Resolve() (source, replacement string, isRegex bool, err error) {
	switch len(r) {
	case 1:
		for from, to := range r {
			if from == "" {
				return "", "", false, fmt.Errorf("replacement source must not be empty")
			}
			return from, to, false, nil
		}
	case 2:
		pattern, hasPattern := r["pattern"]
		repl, hasReplacement := r["replacement"]
		if !hasPattern || !hasReplacement {
			return "", "", false, fmt.Errorf("each replacement must contain either one string mapping or pattern and replacement")
		}
		return pattern, repl, true, nil
	}
	return "", "", false, fmt.Errorf("each replacement must contain either one string mapping or pattern and replacement")
}

// Validate rejects unsafe implicit targets and ambiguous replacement entries.
// Invalid regexes are skipped individually during sanitization and execution.
func (r PayloadReplaceRule) Validate() error {
	if r.Target.Paths != nil {
		if len(r.Target.Paths) == 0 {
			return fmt.Errorf("target.paths must not be empty")
		}
		if r.Target.Role != "" || r.Target.All || len(r.Target.Positions) != 0 {
			return fmt.Errorf("target.paths and role selectors are mutually exclusive")
		}
		for _, path := range r.Target.Paths {
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("target.paths must contain non-empty paths")
			}
		}
	} else {
		switch r.Target.Role {
		case "system", "developer", "user":
		default:
			return fmt.Errorf("target.role must be system, developer, or user")
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
	}
	if len(r.Replacements) == 0 {
		return fmt.Errorf("replacements must not be empty")
	}
	for _, replacement := range r.Replacements {
		if _, _, _, errResolve := replacement.Resolve(); errResolve != nil {
			return errResolve
		}
	}
	return nil
}
