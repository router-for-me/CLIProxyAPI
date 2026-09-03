package config

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Claude Code cache keepalive liveness strategies.
const (
	// ClaudeCodeKeepaliveLivenessClaudeCodeTasks probes only while a Claude Code
	// task belonging to the session is still running.
	ClaudeCodeKeepaliveLivenessClaudeCodeTasks = "claude-code-tasks"
	// ClaudeCodeKeepaliveLivenessAlways skips the liveness check entirely. It is
	// intended for non-Claude-Code clients whose agent state the proxy cannot see.
	ClaudeCodeKeepaliveLivenessAlways = "always"
)

// Claude Code cache keepalive 5m-pool probing modes.
const (
	// ClaudeCodeKeepaliveProbe5mAuto probes a 5m session only when the request
	// model's cache reads are cheap enough for the arithmetic to work out.
	ClaudeCodeKeepaliveProbe5mAuto = "auto"
	// ClaudeCodeKeepaliveProbe5mAlways probes every confirmed session.
	ClaudeCodeKeepaliveProbe5mAlways = "always"
	// ClaudeCodeKeepaliveProbe5mNever probes only 1h sessions.
	ClaudeCodeKeepaliveProbe5mNever = "never"
)

const (
	defaultClaudeCodeKeepaliveBeforeExpiry    = 5 * time.Minute
	defaultClaudeCodeKeepaliveAgentIdleWindow = 10 * time.Minute
	defaultClaudeCodeKeepaliveMaxProbes       = 6
	defaultClaudeCodeKeepaliveMaxTokens       = 1

	// defaultClaudeCodeKeepaliveBeforeExpiry5m is the lead time on the 5m pool.
	// The TTL clock runs from the start of the request that wrote the entry and
	// generation time counts against it, so the margin has to cover a slow turn
	// plus the probe's own round trip while still leaving the probe inside the
	// window.
	defaultClaudeCodeKeepaliveBeforeExpiry5m = 45 * time.Second
	// defaultClaudeCodeKeepaliveMaxProbes5m covers about two hours of idle. The
	// 1h default of 6 probes would cover only 25 minutes on this pool, which is
	// shorter than the subagent waits the feature exists for.
	defaultClaudeCodeKeepaliveMaxProbes5m = 30
)

// ClaudeCodeCacheKeepaliveConfig configures agent-aware prompt-cache keepalive
// probes for Claude Code sessions.
//
// The feature is opt-in: a zero-value config keeps it disabled. Every other
// field falls back to the documented default when the operator omits it, so a
// config that only sets `enabled: true` is a complete configuration.
type ClaudeCodeCacheKeepaliveConfig struct {
	// Enabled turns the keepalive scheduler on. Default false.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// BeforeExpiry fires the probe at t_start + ttl - BeforeExpiry on the 1h
	// pool. Default 5m.
	BeforeExpiry time.Duration `yaml:"before-expiry" json:"before-expiry"`

	// BeforeExpiry5m is the same lead time for the 5m pool. Default 45s.
	BeforeExpiry5m time.Duration `yaml:"before-expiry-5m" json:"before-expiry-5m"`

	// Probe5m selects when a session on the 5m pool is probed: "auto"
	// (default), "always" or "never". "auto" probes only models whose cache
	// reads are priced low enough for probing to beat expiry.
	Probe5m string `yaml:"probe-5m" json:"probe-5m"`

	// Probe5mModels overrides the built-in cheap-cache-read model list "auto"
	// matches against. Entries match case-insensitively as substrings. Empty
	// means the built-in list.
	Probe5mModels []string `yaml:"probe-5m-models" json:"probe-5m-models,omitempty"`

	// OnlyWhenAgentsActive gates every probe on the liveness check. Default true.
	OnlyWhenAgentsActive bool `yaml:"only-when-agents-active" json:"only-when-agents-active"`

	// Liveness selects the liveness strategy. Default "claude-code-tasks".
	Liveness string `yaml:"liveness" json:"liveness"`

	// AgentIdleWindow is how long an agent may be silent and still count as
	// running. Default 10m. It is deliberately not the cache TTL: an agent that
	// has written nothing for an hour is finished, not busy.
	AgentIdleWindow time.Duration `yaml:"agent-idle-window" json:"agent-idle-window"`

	// MaxProbes caps consecutive probes without an intervening real request on
	// the 1h pool. Default 6.
	MaxProbes int `yaml:"max-probes" json:"max-probes"`

	// MaxProbes5m is the same cap for the 5m pool, where each probe buys only
	// one short window. Default 30, about two hours of idle.
	MaxProbes5m int `yaml:"max-probes-5m" json:"max-probes-5m"`

	// MaxTokens is the max_tokens the probe body carries. Default 1.
	MaxTokens int `yaml:"max-tokens" json:"max-tokens"`

	// TaskStateDirs are the roots searched for Claude Code task state JSON files.
	// Empty means the built-in default (~/.claude/tasks).
	TaskStateDirs []string `yaml:"task-state-dirs" json:"task-state-dirs,omitempty"`

	// TaskOutputDirs are the roots searched for Claude Code task output files.
	// Empty means the built-in default (/private/tmp/claude-<uid>).
	TaskOutputDirs []string `yaml:"task-output-dirs" json:"task-output-dirs,omitempty"`

	enabledPresent              bool
	beforeExpiryPresent         bool
	beforeExpiry5mPresent       bool
	probe5mPresent              bool
	maxProbes5mPresent          bool
	onlyWhenAgentsActivePresent bool
	livenessPresent             bool
	agentIdleWindowPresent      bool
	maxProbesPresent            bool
	maxTokensPresent            bool
}

