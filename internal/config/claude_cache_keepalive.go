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

const (
	defaultClaudeCodeKeepaliveBeforeExpiry    = 5 * time.Minute
	defaultClaudeCodeKeepaliveAgentIdleWindow = 10 * time.Minute
	defaultClaudeCodeKeepaliveMaxProbes       = 6
	defaultClaudeCodeKeepaliveMaxTokens       = 1
)

// ClaudeCodeCacheKeepaliveConfig configures agent-aware prompt-cache keepalive
// probes for Claude Code sessions that requested the 1h cache pool.
//
// The feature is opt-in: a zero-value config keeps it disabled. Every other
// field falls back to the documented default when the operator omits it, so a
// config that only sets `enabled: true` is a complete configuration.
type ClaudeCodeCacheKeepaliveConfig struct {
	// Enabled turns the keepalive scheduler on. Default false.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// BeforeExpiry fires the probe at t_start + ttl - BeforeExpiry. Default 5m.
	BeforeExpiry time.Duration `yaml:"before-expiry" json:"before-expiry"`

	// OnlyWhenAgentsActive gates every probe on the liveness check. Default true.
	OnlyWhenAgentsActive bool `yaml:"only-when-agents-active" json:"only-when-agents-active"`

	// Liveness selects the liveness strategy. Default "claude-code-tasks".
	Liveness string `yaml:"liveness" json:"liveness"`

	// AgentIdleWindow is how long an agent may be silent and still count as
	// running. Default 10m. It is deliberately not the cache TTL: an agent that
	// has written nothing for an hour is finished, not busy.
	AgentIdleWindow time.Duration `yaml:"agent-idle-window" json:"agent-idle-window"`

	// MaxProbes caps consecutive probes without an intervening real request. Default 6.
	MaxProbes int `yaml:"max-probes" json:"max-probes"`

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
		OnlyWhenAgentsActive bool          `yaml:"only-when-agents-active"`
		Liveness             string        `yaml:"liveness"`
		AgentIdleWindow      time.Duration `yaml:"agent-idle-window"`
		MaxProbes            int           `yaml:"max-probes"`
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
		OnlyWhenAgentsActive:        raw.OnlyWhenAgentsActive,
		Liveness:                    strings.TrimSpace(raw.Liveness),
		AgentIdleWindow:             raw.AgentIdleWindow,
		MaxProbes:                   raw.MaxProbes,
		MaxTokens:                   raw.MaxTokens,
		TaskStateDirs:               raw.TaskStateDirs,
		TaskOutputDirs:              raw.TaskOutputDirs,
		enabledPresent:              claudeCodeKeepaliveFieldPresent(value, "enabled"),
		beforeExpiryPresent:         claudeCodeKeepaliveFieldPresent(value, "before-expiry"),
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
