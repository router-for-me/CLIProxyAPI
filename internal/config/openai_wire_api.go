package config

import (
	"fmt"
	"strings"
)

// ValidateOpenAICompatibilityWireAPI validates an upstream protocol selector.
// Matching uses the same normalization as SanitizeOpenAICompatibility.
func ValidateOpenAICompatibilityWireAPI(wireAPI string) error {
	switch strings.ToLower(strings.TrimSpace(wireAPI)) {
	case "", "chat-completions", "responses":
		return nil
	default:
		return fmt.Errorf("wire-api must be empty, chat-completions, or responses")
	}
}

// ValidateOpenAICompatibilityWireAPIs validates every provider before config mutation.
func (cfg *Config) ValidateOpenAICompatibilityWireAPIs() error {
	if cfg == nil {
		return nil
	}
	for index := range cfg.OpenAICompatibility {
		if err := ValidateOpenAICompatibilityWireAPI(cfg.OpenAICompatibility[index].WireAPI); err != nil {
			return fmt.Errorf("openai-compatibility[%d].wire-api: %w", index, err)
		}
	}
	return nil
}
