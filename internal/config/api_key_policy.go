package config

import (
	"fmt"
	"strings"
)

// APIKeyPolicy decorates an existing client API key with optional model restrictions.
type APIKeyPolicy struct {
	APIKey        string   `yaml:"api-key" json:"api-key"`
	Name          string   `yaml:"name,omitempty" json:"name,omitempty"`
	AllowedModels []string `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`
	BlockedModels []string `yaml:"blocked-models,omitempty" json:"blocked-models,omitempty"`
}

// Allowed checks the caller-visible model before provider aliases are resolved.
func (p APIKeyPolicy) Allowed(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if open := strings.LastIndex(model, "("); open >= 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:open])
	}
	for _, pattern := range p.BlockedModels {
		if matchPolicyModel(strings.ToLower(strings.TrimSpace(pattern)), model) {
			return false
		}
	}
	if len(p.AllowedModels) == 0 {
		return true
	}
	for _, pattern := range p.AllowedModels {
		if matchPolicyModel(strings.ToLower(strings.TrimSpace(pattern)), model) {
			return true
		}
	}
	return false
}

// ValidateAPIKeyPolicies rejects ambiguous policies without exposing key values.
func (c *SDKConfig) ValidateAPIKeyPolicies() error {
	seen := make(map[string]struct{}, len(c.APIKeyPolicies))
	for i := range c.APIKeyPolicies {
		policy := &c.APIKeyPolicies[i]
		policy.APIKey = strings.TrimSpace(policy.APIKey)
		policy.Name = strings.TrimSpace(policy.Name)
		if policy.APIKey == "" {
			return fmt.Errorf("access.api-key-policies[%d]: api-key must not be blank", i)
		}
		if _, exists := seen[policy.APIKey]; exists {
			return fmt.Errorf("access.api-key-policies[%d]: duplicate api-key", i)
		}
		seen[policy.APIKey] = struct{}{}
	}
	return nil
}

func matchPolicyModel(pattern, model string) bool {
	if pattern == "" {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return model == pattern
	}
	if !strings.HasPrefix(model, parts[0]) {
		return false
	}
	model = model[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		index := strings.Index(model, part)
		if index < 0 {
			return false
		}
		model = model[index+len(part):]
	}
	return strings.HasSuffix(model, parts[len(parts)-1])
}
