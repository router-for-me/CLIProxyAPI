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
//
// A config that is enabled but names no combo falls back to DefaultConfig, so
// turning compression on is enough to get a working pipeline. prefix-freeze is
// the one optional exception to purity: it observes system prompts across
// requests, and only ever to preserve one.
package payloadcompress

import (
	"fmt"
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
	// CachePrefix decides whether the region the provider has already cached may
	// be rewritten. "rewrite" (the default) rewrites it, relying on the pipeline
	// being append-stable: once a session is above MinBytes the compressed bytes
	// stay identical turn over turn, so the provider caches the smaller payload.
	// This matches upstream, which never skips a message to protect a cache.
	// "protect" leaves every item up to the last cache_control breakpoint
	// byte-identical, so the hit is guaranteed and that region costs full price.
	// "compress" and "preserve" are accepted as aliases of the two.
	CachePrefix string `yaml:"cache-prefix" json:"cache-prefix"`
	// PreserveSystemPrompt mirrors upstream's preserveSystemPromptMode:
	// "always" (the default) never rewrites the system prompt, "when-no-cache"
	// rewrites it only when nothing is cached, and "never" rewrites it even when
	// that breaks a prompt cache.
	PreserveSystemPrompt string `yaml:"preserve-system-prompt" json:"preserve-system-prompt"`
	// PrefixFreeze is upstream's usage-observed prefix freeze: it watches the
	// system prompt and, once the same one has been seen Threshold times, treats
	// it as a stable cached prefix and preserves it. Opt-in and off by default,
	// so an unset config never depends on cross-request state.
	PrefixFreeze PrefixFreezeConfig `yaml:"prefix-freeze" json:"prefix-freeze"`
	// DefaultCombo names the combo used when no other combo matches the model.
	DefaultCombo string `yaml:"default-combo" json:"default-combo"`
	// Combos lists named, ordered engine pipelines.
	Combos []Combo `yaml:"combos" json:"combos"`
	// Exclusions names model ids or provider/model targets that bypass
	// compression entirely. Empty patterns are dropped.
	Exclusions []string `yaml:"exclusions" json:"exclusions"`
	// HardBudgetTokens caps the request size after compression; zero disables
	// the cap. Negative values are rejected.
	HardBudgetTokens int `yaml:"hard-budget-tokens" json:"hard-budget-tokens"`
	// LiveZone mirrors upstream's liveZone config: reuse the transform a previous
	// request in the same session already produced for its unchanged prefix.
	// Opt-in, because it keeps per-session state between requests.
	LiveZone LiveZoneConfig `yaml:"live-zone" json:"live-zone"`
	// OutputStyles injects response-style instructions into the system prompt.
	// Independent of Enabled: styles are prompt shaping, not payload compression.
	OutputStyles []OutputStyle `yaml:"output-styles" json:"output-styles"`
	// QuantumLock replaces volatile spans (UUIDs, timestamps, hashes, request ids)
	// in the first system message with stable placeholders plus a value tail.
	// before the engines run and restores them afterwards. Off by default.
	QuantumLock bool `yaml:"quantum-lock" json:"quantum-lock"`
}

// PrefixFreezeConfig mirrors upstream's COMPRESSION_PREFIX_FREEZE_* settings.
type PrefixFreezeConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Threshold is how many sightings of one system prompt make it stable.
	// Defaults to 3, the upstream default.
	Threshold int `yaml:"threshold" json:"threshold"`
}

