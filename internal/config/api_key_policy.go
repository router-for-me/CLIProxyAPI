package config

import (
	"strings"

	log "github.com/sirupsen/logrus"
)

// APIKeyPolicy restricts what a single client access key (access.api-keys) may use.
// Keys without a policy keep the default behavior and may use every credential.
type APIKeyPolicy struct {
	// Key is the client access key this policy applies to.
	Key string `yaml:"key" json:"key"`

	// Name is an optional human readable label shown in the management panel.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Disabled rejects every request authenticated with this key.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`

	// AllowedAuths lists credential auth indexes (auth_index) this key may use.
	// Credential IDs (auth file names) are accepted as well. Empty means all credentials.
	AllowedAuths []string `yaml:"allowed-auths,omitempty" json:"allowed-auths,omitempty"`
}

// SanitizeAPIKeyPolicies trims fields, removes entries without a key and merges
// duplicate entries for the same key (the first entry wins for scalar fields).
func (cfg *Config) SanitizeAPIKeyPolicies() {
	if cfg == nil || len(cfg.APIKeyPolicies) == 0 {
		return
	}
	known := make(map[string]struct{}, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		known[strings.TrimSpace(key)] = struct{}{}
	}
	out := make([]APIKeyPolicy, 0, len(cfg.APIKeyPolicies))
	positions := make(map[string]int, len(cfg.APIKeyPolicies))
	for _, policy := range cfg.APIKeyPolicies {
		policy.Key = strings.TrimSpace(policy.Key)
		if policy.Key == "" {
			continue
		}
		policy.Name = strings.TrimSpace(policy.Name)
		policy.AllowedAuths = normalizePolicyList(policy.AllowedAuths)
		if pos, ok := positions[policy.Key]; ok {
			merged := &out[pos]
			merged.Disabled = merged.Disabled || policy.Disabled
			merged.AllowedAuths = normalizePolicyList(append(merged.AllowedAuths, policy.AllowedAuths...))
			continue
		}
		if _, ok := known[policy.Key]; !ok {
			log.Warn("access.api-key-policies contains a policy for a key that is not listed in access.api-keys")
		}
		positions[policy.Key] = len(out)
		out = append(out, policy)
	}
	cfg.APIKeyPolicies = out
}

// FindAPIKeyPolicy returns the policy configured for key, if any.
func (cfg *SDKConfig) FindAPIKeyPolicy(key string) (APIKeyPolicy, bool) {
	if cfg == nil || key == "" {
		return APIKeyPolicy{}, false
	}
	for _, policy := range cfg.APIKeyPolicies {
		if policy.Key == key {
			return policy, true
		}
	}
	return APIKeyPolicy{}, false
}

func normalizePolicyList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
