// Package usage provides Zero Data Retention (ZDR) controls for privacy-sensitive requests.
// This allows routing requests only to providers that do not retain or train on user data.
package usage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DataPolicy represents a provider's data retention policy
type DataPolicy struct {
	Provider       string
	RetainsData    bool // Whether provider retains any data
	TrainsOnData   bool // Whether provider trains models on data
	RetentionPeriod time.Duration // How long data is retained
	Jurisdiction   string // Data processing jurisdiction
	Certifications []string // Compliance certifications (SOC2, HIPAA, etc.)
}

// ZDRConfig configures Zero Data Retention settings
type ZDRConfig struct {
	// RequireZDR requires all requests to use ZDR providers only
	RequireZDR bool
	// PerRequestZDR allows per-request ZDR override
	PerRequestZDR bool
	// AllowedPolicies maps provider names to their data policies
	AllowedPolicies map[string]*DataPolicy
	// DefaultPolicy is used for providers not in AllowedPolicies
	DefaultPolicy *DataPolicy
}

// ZDRRequest specifies ZDR requirements for a request
type ZDRRequest struct {
	// RequireZDR requires ZDR for this specific request
	RequireZDR bool
	// PreferredJurisdiction is the preferred data jurisdiction (e.g., "US", "EU")
	PreferredJurisdiction string
	// RequiredCertifications required compliance certifications
	RequiredCertifications []string
	// ExcludedProviders providers to exclude
	ExcludedProviders []string
	// AllowRetainData allows providers that retain data
	AllowRetainData bool
	// AllowTrainData allows providers that train on data
	AllowTrainData bool
}

// ZDRResult contains the ZDR routing decision
type ZDRResult struct {
	AllowedProviders []string
	BlockedProviders []string
	Reason          string
	AllZDR         bool
}

// ZDRController handles ZDR routing decisions
type ZDRController struct {
	mu       sync.RWMutex
	config   *ZDRConfig
	providerPolicies map[string]*DataPolicy
}

// NewZDRController creates a new ZDR controller
func NewZDRController(config *ZDRConfig) *ZDRController {
	c := &ZDRController{
		config:           config,
		providerPolicies: make(map[string]*DataPolicy),
	}
	
	// Initialize with default policies if provided
	if config != nil && config.AllowedPolicies != nil {
		for provider, policy := range config.AllowedPolicies {
			c.providerPolicies[provider] = policy
		}
	}
	
	// Set defaults for common providers if not configured
	c.initializeDefaultPolicies()
	
	return c
}

// initializeDefaultPolicies sets up known provider policies
func (z *ZDRController) initializeDefaultPolicies() {
	defaults := map[string]*DataPolicy{
		"google": {
			Provider:       "google",
			RetainsData:    true,
			TrainsOnData:   false, // Has ZDR option
			RetentionPeriod: 24 * time.Hour,
			Jurisdiction:   "US",
			Certifications:  []string{"SOC2", "ISO27001"},
		},
		"anthropic": {
			Provider:       "anthropic",
			RetainsData:    true,
			TrainsOnData:   false,
			RetentionPeriod: time.Hour,
			Jurisdiction:   "US",
			Certifications:  []string{"SOC2", "HIPAA"},
		},
		"openai": {
			Provider:       "openai",
			RetainsData:    true,
			TrainsOnData:   true,
			RetentionPeriod: 30 * 24 * time.Hour,
			Jurisdiction:   "US",
			Certifications:  []string{"SOC2"},
		},
		"deepseek": {
			Provider:       "deepseek",
			RetainsData:    true,
			TrainsOnData:   true,
			RetentionPeriod: 90 * 24 * time.Hour,
			Jurisdiction:   "CN",
			Certifications:  []string{},
		},
		"minimax": {
			Provider:       "minimax",
			RetainsData:    true,
			TrainsOnData:   true,
			RetentionPeriod: 30 * 24 * time.Hour,
			Jurisdiction:   "CN",
			Certifications:  []string{},
		},
		"moonshot": {
			Provider:       "moonshot",
			RetainsData:    true,
			TrainsOnData:   true,
			RetentionPeriod: 30 * 24 * time.Hour,
			Jurisdiction:   "CN",
			Certifications:  []string{},
		},
	}
	
	for provider, policy := range defaults {
		if _, ok := z.providerPolicies[provider]; !ok {
			z.providerPolicies[provider] = policy
		}
	}
}

