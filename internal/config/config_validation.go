package config

import (
	"bytes"
	"encoding/json"
	"strings"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

// SanitizeThinkingPolicy normalizes thinking effort mapping rules and drops invalid entries.
func (cfg *Config) SanitizeThinkingPolicy() {
	if cfg == nil || len(cfg.Thinking.EffortMapping) == 0 {
		return
	}

	validFrom := map[string]struct{}{
		"none":    {},
		"auto":    {},
		"minimal": {},
		"low":     {},
		"medium":  {},
		"high":    {},
		"xhigh":   {},
		"max":     {},
	}
	rules := make([]ThinkingEffortMappingRule, 0, len(cfg.Thinking.EffortMapping))
	for i := range cfg.Thinking.EffortMapping {
		rule := cfg.Thinking.EffortMapping[i]
		rule.From = strings.ToLower(strings.TrimSpace(rule.From))
		rule.To = strings.ToLower(strings.TrimSpace(rule.To))
		rule.SourceProtocol = strings.ToLower(strings.TrimSpace(rule.SourceProtocol))
		rule.TargetProtocol = strings.ToLower(strings.TrimSpace(rule.TargetProtocol))
		rule.TargetProvider = strings.ToLower(strings.TrimSpace(rule.TargetProvider))
		hadModelScope := len(rule.Models) > 0
		rule.Models = sanitizeThinkingModelPatterns(rule.Models)

		_, validSource := validFrom[rule.From]
		switch {
		case !validSource:
			logThinkingRuleDropped(i, "invalid source effort")
		case rule.To == "":
			logThinkingRuleDropped(i, "empty destination effort")
		case rule.From == rule.To:
			logThinkingRuleDropped(i, "no-op mapping (source equals destination)")
		case hadModelScope && len(rule.Models) == 0:
			logThinkingRuleDropped(i, "empty model scope")
		default:
			rules = append(rules, rule)
		}
	}
	cfg.Thinking.EffortMapping = rules
}

func sanitizeThinkingModelPatterns(models []string) []string {
	if len(models) == 0 {
		return models
	}
	seen := make(map[string]struct{}, len(models))
	normalized := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, model)
	}
	return normalized
}

func logThinkingRuleDropped(index int, reason string) {
	log.WithFields(log.Fields{
		"section":    "thinking.effort-mapping",
		"rule_index": index + 1,
		"reason":     reason,
	}).Warn("thinking effort mapping rule dropped")
}

// SanitizePayloadRules validates raw JSON payload rule params and drops invalid rules.
func (cfg *Config) SanitizePayloadRules() {
	if cfg == nil {
		return
	}
	cfg.Payload.DefaultRaw = sanitizePayloadRawRules(cfg.Payload.DefaultRaw, "default-raw")
	cfg.Payload.OverrideRaw = sanitizePayloadRawRules(cfg.Payload.OverrideRaw, "override-raw")
}

func sanitizePayloadRawRules(rules []PayloadRule, section string) []PayloadRule {
	if len(rules) == 0 {
		return rules
	}
	out := make([]PayloadRule, 0, len(rules))
	for i := range rules {
		rule := rules[i]
		if len(rule.Params) == 0 {
			continue
		}
		invalid := false
		for path, value := range rule.Params {
			raw, ok := payloadRawString(value)
			if !ok {
				continue
			}
			trimmed := bytes.TrimSpace(raw)
			if len(trimmed) == 0 || !json.Valid(trimmed) {
				log.WithFields(log.Fields{
					"section":    section,
					"rule_index": i + 1,
					"param":      path,
				}).Warn("payload rule dropped: invalid raw JSON")
				invalid = true
				break
			}
		}
		if invalid {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func payloadRawString(value any) ([]byte, bool) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		return nil, false
	}
}

// looksLikeBcrypt returns true if the provided string appears to be a bcrypt hash.
func looksLikeBcrypt(s string) bool {
	return len(s) > 4 && (s[:4] == "$2a$" || s[:4] == "$2b$" || s[:4] == "$2y$")
}

// hashSecret hashes the given secret using bcrypt.
func hashSecret(secret string) (string, error) {
	// Use default cost for simplicity.
	hashedBytes, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashedBytes), nil
}