// LiveZoneConfig mirrors upstream's compressionConfig.liveZone block, which
// carries nothing but the opt-in flag. The TTL and size bounds are internal.
type LiveZoneConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
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
	// Lite: keep interior whitespace inside tool results. Defaults to true, the
	// upstream safe default.
	PreserveToolWhitespace *bool `yaml:"preserve-tool-whitespace" json:"preserve-tool-whitespace"`
	// Lite: drop redundant consecutive messages. Opt-in: it discards content.
	LiteRemoveRedundant *bool `yaml:"lite-remove-redundant" json:"lite-remove-redundant"`
	// Lite: replace image payloads with text placeholders for non-vision models.
	// Opt-in: unknown or self-hosted vision models can lose images otherwise.
	LiteReplaceImages *bool `yaml:"lite-replace-images" json:"lite-replace-images"`
	// Lossy engines: require the fidelity gate before rewriting. Nil means the
	// engine default.
	FidelityGate *bool `yaml:"fidelity-gate" json:"fidelity-gate"`
	// Lossy engines: require the risk gate before rewriting. Nil means the
	// engine default.
	RiskGate *bool `yaml:"risk-gate" json:"risk-gate"`

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

	// MCP accessibility: minimum segment length considered for rewriting.
	McpMinLength int `yaml:"mcp-min-length" json:"mcp-min-length"`
	// MCP accessibility: maximum size of a rewritten segment.
	McpMaxChars int `yaml:"mcp-max-chars" json:"mcp-max-chars"`
	// MCP accessibility: repeated-pattern run length that collapses.
	McpCollapseThreshold int `yaml:"mcp-collapse-threshold" json:"mcp-collapse-threshold"`
	// MCP accessibility: lines kept at the head when collapsing repeats.
	McpCollapseKeepHead int `yaml:"mcp-collapse-keep-head" json:"mcp-collapse-keep-head"`
	// MCP accessibility: lines kept at the tail when collapsing repeats.
	McpCollapseKeepTail int `yaml:"mcp-collapse-keep-tail" json:"mcp-collapse-keep-tail"`

	// Relevance: percentage of the body kept, between 0.1 and 1.
	RelevanceBudgetPercent float64 `yaml:"relevance-budget-percent" json:"relevance-budget-percent"`
	// Relevance: similarity overlap threshold, between 0 and 1.
	RelevanceOverlapThreshold float64 `yaml:"relevance-overlap-threshold" json:"relevance-overlap-threshold"`

	// Read lifecycle: the engine is opt-in, so the step must set enabled: true.
	ReadLifecycleEnabled *bool `yaml:"enabled" json:"enabled"`

	// Presence of explicit YAML keys: an explicit 0 differs from "unset" for the
	// MCP keep knobs, where 0 means "keep no siblings".
	mcpKeepHeadSet bool
	mcpKeepTailSet bool
}

// UnmarshalYAML records whether the MCP keep knobs were set explicitly, so the
// engine can tell an explicit 0 from the zero value of an omitted field.
func (o *StepOptions) UnmarshalYAML(node *yaml.Node) error {
	// Decode in place so nested options merge with inline keys already set.
	type plain StepOptions
	if err := node.Decode((*plain)(o)); err != nil {
		return err
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			switch node.Content[i].Value {
			case "mcp-collapse-keep-head":
				o.mcpKeepHeadSet = true
			case "mcp-collapse-keep-tail":
				o.mcpKeepTailSet = true
			}
		}
	}
	return nil
}

// Supported engine names.
const (
	EngineSessionDedup = "session-dedup"
	EngineHeadroom     = "headroom"
	EngineLite         = "lite"
	EngineRTK          = "rtk"
	EngineCaveman      = "caveman"
	// EngineMCPAccessibility collapses repetitive MCP tool output.
	EngineMCPAccessibility = "mcp-accessibility"
	// EngineRelevance keeps the segments most relevant to the recent request.
	EngineRelevance = "relevance"
	// EngineReadLifecycle stubs file reads that a later read or write
	// superseded. Opt-in, like upstream.
	EngineReadLifecycle = "read-lifecycle"
	// EngineProgressiveAging compresses older turns more aggressively than
	// recent ones (`progressiveAging.ts`).
	EngineProgressiveAging = "progressive-aging"
)

