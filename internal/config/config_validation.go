package config

import (
	"bytes"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/regexp"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

// SanitizePayloadRules drops invalid rules and skips invalid regex entries individually.
func (cfg *Config) SanitizePayloadRules() {
	if cfg == nil {
		return
	}
	cfg.Payload.DefaultRaw = sanitizePayloadRawRules(cfg.Payload.DefaultRaw, "default-raw")
	cfg.Payload.OverrideRaw = sanitizePayloadRawRules(cfg.Payload.OverrideRaw, "override-raw")
	if len(cfg.Payload.Replace) > 0 {
		valid := make([]PayloadReplaceRule, 0, len(cfg.Payload.Replace))
		for i, rule := range cfg.Payload.Replace {
			if errValidate := rule.Validate(); errValidate != nil {
				log.Warnf("payload.replace rule %d dropped: invalid rule", i+1)
				continue
			}
			replacements := make([]PayloadReplacement, 0, len(rule.Replacements))
			for j, replacement := range rule.Replacements {
				// Validation guarantees that two-key entries are regex replacements.
				if len(replacement) == 2 {
					pattern := replacement["pattern"]
					if _, errCompile := regexp.Compile(pattern); pattern == "" || errCompile != nil {
						log.Warnf("payload.replace rule %d replacement %d skipped: invalid regex", i+1, j+1)
						continue
					}
				}
				replacements = append(replacements, replacement)
			}
			if len(replacements) == 0 {
				continue
			}
			rule.Replacements = replacements
			valid = append(valid, rule)
		}
		cfg.Payload.Replace = valid
	}
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
