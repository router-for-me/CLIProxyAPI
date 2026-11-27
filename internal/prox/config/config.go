// Package config decodes and validates the plugin's settings block
// (plugins.configs.kpatdev-quota-router in CPA's config.yaml).
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/regexp"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	PluginID = "prox"

	ModeShadow = "shadow"
	ModeActive = "active"

	StrategyResetFirst     = "reset_first"
	StrategyQuotaPressure  = "quota_pressure"
	StrategyBalanced       = "balanced"
	StrategyManualPriority = "manual_priority"

	ScopeModel    = "model"
	ScopeProvider = "provider"

	RebindConservative = "conservative"
	RebindEager        = "eager"
	RebindNever        = "never"
)

// Config is the validated plugin configuration.
type Config struct {
	Mode                      string            `yaml:"mode" json:"mode"`
	Strategy                  string            `yaml:"strategy" json:"strategy"`
	ManualPriorityBase        string            `yaml:"manual_priority_base" json:"manual_priority_base"`
	PollInterval              time.Duration     `yaml:"poll_interval" json:"-"`
	PassiveSkip               time.Duration     `yaml:"passive_skip" json:"-"`
	PassiveSkipMax            time.Duration     `yaml:"passive_skip_max" json:"-"`
	MaxAge                    time.Duration     `yaml:"max_age" json:"-"`
	RequestTimeout            time.Duration     `yaml:"request_timeout" json:"-"`
	PassiveQuota              bool              `yaml:"passive_quota" json:"passive_quota"`
	AffinityEnabled           bool              `yaml:"affinity_enabled" json:"affinity_enabled"`
	AffinityTTL               time.Duration     `yaml:"affinity_ttl" json:"-"`
	AffinityScope             string            `yaml:"affinity_scope" json:"affinity_scope"`
	AffinityInheritParent     bool              `yaml:"affinity_inherit_parent" json:"affinity_inherit_parent"`
	AffinityMaxSessions       int               `yaml:"affinity_max_sessions" json:"affinity_max_sessions"`
	RebindPolicy              string            `yaml:"rebind_policy" json:"rebind_policy"`
	PressureMinHours          float64           `yaml:"pressure_min_hours" json:"pressure_min_hours"`
	BalancedFiveHourSoftLimit float64           `yaml:"balanced_five_hour_soft_limit" json:"balanced_five_hour_soft_limit"`
	NewSessionMinRemaining    float64           `yaml:"new_session_min_remaining" json:"new_session_min_remaining"`
	ScheduleAcrossPriorities  bool              `yaml:"schedule_across_priorities" json:"schedule_across_priorities"`
	AccountPriority           map[string]int    `yaml:"account_priority" json:"account_priority"`
	AccountAlias              map[string]string `yaml:"account_alias" json:"account_alias"`
	StateDir                  string            `yaml:"state_dir" json:"state_dir"`
	ActivityRetention         int               `yaml:"activity_retention" json:"activity_retention"`
	HistoryRetention          time.Duration     `yaml:"history_retention" json:"-"`
}

// Default returns the configuration used when a key is absent.
func Default() Config {
	return Config{
		Mode:                      ModeShadow,
		Strategy:                  StrategyResetFirst,
		ManualPriorityBase:        StrategyResetFirst,
		PollInterval:              5 * time.Minute,
		PassiveSkip:               time.Minute,
		PassiveSkipMax:            30 * time.Minute,
		MaxAge:                    10 * time.Minute,
		RequestTimeout:            10 * time.Second,
		PassiveQuota:              true,
		AffinityEnabled:           true,
		AffinityTTL:               3 * time.Hour,
		AffinityScope:             ScopeModel,
		AffinityInheritParent:     true,
		AffinityMaxSessions:       20000,
		RebindPolicy:              RebindConservative,
		PressureMinHours:          0.5,
		BalancedFiveHourSoftLimit: 85,
		NewSessionMinRemaining:    5,
		ScheduleAcrossPriorities:  true,
		AccountPriority:           map[string]int{},
		AccountAlias:              map[string]string{},
		StateDir:                  PluginID + "-data",
		ActivityRetention:         1000,
		HistoryRetention:          168 * time.Hour,
	}
}

// FieldError is one invalid setting.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Errors is a list of invalid settings.
type Errors []FieldError

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, f := range e {
		parts[i] = f.Field + ": " + f.Message
	}
	return strings.Join(parts, "; ")
}