// CachePrefix modes name the only cache decision this package makes: whether the
// region the provider has already cached may be rewritten. "rewrite" (the
// default, and what upstream does) rewrites it, so the smaller bytes become the
// new cached prefix after one miss. "protect" leaves it byte-identical, which
// guarantees the hit but leaves that region at full price. The older names are
// still accepted as aliases.
const (
	CachePrefixRewrite = "rewrite"
	CachePrefixProtect = "protect"

	CachePrefixCompress = CachePrefixRewrite
	CachePrefixPreserve = CachePrefixProtect
)

// PreserveSystemPrompt modes, mirroring upstream preserveSystemPromptMode.
const (
	PreserveSystemPromptAlways      = "always"
	PreserveSystemPromptWhenNoCache = "when-no-cache"
	PreserveSystemPromptNever       = "never"
)

const (
	defaultMinBytes              = 4096
	defaultCombo                 = "standard"
	defaultPrefixFreezeThreshold = 3
)

// DefaultConfig returns the built-in pipeline. It only contains lossless
// transforms, so it cannot corrupt a payload that is later replayed.
func DefaultConfig() Config {
	return Config{
		MinBytes:     defaultMinBytes,
		DefaultCombo: defaultCombo,
		Combos: []Combo{{
			Name:   defaultCombo,
			Models: []string{"*"},
			Pipeline: []Step{
				{Engine: EngineSessionDedup},
				{Engine: EngineLite, Options: StepOptions{
					CompressToolResults:    boolPtr(false),
					PreserveToolWhitespace: boolPtr(true),
				}},
			},
		}},
	}
}

// boolPtr returns a pointer to v, for the optional *bool engine knobs.
func boolPtr(v bool) *bool { return &v }

func knownEngine(name string) bool {
	switch name {
	case EngineSessionDedup, EngineHeadroom, EngineLite, EngineRTK, EngineCaveman,
		EngineMCPAccessibility, EngineRelevance, EngineReadLifecycle,
		EngineProgressiveAging:
		return true
	}
	return false
}

// normalizeEngineLevel resolves a step's level for its engine. Caveman keeps its
// lite/full/ultra ladder. RTK maps those rungs onto its intensity ladder when
// Intensity is unset, and every other engine has no level selector. An unknown
// level is rejected with an error instead of being silently dropped.
func normalizeEngineLevel(engine, level string) (normalizedLevel, intensity string, err error) {
	lvl := strings.ToLower(strings.TrimSpace(level))
	switch engine {
	case EngineCaveman:
		switch lvl {
		case "", "lite":
			return "lite", "", nil
		case "full":
			return "full", "", nil
		case "ultra":
			return "ultra", "", nil
		}
		return "", "", fmt.Errorf("unknown %s level %q", engine, level)
	case EngineRTK:
		switch lvl {
		case "":
			return "", "", nil
		case "lite", "minimal":
			return "", "minimal", nil
		case "full", "standard":
			return "", "standard", nil
		case "ultra", "aggressive":
			return "", "aggressive", nil
		}
		return "", "", fmt.Errorf("unknown %s level %q", engine, level)
	}
	if lvl != "" {
		return "", "", fmt.Errorf("engine %s has no level selector", engine)
	}
	return "", "", nil
}