// UnmarshalYAML preserves field presence so an explicitly configured false or
// zero is not silently replaced by the default.
func (c *ClaudeCodeCacheKeepaliveConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawClaudeCodeCacheKeepaliveConfig struct {
		Enabled              bool          `yaml:"enabled"`
		BeforeExpiry         time.Duration `yaml:"before-expiry"`
		BeforeExpiry5m       time.Duration `yaml:"before-expiry-5m"`
		Probe5m              string        `yaml:"probe-5m"`
		Probe5mModels        []string      `yaml:"probe-5m-models"`
		OnlyWhenAgentsActive bool          `yaml:"only-when-agents-active"`
		Liveness             string        `yaml:"liveness"`
		AgentIdleWindow      time.Duration `yaml:"agent-idle-window"`
		MaxProbes            int           `yaml:"max-probes"`
		MaxProbes5m          int           `yaml:"max-probes-5m"`
		MaxTokens            int           `yaml:"max-tokens"`
		TaskStateDirs        []string      `yaml:"task-state-dirs"`
		TaskOutputDirs       []string      `yaml:"task-output-dirs"`
	}

	var raw rawClaudeCodeCacheKeepaliveConfig
	if errDecode := value.Decode(&raw); errDecode != nil {
		return errDecode
	}

	*c = ClaudeCodeCacheKeepaliveConfig{
		Enabled:                     raw.Enabled,
		BeforeExpiry:                raw.BeforeExpiry,
		BeforeExpiry5m:              raw.BeforeExpiry5m,
		Probe5m:                     strings.TrimSpace(raw.Probe5m),
		Probe5mModels:               raw.Probe5mModels,
		OnlyWhenAgentsActive:        raw.OnlyWhenAgentsActive,
		Liveness:                    strings.TrimSpace(raw.Liveness),
		AgentIdleWindow:             raw.AgentIdleWindow,
		MaxProbes:                   raw.MaxProbes,
		MaxProbes5m:                 raw.MaxProbes5m,
		MaxTokens:                   raw.MaxTokens,
		TaskStateDirs:               raw.TaskStateDirs,
		TaskOutputDirs:              raw.TaskOutputDirs,
		enabledPresent:              claudeCodeKeepaliveFieldPresent(value, "enabled"),
		beforeExpiryPresent:         claudeCodeKeepaliveFieldPresent(value, "before-expiry"),
		beforeExpiry5mPresent:       claudeCodeKeepaliveFieldPresent(value, "before-expiry-5m"),
		probe5mPresent:              claudeCodeKeepaliveFieldPresent(value, "probe-5m"),
		maxProbes5mPresent:          claudeCodeKeepaliveFieldPresent(value, "max-probes-5m"),
		onlyWhenAgentsActivePresent: claudeCodeKeepaliveFieldPresent(value, "only-when-agents-active"),
		livenessPresent:             claudeCodeKeepaliveFieldPresent(value, "liveness"),
		agentIdleWindowPresent:      claudeCodeKeepaliveFieldPresent(value, "agent-idle-window"),
		maxProbesPresent:            claudeCodeKeepaliveFieldPresent(value, "max-probes"),
		maxTokensPresent:            claudeCodeKeepaliveFieldPresent(value, "max-tokens"),
	}
	return nil
}

func claudeCodeKeepaliveFieldPresent(value *yaml.Node, field string) bool {
	if value == nil || value.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(value.Content); index += 2 {
		if value.Content[index].Value == field {
			return true
		}
	}
	return false
}