// Decode parses YAML over the defaults and validates the result. Keys owned by
// CPA (enabled, priority) and unknown keys are ignored.
func Decode(raw []byte) (Config, error) {
	cfg := Default()
	if len(strings.TrimSpace(string(raw))) != 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return Config{}, Errors{{Field: "", Message: "invalid YAML or value type"}}
		}
	}
	if cfg.AccountPriority == nil {
		cfg.AccountPriority = map[string]int{}
	}
	if cfg.AccountAlias == nil {
		cfg.AccountAlias = map[string]string{}
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		return Config{}, errs
	}
	return cfg, nil
}

// DecodeJSONPatch applies a JSON object of settings (as sent to CPA's config
// PATCH endpoint) over cfg and validates the result.
func DecodeJSONPatch(cfg Config, raw []byte) (Config, error) {
	var patch map[string]any
	if err := json.Unmarshal(raw, &patch); err != nil || patch == nil {
		return Config{}, Errors{{Message: "body must be a JSON object"}}
	}
	current, err := yaml.Marshal(cfg.yamlMap())
	if err != nil {
		return Config{}, Errors{{Message: "internal encoding error"}}
	}
	var merged map[string]any
	_ = yaml.Unmarshal(current, &merged)
	for k, v := range patch {
		if v == nil {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	out, err := yaml.Marshal(merged)
	if err != nil {
		return Config{}, Errors{{Message: "invalid values"}}
	}
	return Decode(out)
}

var aliasPattern = regexp.MustCompile(`^[\p{L}\p{N} ._@+\-()#/]{1,40}$`)

// Validate checks ranges and enumerations.
func (c Config) Validate() Errors {
	var errs Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	oneOf := func(field, value string, allowed ...string) {
		for _, a := range allowed {
			if value == a {
				return
			}
		}
		add(field, "must be one of %s", strings.Join(allowed, ", "))
	}
	oneOf("mode", c.Mode, ModeShadow, ModeActive)
	oneOf("strategy", c.Strategy, StrategyResetFirst, StrategyQuotaPressure, StrategyBalanced, StrategyManualPriority)
	oneOf("manual_priority_base", c.ManualPriorityBase, StrategyResetFirst, StrategyQuotaPressure, StrategyBalanced)
	oneOf("affinity_scope", c.AffinityScope, ScopeModel, ScopeProvider)
	oneOf("rebind_policy", c.RebindPolicy, RebindConservative, RebindEager, RebindNever)
	if c.PollInterval < time.Minute || c.PollInterval > time.Hour {
		add("poll_interval", "must be between 1m and 1h")
	}
	if c.PassiveSkip < 0 || c.PassiveSkip > time.Hour {
		add("passive_skip", "must be between 0s and 1h")
	}
	if c.PassiveSkipMax < time.Minute || c.PassiveSkipMax > 24*time.Hour {
		add("passive_skip_max", "must be between 1m and 24h")
	}
	if c.MaxAge < c.PollInterval || c.MaxAge > time.Hour {
		add("max_age", "must be between poll_interval and 1h")
	}
	if c.RequestTimeout < time.Second || c.RequestTimeout > 30*time.Second {
		add("request_timeout", "must be between 1s and 30s")
	}
	if c.AffinityTTL < 5*time.Minute || c.AffinityTTL > 7*24*time.Hour {
		add("affinity_ttl", "must be between 5m and 168h")
	}
	if c.AffinityMaxSessions < 100 || c.AffinityMaxSessions > 200000 {
		add("affinity_max_sessions", "must be between 100 and 200000")
	}
	if math.IsNaN(c.PressureMinHours) || c.PressureMinHours < 0.1 || c.PressureMinHours > 24 {
		add("pressure_min_hours", "must be between 0.1 and 24")
	}
	if math.IsNaN(c.BalancedFiveHourSoftLimit) || c.BalancedFiveHourSoftLimit < 50 || c.BalancedFiveHourSoftLimit > 100 {
		add("balanced_five_hour_soft_limit", "must be between 50 and 100")
	}
	if math.IsNaN(c.NewSessionMinRemaining) || c.NewSessionMinRemaining < 0 || c.NewSessionMinRemaining > 50 {
		add("new_session_min_remaining", "must be between 0 and 50")
	}
	if c.ActivityRetention < 100 || c.ActivityRetention > 20000 {
		add("activity_retention", "must be between 100 and 20000")
	}
	if c.HistoryRetention < time.Hour || c.HistoryRetention > 720*time.Hour {
		add("history_retention", "must be between 1h and 720h")
	}
	if strings.TrimSpace(c.StateDir) == "" || strings.ContainsRune(c.StateDir, 0) {
		add("state_dir", "must be a non-empty path")
	}
	if len(c.AccountPriority) > 1000 {
		add("account_priority", "at most 1000 entries")
	}
	for id, p := range c.AccountPriority {
		if !validAuthID(id) {
			add("account_priority", "invalid account ID")
		} else if p < -1000 || p > 1000 {
			add("account_priority", "priority for an account must be between -1000 and 1000")
		}
	}
	if len(c.AccountAlias) > 1000 {
		add("account_alias", "at most 1000 entries")
	}
	for id, alias := range c.AccountAlias {
		if !validAuthID(id) {
			add("account_alias", "invalid account ID")
		} else if !aliasPattern.MatchString(alias) {
			add("account_alias", "aliases are 1–40 letters, digits, spaces, or ._@+-()#/")
		}
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Field < errs[j].Field })
	return errs
}

func validAuthID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Generation is a short stable hash of the effective configuration, used by
// the dashboard to confirm CPA reloaded the plugin with new settings.
func (c Config) Generation() string {
	raw, _ := json.Marshal(c.yamlMap())
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:6])
}

