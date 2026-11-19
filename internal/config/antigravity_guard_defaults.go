package config

import (
	"net/http"
	"regexp"
	"strings"
)

// Antigravity guard defaults.
//
// Antigravity answers some prompt content with a signal-less 429
// RESOURCE_EXHAUSTED (no details, RetryInfo or Retry-After) that is not quota.
// Treated as quota, it cools the credential down and the same request is
// replayed on the next credentials. Unless the config says otherwise:
//   - such a 429 stops the request on the first credential, without rotation
//     or cooldown;
//   - plugin loading is on and the bundled antigravity-coding-filter plugin is
//     enabled, which answers known and learned flagged prompts locally.
//
// Explicit settings win: `oauth.request-scoped-errors.antigravity: []`,
// `plugins.enabled: false` or `plugins.configs.antigravity-coding-filter.enabled: false`
// turn the respective default off.

const (
	// AntigravityGuardPluginID is the bundled plugin enabled by default.
	AntigravityGuardPluginID = "antigravity-coding-filter"

	antigravityGuardChannel = "antigravity"
	// antigravityContentFilter429Pattern matches a 429 body that ends right
	// after its status, i.e. one without details. Quota 429s carry details.
	antigravityContentFilter429Pattern = `"status":\s*"RESOURCE_EXHAUSTED"\s*\}\s*\}\s*$`
)

var antigravityContentFilter429 = regexp.MustCompile(antigravityContentFilter429Pattern)

// IsAntigravityContentFilter429 reports whether an upstream 429 body is the
// signal-less content-filter rejection matched by the default stop rule.
func IsAntigravityContentFilter429(body string) bool {
	return antigravityContentFilter429.MatchString(body)
}

// applyAntigravityGuardLoadDefaults runs before YAML decoding, so an explicit
// plugins.enabled value overrides it.
func applyAntigravityGuardLoadDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	cfg.Plugins.Enabled = true
}

// applyAntigravityGuardPluginDefault enables the bundled plugin unless the
// config already has an entry for it.
func (cfg *Config) applyAntigravityGuardPluginDefault() {
	if cfg == nil {
		return
	}
	if _, exists := cfg.Plugins.Configs[AntigravityGuardPluginID]; exists {
		return
	}
	if cfg.Plugins.Configs == nil {
		cfg.Plugins.Configs = map[string]PluginInstanceConfig{}
	}
	enabled := true
	cfg.Plugins.Configs[AntigravityGuardPluginID] = PluginInstanceConfig{Enabled: &enabled, Priority: 1}
}

// applyAntigravityGuardScopedErrorDefault adds the stop rule unless the config
// already lists rules, possibly none, for Antigravity.
func (cfg *Config) applyAntigravityGuardScopedErrorDefault() {
	if cfg == nil {
		return
	}
	for channel := range cfg.OAuthRequestScopedErrors {
		if strings.EqualFold(strings.TrimSpace(channel), antigravityGuardChannel) {
			return
		}
	}
	if cfg.OAuthRequestScopedErrors == nil {
		cfg.OAuthRequestScopedErrors = map[string][]RequestScopedErrorRule{}
	}
	cfg.OAuthRequestScopedErrors[antigravityGuardChannel] = []RequestScopedErrorRule{{
		Status:      http.StatusTooManyRequests,
		MatchRegexr: []string{antigravityContentFilter429Pattern},
		Action:      "stop",
	}}
}
