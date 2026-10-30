// Package payloadcompress shrinks request payloads before they reach an upstream
// provider. Every transform is deterministic: identical input bytes must produce
// identical output bytes, otherwise a changing prompt prefix invalidates the
// provider prompt cache and costs more than the saved tokens are worth.
//
// Determinism alone is not enough, because a session appends turns rather than
// replaying one body. The property the cache actually needs is append-stability:
// compressing history H, then H plus more turns, must leave the compressed bytes
// of H unchanged. An engine may therefore derive a segment's output only from that
// segment and the segments before it, never from a total budget, a later turn, or
// elapsed time.
//
// Append-stability holds from the turn compression engages. A body that starts
// below Config.MinBytes and grows past it changes the prefix once, at that turn,
// so keep MinBytes below the first request of a session. Output styles are a
// separate stage chosen per request (see styleBypass) and are not covered by
// this guarantee.
package payloadcompress

import (
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// Config mirrors the requests.compression YAML block.
type Config struct {
	// Enabled turns context compression on. Off by default.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Shadow computes and reports compression but sends the original payload.
	Shadow bool `yaml:"shadow" json:"shadow"`
	// MinBytes skips payloads smaller than this many bytes.
	MinBytes int `yaml:"min-bytes" json:"min-bytes"`
	// CachePrefix decides whether the provider-cached prefix is compressed.
	// "preserve" (the default) leaves every message up to the last
	// cache_control breakpoint byte-identical, so a hit is guaranteed and the
	// cached zone costs full price. "compress" transforms that zone too and
	// relies on the pipeline being append-stable: once a session is above
	// MinBytes, the compressed bytes stay identical turn over turn, so the
	// provider caches the smaller payload.
	CachePrefix string `yaml:"cache-prefix" json:"cache-prefix"`
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
	// Level tunes engines that have an intensity ladder: lite, full, ultra.
	Level string `yaml:"level" json:"level"`
	// Options carries the per-engine knobs. Zero values mean "engine default".
	Options StepOptions `yaml:"options" json:"options"`
}

// UnmarshalYAML accepts engine options both nested under options: (the
// documented shape) and inline on the step (the upstream OmniRoute shape).
// An explicit options block wins over inline keys.
func (s *Step) UnmarshalYAML(node *yaml.Node) error {
	type plain Step
	var step plain
	if err := node.Decode(&step); err != nil {
		return err
	}
	var inline StepOptions
	if err := node.Decode(&inline); err != nil {
		return err
	}
	step.Options = inline
	if err := node.Decode(&step); err != nil {
		return err
	}
	*s = Step(step)
	return nil
}

// StepOptions tunes one pipeline step. Every field is optional; an omitted block
// keeps the defaults documented on the engine, so an empty struct is always safe.
type StepOptions struct {
	// Headroom: minimum homogeneous rows before columnar compaction.
	MinRows int `yaml:"min-rows" json:"min-rows"`

	// SessionDedup: minimum segment size that may be elided as a duplicate.
	MinBlockChars int `yaml:"min-block-chars" json:"min-block-chars"`

	// Lite: truncate a tool result longer than this many characters, head and
	// tail kept. Defaults to 2000, the upstream default.
	MaxToolLength int `yaml:"max-tool-length" json:"max-tool-length"`
	// Lite: skip tool-result truncation entirely when false.
	CompressToolResults *bool `yaml:"compress-tool-results" json:"compress-tool-results"`

	// RTK: minimal, standard or aggressive. Sets threshold defaults.
	Intensity string `yaml:"intensity" json:"intensity"`
	// RTK: maximum lines kept per segment, head and tail kept.
	MaxLines int `yaml:"max-lines" json:"max-lines"`
	// RTK: maximum characters kept per segment.
	MaxChars int `yaml:"max-chars" json:"max-chars"`
	// RTK: how many identical consecutive lines collapse into one.
	DeduplicateThreshold int `yaml:"deduplicate-threshold" json:"deduplicate-threshold"`
	// RTK: minimum percentage saved before a rewrite is kept.
	MinSavingsPercent int `yaml:"min-savings-percent" json:"min-savings-percent"`

	// Caveman: minimum user-message length considered for rewriting.
	MinMessageLength int `yaml:"min-message-length" json:"min-message-length"`
	// Caveman: rule names to disable.
	SkipRules []string `yaml:"skip-rules" json:"skip-rules"`
	// Caveman: language pack tag ("pt-BR", "ja", ...). Empty means infer.
	Language string `yaml:"language" json:"language"`
	// Caveman: infer the pack from the prompt text, choosing among LanguagePacks.
	// Defaults to true, the upstream default.
	AutoDetect *bool `yaml:"auto-detect" json:"auto-detect"`
	// Caveman: packs auto-detection may pick from. Defaults to English only.
	LanguagePacks []string `yaml:"language-packs" json:"language-packs"`
}

// Supported engine names.
const (
	EngineSessionDedup = "session-dedup"
	EngineHeadroom     = "headroom"
	EngineLite         = "lite"
	EngineRTK          = "rtk"
	EngineCaveman      = "caveman"
)

// CachePrefix modes. Only CachePrefixCompress changes behaviour; every other
// value, including an empty one, means preserve, so an unset config can never
// start rewriting a cached prefix.
const (
	CachePrefixPreserve = "preserve"
	CachePrefixCompress = "compress"
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
	case EngineSessionDedup, EngineHeadroom, EngineLite, EngineRTK, EngineCaveman:
		return true
	}
	return false
}