// Public is the configuration as shown to the dashboard, with durations as
// Go duration strings.
func (c Config) Public() map[string]any { return c.yamlMap() }

func (c Config) yamlMap() map[string]any {
	priority := map[string]any{}
	for k, v := range c.AccountPriority {
		priority[k] = v
	}
	alias := map[string]any{}
	for k, v := range c.AccountAlias {
		alias[k] = v
	}
	return map[string]any{
		"mode":                          c.Mode,
		"strategy":                      c.Strategy,
		"manual_priority_base":          c.ManualPriorityBase,
		"poll_interval":                 c.PollInterval.String(),
		"passive_skip":                  c.PassiveSkip.String(),
		"passive_skip_max":              c.PassiveSkipMax.String(),
		"max_age":                       c.MaxAge.String(),
		"request_timeout":               c.RequestTimeout.String(),
		"passive_quota":                 c.PassiveQuota,
		"affinity_enabled":              c.AffinityEnabled,
		"affinity_ttl":                  c.AffinityTTL.String(),
		"affinity_scope":                c.AffinityScope,
		"affinity_inherit_parent":       c.AffinityInheritParent,
		"affinity_max_sessions":         c.AffinityMaxSessions,
		"rebind_policy":                 c.RebindPolicy,
		"pressure_min_hours":            c.PressureMinHours,
		"balanced_five_hour_soft_limit": c.BalancedFiveHourSoftLimit,
		"new_session_min_remaining":     c.NewSessionMinRemaining,
		"schedule_across_priorities":    c.ScheduleAcrossPriorities,
		"account_priority":              priority,
		"account_alias":                 alias,
		"state_dir":                     c.StateDir,
		"activity_retention":            c.ActivityRetention,
		"history_retention":             c.HistoryRetention.String(),
	}
}

// Field describes one setting for management clients.
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Enum        []string `json:"enum,omitempty"`
	Min         string   `json:"min,omitempty"`
	Max         string   `json:"max,omitempty"`
	Default     any      `json:"default"`
	Group       string   `json:"group"`
	Description string   `json:"description"`
	// Effect tells the operator what changing the value does to live traffic.
	Effect string `json:"effect"`
	// Restart marks settings applied only after a CPA restart.
	Restart bool `json:"restart,omitempty"`
}