// sanitizeStepOptions clamps the knobs an engine would misbehave on.
func sanitizeStepOptions(opts StepOptions) StepOptions {
	if opts.MinRows < 0 {
		opts.MinRows = 0
	}
	// Upstream's DEFAULT_MAX_TOOL_LENGTH: non-positive means the default, never
	// "truncate everything".
	if opts.MaxToolLength <= 0 {
		opts.MaxToolLength = liteDefaultMaxToolLength
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
	for _, v := range []*int{&opts.McpMinLength, &opts.McpMaxChars, &opts.McpCollapseThreshold, &opts.McpCollapseKeepHead, &opts.McpCollapseKeepTail} {
		if *v < 0 {
			*v = 0
		}
	}
	// Upstream falls back to the default when maxTextChars is below twice the
	// tail reserve: a smaller head would truncate the entire output.
	if opts.McpMaxChars > 0 && opts.McpMaxChars < mcpMinMaxTextChars {
		opts.McpMaxChars = 0
	}
	// 0 stays the engine's "use the default" sentinel; any other value is a
	// percentage clamped to 0.1..1.
	opts.RelevanceBudgetPercent = clampPercent(opts.RelevanceBudgetPercent)
	opts.RelevanceOverlapThreshold = clampPercent(opts.RelevanceOverlapThreshold)
	return opts
}

// clampPercent keeps 0 (the engine default sentinel) and clamps other
// percentages to 0.1..1.
func clampPercent(v float64) float64 {
	switch {
	case v <= 0:
		return 0
	case v < 0.1:
		return 0.1
	case v > 1:
		return 1
	}
	return v
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
// combo name. A typo degrades to the next best thing, never to a panic.
func (c *Config) Sanitize() {
	if c == nil {
		return
	}
	if c.MinBytes <= 0 {
		c.MinBytes = defaultMinBytes
	}
	if c.HardBudgetTokens < 0 {
		log.Errorf("payloadcompress: hard-budget-tokens must be >= 0, got %d; disabling the cap", c.HardBudgetTokens)
		c.HardBudgetTokens = 0
	}
	exclusions := make([]string, 0, len(c.Exclusions))
	for _, pattern := range c.Exclusions {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			exclusions = append(exclusions, pattern)
		}
	}
	c.Exclusions = exclusions
	switch strings.ToLower(strings.TrimSpace(c.CachePrefix)) {
	case CachePrefixProtect, "preserve":
		c.CachePrefix = CachePrefixProtect
	case CachePrefixRewrite, "compress", "":
		c.CachePrefix = CachePrefixRewrite
	default:
		log.Warnf("payloadcompress: unknown cache-prefix %q, falling back to rewrite", c.CachePrefix)
		c.CachePrefix = CachePrefixRewrite
	}
	switch strings.ToLower(strings.TrimSpace(c.PreserveSystemPrompt)) {
	case PreserveSystemPromptWhenNoCache:
		c.PreserveSystemPrompt = PreserveSystemPromptWhenNoCache
	case PreserveSystemPromptNever:
		c.PreserveSystemPrompt = PreserveSystemPromptNever
	case PreserveSystemPromptAlways, "":
		c.PreserveSystemPrompt = PreserveSystemPromptAlways
	default:
		log.Warnf("payloadcompress: unknown preserve-system-prompt %q, falling back to always", c.PreserveSystemPrompt)
		c.PreserveSystemPrompt = PreserveSystemPromptAlways
	}
	if c.PrefixFreeze.Threshold <= 0 {
		c.PrefixFreeze.Threshold = defaultPrefixFreezeThreshold
	}
	hadCombos := len(c.Combos) > 0
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
			level, intensity, errLevel := normalizeEngineLevel(engine, step.Level)
			if errLevel != nil {
				log.Errorf("payloadcompress: combo %q step %q: %v; using the engine default", name, engine, errLevel)
			}
			options := step.Options
			if options.Intensity == "" && intensity != "" {
				options.Intensity = intensity
			}
			steps = append(steps, Step{
				Engine:  engine,
				Level:   level,
				Options: sanitizeStepOptions(options),
			})
		}
		if len(steps) == 0 {
			continue
		}
		seen[name] = struct{}{}
		combos = append(combos, Combo{Name: name, Models: combo.Models, Pipeline: steps})
	}
	c.Combos = combos
	if len(c.Combos) == 0 && c.Enabled && !hadCombos {
		// Enabled with no pipeline configured at all means the built-in one, not
		// silence. This matches upstream's default plan.
		c.Combos = DefaultConfig().Combos
	}
	// A pipeline the operator did configure but whose engines all resolved away
	// (unknown names) resolves to off: resolve() returns nil and compression is
	// skipped. This is the mapping of upstream 916a464 ("legacy stacked dispatch
	// with zero enabled engines resolves to off").

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
