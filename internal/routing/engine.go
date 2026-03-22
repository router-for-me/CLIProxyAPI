package routing

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// Engine evaluates routing rules against request signals.
type Engine struct {
	mu           sync.RWMutex
	rules        []config.ModelRoutingRule
	enabled      bool
	dryRun       bool
	defaultModel string
}

// NewEngine creates a routing engine from configuration.
func NewEngine(cfg config.ModelRoutingConfig) *Engine {
	e := &Engine{
		enabled:      cfg.Enabled,
		dryRun:       cfg.DryRun,
		defaultModel: cfg.DefaultModel,
	}
	e.SetRulesFromConfig(cfg.Rules)
	return e
}


// SetEnabled toggles the engine on/off.
func (e *Engine) SetEnabled(enabled bool) {
	e.mu.Lock()
	e.enabled = enabled
	e.mu.Unlock()
}

// IsEnabled returns whether the engine is active.
func (e *Engine) IsEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

// IsDryRun returns whether dry-run mode is active.
func (e *Engine) IsDryRun() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dryRun
}

// Evaluate checks the request body against all rules and returns the target model
// if a rule matches. Returns empty string if no rule matches or engine is disabled.
func (e *Engine) Evaluate(rawJSON []byte, modelName string) string {
	e.mu.RLock()
	enabled := e.enabled
	dryRun := e.dryRun
	rules := e.rules
	defaultModel := e.defaultModel
	e.mu.RUnlock()

	if !enabled {
		return ""
	}

	signals := Analyze(rawJSON, modelName)

	for _, rule := range rules {
		if !rule.IsEnabled() {
			continue
		}
		if matchesAllConditions(signals, rule.Conditions) {
			if dryRun {
				log.WithFields(log.Fields{
					"rule":            rule.Name,
					"requested_model": modelName,
					"target_model":    rule.TargetModel,
					"dry_run":         true,
				}).Info("[model-routing] rule matched (dry-run, not applied)")
				return ""
			}
			log.WithFields(log.Fields{
				"rule":            rule.Name,
				"requested_model": modelName,
				"target_model":    rule.TargetModel,
			}).Info("[model-routing] rule matched, routing to target model")
			return rule.TargetModel
		}
	}

	if defaultModel != "" {
		if dryRun {
			log.WithFields(log.Fields{
				"requested_model": modelName,
				"default_model":   defaultModel,
				"dry_run":         true,
			}).Info("[model-routing] no rule matched, would use default (dry-run)")
			return ""
		}
		return defaultModel
	}

	return ""
}

// SetDryRun toggles dry-run mode (thread-safe).
func (e *Engine) SetDryRun(dryRun bool) {
	e.mu.Lock()
	e.dryRun = dryRun
	e.mu.Unlock()
}

// GetRules returns the current rules as JSON.
func (e *Engine) GetRules() json.RawMessage {
	e.mu.RLock()
	rules := make([]config.ModelRoutingRule, len(e.rules))
	copy(rules, e.rules)
	e.mu.RUnlock()
	data, err := json.Marshal(rules)
	if err != nil {
		return json.RawMessage("[]")
	}
	return data
}

// SetRulesFromConfig updates rules from config structs (for builder init).
func (e *Engine) SetRulesFromConfig(rules []config.ModelRoutingRule) {
	sorted := make([]config.ModelRoutingRule, len(rules))
	copy(sorted, rules)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})
	e.mu.Lock()
	e.rules = sorted
	e.mu.Unlock()
}

// SetRules updates the active rules from JSON (for management API).
func (e *Engine) SetRules(raw json.RawMessage) error {
	var rules []config.ModelRoutingRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return err
	}
	e.SetRulesFromConfig(rules)
	return nil
}

// matchesAllConditions checks if all conditions in a rule are satisfied (AND logic).
func matchesAllConditions(signals RequestSignals, conditions []config.ModelRoutingCondition) bool {
	for _, cond := range conditions {
		if !matchCondition(signals, cond) {
			return false
		}
	}
	return len(conditions) > 0 // empty conditions never match
}

func matchCondition(signals RequestSignals, cond config.ModelRoutingCondition) bool {
	switch cond.Type {
	case "message-count":
		return compareInt(signals.MessageCount, cond.Operator, cond.Value)
	case "last-message-length":
		return compareInt(signals.LastUserMessageLen, cond.Operator, cond.Value)
	case "total-message-length":
		return compareInt(signals.TotalMessageLength, cond.Operator, cond.Value)
	case "last-message-contains":
		return matchString(signals.LastUserMessage, cond.Operator, cond.Value)
	case "system-prompt-contains":
		return matchString(signals.SystemPrompt, cond.Operator, cond.Value)
	case "has-tool-blocks":
		return matchBool(signals.HasToolBlocks, cond.Operator, cond.Value)
	case "has-code-blocks":
		return matchBool(signals.HasCodeBlocks, cond.Operator, cond.Value)
	case "requested-model-family":
		family := detectModelFamily(signals.RequestedModel)
		return matchString(family, cond.Operator, cond.Value)
	case "always":
		return true
	default:
		log.WithField("condition_type", cond.Type).Warn("[model-routing] unknown condition type")
		return false
	}
}

func compareInt(actual int, operator, valueStr string) bool {
	expected, err := strconv.Atoi(valueStr)
	if err != nil {
		return false
	}
	switch operator {
	case "equals", "eq", "==":
		return actual == expected
	case "not-equals", "ne", "!=":
		return actual != expected
	case "greater-than", "gt", ">":
		return actual > expected
	case "less-than", "lt", "<":
		return actual < expected
	case "greater-or-equal", "gte", ">=":
		return actual >= expected
	case "less-or-equal", "lte", "<=":
		return actual <= expected
	default:
		return false
	}
}

func matchString(actual, operator, pattern string) bool {
	switch operator {
	case "contains":
		return strings.Contains(strings.ToLower(actual), strings.ToLower(pattern))
	case "not-contains":
		return !strings.Contains(strings.ToLower(actual), strings.ToLower(pattern))
	case "equals", "eq":
		return strings.EqualFold(actual, pattern)
	case "matches", "regex":
		re, err := regexp.Compile(pattern)
		if err != nil {
			log.WithError(err).Warn("[model-routing] invalid regex pattern")
			return false
		}
		return re.MatchString(actual)
	default:
		return false
	}
}

func matchBool(actual bool, operator, valueStr string) bool {
	expected := strings.EqualFold(valueStr, "true")
	switch operator {
	case "equals", "eq", "==", "":
		return actual == expected
	case "not-equals", "ne", "!=":
		return actual != expected
	default:
		return actual == expected
	}
}

// detectModelFamily returns a family string for model compatibility checking.
func detectModelFamily(model string) string {
	lower := strings.ToLower(model)
	claudeKeywords := []string{"claude", "opus", "sonnet", "haiku"}
	for _, kw := range claudeKeywords {
		if strings.Contains(lower, kw) {
			return "claude"
		}
	}
	if strings.HasPrefix(lower, "gpt") || strings.HasPrefix(lower, "o1") ||
		strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o4") {
		return "gpt"
	}
	if strings.Contains(lower, "gemini") {
		return "gemini"
	}
	return "compatible"
}