// Schema lists every setting with its range and effect.
func Schema() []Field {
	d := Default()
	return []Field{
		{"mode", "enum", []string{ModeShadow, ModeActive}, "", "", d.Mode, "Routing", "Shadow records proposed decisions without changing traffic. Active routes requests.", "Switching to active starts steering new and bound sessions immediately.", false},
		{"strategy", "enum", []string{StrategyResetFirst, StrategyQuotaPressure, StrategyBalanced, StrategyManualPriority}, "", "", d.Strategy, "Routing", "How new sessions are ranked.", "Affects new sessions and failovers only; bound sessions stay put.", false},
		{"manual_priority_base", "enum", []string{StrategyResetFirst, StrategyQuotaPressure, StrategyBalanced}, "", "", d.ManualPriorityBase, "Routing", "Ranking used after account priority when strategy is manual_priority.", "Affects new sessions and failovers only.", false},
		{"schedule_across_priorities", "boolean", nil, "", "", d.ScheduleAcrossPriorities, "Routing", "Receive candidates from every CPA priority tier so a lower tier is used when a higher tier is exhausted.", "Higher tiers still win while usable.", false},
		{"affinity_enabled", "boolean", nil, "", "", d.AffinityEnabled, "Affinity", "Keep each session on the account it started on.", "Turning off makes every request rank independently; bindings are kept for when it is turned back on.", false},
		{"affinity_ttl", "duration", nil, "5m", "168h", d.AffinityTTL.String(), "Affinity", "Idle time after which a binding expires. Each request renews it.", "Shorter values release idle sessions sooner.", false},
		{"affinity_scope", "enum", []string{ScopeModel, ScopeProvider}, "", "", d.AffinityScope, "Affinity", "Bind per model, or one account per session across all models of a provider.", "Applies to bindings created after the change.", false},
		{"affinity_inherit_parent", "boolean", nil, "", "", d.AffinityInheritParent, "Affinity", "Subagents and forks start on their parent's account.", "Applies to child sessions that are not bound yet.", false},
		{"affinity_max_sessions", "integer", nil, "100", "200000", d.AffinityMaxSessions, "Affinity", "Maximum bindings kept; least recently used are evicted.", "Lowering evicts the oldest bindings.", false},
		{"rebind_policy", "enum", []string{RebindConservative, RebindEager, RebindNever}, "", "", d.RebindPolicy, "Affinity", "Conservative moves a session only on exhaustion, auth failure, or removal. Eager also moves after a temporary failover. Never only moves when the account is removed.", "Affects the next failure of a bound session.", false},
		{"pressure_min_hours", "number", nil, "0.1", "24", d.PressureMinHours, "Policies", "Smallest hours-to-reset divisor in the quota-pressure score.", "Affects quota_pressure ranking.", false},
		{"balanced_five_hour_soft_limit", "number", nil, "50", "100", d.BalancedFiveHourSoftLimit, "Policies", "Five-hour utilization above which balanced ranks an account last.", "Affects balanced ranking.", false},
		{"new_session_min_remaining", "number", nil, "0", "50", d.NewSessionMinRemaining, "Policies", "New sessions skip accounts with less long-window capacity left than this percentage, so a thread does not start on an account that is about to run out. Bound sessions keep using the remainder.", "Affects new sessions and failovers only.", false},
		{"poll_interval", "duration", nil, "1m", "1h", d.PollInterval.String(), "Quota", "How often each account's quota is read.", "Takes effect at each account's next poll.", false},
		{"passive_skip", "duration", nil, "0s", "1h", d.PassiveSkip.String(), "Quota", "Skip regular polls after recent passive quota observations; zero disables.", "Probes and manual refreshes still run.", false},
		{"passive_skip_max", "duration", nil, "1m", "24h", d.PassiveSkipMax.String(), "Quota", "Maximum age of a successful poll before passive skip stops.", "Keeps windows without passive headers fresh.", false},
		{"max_age", "duration", nil, "poll_interval", "1h", d.MaxAge.String(), "Quota", "Quota older than this is stale and not used to rank new sessions.", "Stale accounts stop receiving new sessions until refreshed.", false},
		{"request_timeout", "duration", nil, "1s", "30s", d.RequestTimeout.String(), "Quota", "Timeout for one quota request.", "Next poll.", false},
		{"passive_quota", "boolean", nil, "", "", d.PassiveQuota, "Quota", "Update quota from rate-limit headers on real responses.", "Next response.", false},
		{"activity_retention", "integer", nil, "100", "20000", d.ActivityRetention, "Diagnostics", "Routing decisions kept for the activity view.", "Older decisions are dropped.", false},
		{"history_retention", "duration", nil, "1h", "720h", d.HistoryRetention.String(), "Diagnostics", "How long usage and quota history is kept.", "Older history is dropped.", false},
		{"state_dir", "string", nil, "", "", d.StateDir, "Storage", "Directory for the plugin state file, relative to CPA's working directory.", "Applied after a CPA restart.", true},
	}
}
