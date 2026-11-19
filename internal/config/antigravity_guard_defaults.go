package config

import (
	"net/http"
	"strings"

	"github.com/MoeForge/moego/regexp"
	"gopkg.in/yaml.v3"
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
// turn the respective default off. Both the synthesized defaults and the empty
// Antigravity list survive config saves.

const (
	// AntigravityGuardPluginID is the bundled plugin enabled by default.
	AntigravityGuardPluginID = "antigravity-coding-filter"

	antigravityGuardChannel = "antigravity"
	// antigravityGuardPluginDefaultYAML is the synthesized plugin entry. It is
	// decoded rather than built field by field so its Raw node is set and a
	// config save writes it out as enabled.
	antigravityGuardPluginDefaultYAML = "enabled: true\npriority: 1\n"
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

// KeepAntigravityGuardOptOut stores an explicitly empty Antigravity rule list
// in out and reports whether channel/rules was one. Sanitizers call it before
// dropping empty channels, so the opt-out from the default stop rule is kept
// in memory and written back by config saves.
func KeepAntigravityGuardOptOut(out map[string][]RequestScopedErrorRule, channel string, rules []RequestScopedErrorRule) bool {
	if out == nil || len(rules) != 0 || !strings.EqualFold(strings.TrimSpace(channel), antigravityGuardChannel) {
		return false
	}
	out[antigravityGuardChannel] = []RequestScopedErrorRule{}
	return true
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
	var plugin PluginInstanceConfig
	if err := yaml.Unmarshal([]byte(antigravityGuardPluginDefaultYAML), &plugin); err != nil {
		return
	}
	if cfg.Plugins.Configs == nil {
		cfg.Plugins.Configs = map[string]PluginInstanceConfig{}
	}
	cfg.Plugins.Configs[AntigravityGuardPluginID] = plugin
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
