package config

import (
	"fmt"
	"strings"
)

const (
	// QuotaCooldownResponseModeRetryable answers an exhausted credential pool
	// with the retryable 429 and Retry-After. It is the default.
	QuotaCooldownResponseModeRetryable = "retryable"
	// QuotaCooldownResponseModeUsageLimit answers a pool whose every credential
	// is out of quota until a known reset with a final usage limit.
	QuotaCooldownResponseModeUsageLimit = "usage-limit"
)

// NormalizeQuotaCooldownResponseMode lower-cases QuotaCooldownResponseMode and
// rejects unknown values. Empty means retryable and stays empty, so saving a
// config does not add the key.
func (cfg *Config) NormalizeQuotaCooldownResponseMode() error {
	mode := strings.ToLower(strings.TrimSpace(cfg.QuotaCooldownResponseMode))
	switch mode {
	case "", QuotaCooldownResponseModeRetryable, QuotaCooldownResponseModeUsageLimit:
		cfg.QuotaCooldownResponseMode = mode
		return nil
	}
	return fmt.Errorf("invalid routing.cooldown.quota-response-mode %q: want %q or %q", cfg.QuotaCooldownResponseMode, QuotaCooldownResponseModeRetryable, QuotaCooldownResponseModeUsageLimit)
}
