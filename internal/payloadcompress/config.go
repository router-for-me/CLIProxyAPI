// Package payloadcompress shrinks request payloads before they reach an upstream
// provider. Every transform is deterministic: identical input bytes must produce
// identical output bytes, otherwise a changing prompt prefix invalidates the
// provider prompt cache and costs more than the saved tokens are worth.
package payloadcompress

import (
	"strings"

	log "github.com/sirupsen/logrus"
)

// Config mirrors the requests.compression YAML block.
type Config struct {
	// Enabled turns context compression on. Off by default.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Shadow computes and reports compression but sends the original payload.
	Shadow bool `yaml:"shadow" json:"shadow"`
	// MinBytes skips payloads smaller than this many bytes.
	MinBytes int `yaml:"min-bytes" json:"min-bytes"`
	// DefaultCombo names the combo used when no other combo matches the model.
	DefaultCombo string `yaml:"default-combo" json:"default-combo"`
	// Combos lists named, ordered engine pipelines.
	Combos []Combo `yaml:"combos" json:"combos"`
	// OutputStyles injects response-style instructions into the system prompt.
	// Independent of Enabled: styles are prompt shaping, not payload compression.
	OutputStyles []OutputStyle `yaml:"output-styles" json:"output-styles"`
}

// Combo is one named engine pipeline, optionally scoped to models.
type Combo struct {
	Name string `yaml:"name" json:"name"`
	// Models holds glob patterns ("claude-*"). Empty or "*" matches everything.
	Models []string `yaml:"models" json:"models"`
	// Pipeline runs in order; each engine sees the previous engine's output.
	Pipeline []Step `yaml:"pipeline" json:"pipeline"`
}

// Step names one engine in a combo pipeline.
type Step struct {
	Engine string `yaml:"engine" json:"engine"`
}

// Supported engine names.
const (
	EngineSessionDedup = "session-dedup"
	EngineHeadroom     = "headroom"
	EngineLite         = "lite"
	EngineRTK          = "rtk"
)

const (
	defaultMinBytes = 4096
	defaultCombo    = "standard"
)

// DefaultConfig returns the built-in pipeline. It only contains lossless
// transforms, so it cannot corrupt a payload that is later replayed.
func DefaultConfig() Config {
	return Config{
		MinBytes:     defaultMinBytes,
		DefaultCombo: defaultCombo,
		Combos: []Combo{{
			Name:     defaultCombo,
			Models:   []string{"*"},
			Pipeline: []Step{{Engine: EngineSessionDedup}, {Engine: EngineLite}},
		}},
	}
}

func knownEngine(name string) bool {
	switch name {
	case EngineSessionDedup, EngineHeadroom, EngineLite, EngineRTK:
		return true
	}
	return false
}

// Sanitize drops unknown engines and duplicate combos, and repairs the default
// combo name so a typo in config degrades to "no compression", never to a panic.
func (c *Config) Sanitize() {
	if c == nil {
		return
	}
	if c.MinBytes <= 0 {
		c.MinBytes = defaultMinBytes
	}
	seen := make(map[string]struct{}, len(c.Combos))
	combos := c.Combos[:0]
	for _, combo := range c.Combos {
		name := strings.ToLower(strings.TrimSpace(combo.Name))
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			log.Warnf("payloadcompress: duplicate combo %q ignored", name)
			continue
		}
		steps := make([]Step, 0, len(combo.Pipeline))
		for _, step := range combo.Pipeline {
			engine := strings.ToLower(strings.TrimSpace(step.Engine))
			if !knownEngine(engine) {
				log.Warnf("payloadcompress: unknown engine %q in combo %q ignored", step.Engine, name)
				continue
			}
			steps = append(steps, Step{Engine: engine})
		}
		if len(steps) == 0 {
			continue
		}
		seen[name] = struct{}{}
		combos = append(combos, Combo{Name: name, Models: combo.Models, Pipeline: steps})
	}
	c.Combos = combos

	styles := make([]OutputStyle, 0, len(c.OutputStyles))
	seenStyle := make(map[string]struct{}, len(c.OutputStyles))
	for _, style := range c.OutputStyles {
		id := strings.ToLower(strings.TrimSpace(style.ID))
		if !knownStyle(id) {
			log.Warnf("payloadcompress: unknown output style %q ignored", style.ID)
			continue
		}
		if _, dup := seenStyle[id]; dup {
			continue
		}
		seenStyle[id] = struct{}{}
		styles = append(styles, OutputStyle{ID: id, Level: normalizeLevel(style.Level)})
	}
	c.OutputStyles = styles

	c.DefaultCombo = strings.ToLower(strings.TrimSpace(c.DefaultCombo))
	if _, ok := seen[c.DefaultCombo]; ok {
		return
	}
	if len(c.Combos) > 0 {
		c.DefaultCombo = c.Combos[0].Name
		return
	}
	c.DefaultCombo = defaultCombo
}

// resolve picks the first model-scoped combo that matches, then the default
// combo. A nil result means nothing applies to this request.
func (c Config) resolve(model string) *Combo {
	var fallback *Combo
	for i := range c.Combos {
		combo := &c.Combos[i]
		if combo.Name == c.DefaultCombo {
			fallback = combo
			continue
		}
		if comboMatchesModel(combo, model) {
			return combo
		}
	}
	if fallback != nil && comboMatchesModel(fallback, model) {
		return fallback
	}
	return nil
}

func comboMatchesModel(combo *Combo, model string) bool {
	if len(combo.Models) == 0 {
		return true
	}
	for _, pattern := range combo.Models {
		if matchGlob(pattern, model) {
			return true
		}
	}
	return false
}

// matchGlob supports a single '*' wildcard, matching payload rule semantics.
func matchGlob(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	value = strings.TrimSpace(value)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	pi, si := 0, 0
	starIdx, matchIdx := -1, 0
	for si < len(value) {
		if pi < len(pattern) && pattern[pi] == value[si] {
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
		if starIdx < 0 {
			return false
		}
		pi = starIdx + 1
		matchIdx++
		si = matchIdx
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
