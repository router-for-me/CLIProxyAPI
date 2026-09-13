package config

import (
	"fmt"
	"strings"
)

// VertexADCConfig configures a Vertex AI credential backed by Application
// Default Credentials (ADC) instead of a service account key or API key.
// On GCE the metadata server is used automatically; elsewhere ADC honors the
// GOOGLE_APPLICATION_CREDENTIALS environment variable.
type VertexADCConfig struct {
	// ProjectID is the Google Cloud project that owns the Vertex AI endpoint.
	// Required: ADC discovers credentials, not the project.
	ProjectID string `yaml:"project-id" json:"project-id"`

	// Location optionally sets the region for Vertex endpoints (e.g. "us-central1"
	// or "global"). Defaults to "us-central1" at request time; note the newest
	// Gemini models are often served only from "global".
	Location string `yaml:"location,omitempty" json:"location,omitempty"`

	// Prefix optionally namespaces model aliases for this credential (e.g. "teamA").
	// This results in model names like "teamA/gemini-2.5-flash".
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// Priority controls selection preference when multiple credentials match.
	// Higher values are preferred; defaults to 0.
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`

	// Weight controls proportional selection under weighted-round-robin.
	// An omitted value defaults to 1; non-positive values exclude this credential.
	Weight *int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// ProxyURL optionally routes this credential's traffic through a proxy.
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}

// ValidateVertexADC rejects malformed vertex-adc entries at config load time,
// before the config is accepted. Synthesis assumes entries are valid: a
// malformed entry reaching the synthesizer is skipped there, which would
// silently drop the credential instead of failing loudly.
func (cfg *Config) ValidateVertexADC() error {
	if cfg == nil {
		return nil
	}
	for index := range cfg.VertexADC {
		entry := &cfg.VertexADC[index]
		if strings.TrimSpace(entry.ProjectID) == "" {
			return fmt.Errorf("vertex-adc[%d]: project-id is required", index)
		}
		if prefix := strings.TrimSpace(entry.Prefix); prefix != "" && strings.Contains(prefix, "/") {
			return fmt.Errorf("vertex-adc[%d]: prefix must be a single segment (no '/' allowed): %q", index, prefix)
		}
	}
	return nil
}