// CheckProviders filters providers based on ZDR requirements
func (z *ZDRController) CheckProviders(ctx context.Context, providers []string, req *ZDRRequest) (*ZDRResult, error) {
	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers specified")
	}

	// Use default request if nil
	if req == nil {
		req = &ZDRRequest{}
	}

	// Check if global ZDR is required
	if z.config != nil && z.config.RequireZDR && !req.RequireZDR {
		req.RequireZDR = true
	}

	var allowed []string
	var blocked []string

	for _, provider := range providers {
		policy := z.getPolicy(provider)
		
		// Check exclusions first
		if isExcluded(provider, req.ExcludedProviders) {
			blocked = append(blocked, provider)
			continue
		}

		// Check ZDR requirements
		if req.RequireZDR {
			if policy == nil || policy.RetainsData || policy.TrainsOnData {
				if !req.AllowRetainData && policy != nil && policy.RetainsData {
					blocked = append(blocked, provider)
					continue
				}
				if !req.AllowTrainData && policy != nil && policy.TrainsOnData {
					blocked = append(blocked, provider)
					continue
				}
			}
		}

		// Check jurisdiction
		if req.PreferredJurisdiction != "" && policy != nil {
			if policy.Jurisdiction != req.PreferredJurisdiction {
				// Not blocked, but deprioritized in real implementation
			}
		}

		// Check certifications
		if len(req.RequiredCertifications) > 0 && policy != nil {
			hasCerts := hasAllCertifications(policy.Certifications, req.RequiredCertifications)
			if !hasCerts {
				blocked = append(blocked, provider)
				continue
			}
		}

		allowed = append(allowed, provider)
	}

	allZDR := true
	for _, p := range allowed {
		policy := z.getPolicy(p)
		if policy == nil || policy.RetainsData || policy.TrainsOnData {
			allZDR = false
			break
		}
	}

	reason := ""
	if len(allowed) == 0 {
		reason = "no providers match ZDR requirements"
	} else if len(blocked) > 0 {
		reason = fmt.Sprintf("%d providers blocked by ZDR requirements", len(blocked))
	} else if allZDR {
		reason = "all providers support ZDR"
	}

	return &ZDRResult{
		AllowedProviders: allowed,
		BlockedProviders: blocked,
		Reason:          reason,
		AllZDR:         allZDR,
	}, nil
}

// getPolicy returns the data policy for a provider
func (z *ZDRController) getPolicy(provider string) *DataPolicy {
	z.mu.RLock()
	defer z.mu.RUnlock()
	
	// Try exact match first
	if policy, ok := z.providerPolicies[provider]; ok {
		return policy
	}
	
	// Try prefix match
	lower := provider
	for p, policy := range z.providerPolicies {
		if len(p) < len(lower) && lower[:len(p)] == p {
			return policy
		}
	}
	
	// Return default if configured
	if z.config != nil && z.config.DefaultPolicy != nil {
		return z.config.DefaultPolicy
	}
	
	return nil
}

// isExcluded checks if a provider is in the exclusion list
func isExcluded(provider string, exclusions []string) bool {
	for _, e := range exclusions {
		if provider == e {
			return true
		}
	}
	return false
}

// hasAllCertifications checks if provider has all required certifications
func hasAllCertifications(providerCerts, required []string) bool {
	certSet := make(map[string]bool)
	for _, c := range providerCerts {
		certSet[c] = true
	}
	for _, r := range required {
		if !certSet[r] {
			return false
		}
	}
	return true
}

// SetPolicy updates the data policy for a provider
func (z *ZDRController) SetPolicy(provider string, policy *DataPolicy) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.providerPolicies[provider] = policy
}

// GetPolicy returns the data policy for a provider
func (z *ZDRController) GetPolicy(provider string) *DataPolicy {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.providerPolicies[provider]
}

// GetAllPolicies returns all configured policies
func (z *ZDRController) GetAllPolicies() map[string]*DataPolicy {
	z.mu.RLock()
	defer z.mu.RUnlock()
	result := make(map[string]*DataPolicy)
	for k, v := range z.providerPolicies {
		result[k] = v
	}
	return result
}

// NewZDRRequest creates a new ZDR request with sensible defaults
func NewZDRRequest() *ZDRRequest {
	return &ZDRRequest{
		RequireZDR:        true,
		AllowRetainData:   false,
		AllowTrainData:    false,
	}
}

// NewZDRConfig creates a new ZDR configuration
func NewZDRConfig() *ZDRConfig {
	return &ZDRConfig{
		RequireZDR:     false,
		PerRequestZDR:  true,
		AllowedPolicies: make(map[string]*DataPolicy),
	}
}