// WithDefaults fills in every field the operator left out.
func (c ClaudeCodeCacheKeepaliveConfig) WithDefaults() ClaudeCodeCacheKeepaliveConfig {
	if !c.beforeExpiryPresent && c.BeforeExpiry == 0 {
		c.BeforeExpiry = defaultClaudeCodeKeepaliveBeforeExpiry
	}
	if !c.beforeExpiry5mPresent && c.BeforeExpiry5m == 0 {
		c.BeforeExpiry5m = defaultClaudeCodeKeepaliveBeforeExpiry5m
	}
	if !c.probe5mPresent && strings.TrimSpace(c.Probe5m) == "" {
		c.Probe5m = ClaudeCodeKeepaliveProbe5mAuto
	}
	c.Probe5m = strings.ToLower(strings.TrimSpace(c.Probe5m))
	if c.Probe5m == "" {
		c.Probe5m = ClaudeCodeKeepaliveProbe5mAuto
	}
	if !c.onlyWhenAgentsActivePresent {
		c.OnlyWhenAgentsActive = true
	}
	if !c.livenessPresent && strings.TrimSpace(c.Liveness) == "" {
		c.Liveness = ClaudeCodeKeepaliveLivenessClaudeCodeTasks
	}
	c.Liveness = strings.ToLower(strings.TrimSpace(c.Liveness))
	if c.Liveness == "" {
		c.Liveness = ClaudeCodeKeepaliveLivenessClaudeCodeTasks
	}
	if !c.agentIdleWindowPresent && c.AgentIdleWindow == 0 {
		c.AgentIdleWindow = defaultClaudeCodeKeepaliveAgentIdleWindow
	}
	if !c.maxProbesPresent && c.MaxProbes == 0 {
		c.MaxProbes = defaultClaudeCodeKeepaliveMaxProbes
	}
	if !c.maxProbes5mPresent && c.MaxProbes5m == 0 {
		c.MaxProbes5m = defaultClaudeCodeKeepaliveMaxProbes5m
	}
	if !c.maxTokensPresent && c.MaxTokens == 0 {
		c.MaxTokens = defaultClaudeCodeKeepaliveMaxTokens
	}
	return c
}

// Validate rejects a configuration that would schedule probes that cannot help.
func (c ClaudeCodeCacheKeepaliveConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.BeforeExpiry <= 0 {
		return fmt.Errorf("claude-code.cache-keepalive.before-expiry must be positive")
	}
	switch c.Probe5m {
	case ClaudeCodeKeepaliveProbe5mAuto, ClaudeCodeKeepaliveProbe5mAlways, ClaudeCodeKeepaliveProbe5mNever:
	default:
		return fmt.Errorf("claude-code.cache-keepalive.probe-5m must be %q, %q or %q, got %q",
			ClaudeCodeKeepaliveProbe5mAuto, ClaudeCodeKeepaliveProbe5mAlways, ClaudeCodeKeepaliveProbe5mNever, c.Probe5m)
	}
	if c.Probe5m != ClaudeCodeKeepaliveProbe5mNever {
		// A lead time at or above the 5m TTL leaves no window to schedule in, so
		// every 5m session would be silently dropped.
		if c.BeforeExpiry5m <= 0 || c.BeforeExpiry5m >= 5*time.Minute {
			return fmt.Errorf("claude-code.cache-keepalive.before-expiry-5m must be positive and below 5m, got %s", c.BeforeExpiry5m)
		}
		if c.MaxProbes5m <= 0 {
			return fmt.Errorf("claude-code.cache-keepalive.max-probes-5m must be positive")
		}
	}
	switch c.Liveness {
	case ClaudeCodeKeepaliveLivenessClaudeCodeTasks, ClaudeCodeKeepaliveLivenessAlways:
	default:
		return fmt.Errorf("claude-code.cache-keepalive.liveness must be %q or %q, got %q",
			ClaudeCodeKeepaliveLivenessClaudeCodeTasks, ClaudeCodeKeepaliveLivenessAlways, c.Liveness)
	}
	if c.AgentIdleWindow <= 0 && c.Liveness == ClaudeCodeKeepaliveLivenessClaudeCodeTasks {
		return fmt.Errorf("claude-code.cache-keepalive.agent-idle-window must be positive")
	}
	if c.MaxProbes <= 0 {
		return fmt.Errorf("claude-code.cache-keepalive.max-probes must be positive")
	}
	if c.MaxTokens <= 0 {
		return fmt.Errorf("claude-code.cache-keepalive.max-tokens must be positive")
	}
	return nil
}