// normalizeEngineLevel clamps an engine level to its supported ladder. A missing
// level defaults to the safest rung.
func normalizeEngineLevel(engine, level string) string {
	if engine != EngineCaveman {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "full":
		return "full"
	case "ultra":
		return "ultra"
	default:
		return "lite"
	}
}

// sanitizeStepOptions clamps the knobs an engine would misbehave on.
func sanitizeStepOptions(opts StepOptions) StepOptions {
	if opts.MinRows < 0 {
		opts.MinRows = 0
	}
	if opts.MaxToolLength < 0 {
		opts.MaxToolLength = 0
	}
	if opts.MaxLines < 0 {
		opts.MaxLines = 0
	}
	if opts.MaxChars < 0 {
		opts.MaxChars = 0
	}
	if opts.MinSavingsPercent < 0 || opts.MinSavingsPercent > 90 {
		opts.MinSavingsPercent = 0
	}
	if opts.DeduplicateThreshold < 0 || opts.DeduplicateThreshold == 1 {
		opts.DeduplicateThreshold = 0
	}
	switch strings.ToLower(strings.TrimSpace(opts.Intensity)) {
	case "minimal":
		opts.Intensity = "minimal"
	case "standard":
		opts.Intensity = "standard"
	case "aggressive":
		opts.Intensity = "aggressive"
	default:
		opts.Intensity = ""
	}
	opts.Language = strings.TrimSpace(opts.Language)
	if opts.Language != "" && !knownLanguage(opts.Language) {
		log.Warnf("payloadcompress: unknown caveman language %q, falling back to detection", opts.Language)
		opts.Language = ""
	}
	packs := make([]string, 0, len(opts.LanguagePacks))
	for _, tag := range opts.LanguagePacks {
		tag = strings.TrimSpace(tag)
		if tag == "" || !knownLanguage(tag) {
			continue
		}
		packs = append(packs, tag)
	}
	opts.LanguagePacks = packs
	return opts
}

func knownLanguage(tag string) bool {
	for _, known := range languageTags() {
		if known == tag {
			return true
		}
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
	switch strings.ToLower(strings.TrimSpace(c.CachePrefix)) {
	case CachePrefixCompress:
		c.CachePrefix = CachePrefixCompress
	case CachePrefixPreserve, "":
		c.CachePrefix = CachePrefixPreserve
	default:
		log.Warnf("payloadcompress: unknown cache-prefix %q, falling back to preserve", c.CachePrefix)
		c.CachePrefix = CachePrefixPreserve
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
			steps = append(steps, Step{
				Engine:  engine,
				Level:   normalizeEngineLevel(engine, step.Level),
				Options: sanitizeStepOptions(step.Options),
			})
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
