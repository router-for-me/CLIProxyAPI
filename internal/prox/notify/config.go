// Package notify turns quota changes and ignition problems into operator
// notifications and delivers them through Bark and a generic webhook.
package notify

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Recovery and reminder policies.
const (
	PolicyOff            = "off"
	PolicyAll            = "all"
	PolicyAfterExhausted = "after_exhausted"
	PolicyHasRemaining   = "has_remaining"
)

// Webhook is a generic HTTP notification target.
type Webhook struct {
	URL     string            `yaml:"url" json:"url,omitempty"`
	Method  string            `yaml:"method" json:"method,omitempty"`
	Headers map[string]string `yaml:"headers" json:"headers,omitempty"`
	// Body is a template; {{title}}, {{body}}, {{text}} and {{level}} are
	// replaced (JSON-escaped when the body is JSON). Empty sends a JSON object.
	Body string `yaml:"body" json:"body,omitempty"`
	// SuccessJSON lists top-level response fields that must have these values.
	SuccessJSON map[string]any `yaml:"success_json" json:"success_json,omitempty"`
}

// WindowPolicy sets a policy per window length.
type WindowPolicy struct {
	FiveHour string `yaml:"five_hour" json:"five_hour"`
	Weekly   string `yaml:"weekly" json:"weekly"`
}

// Config is the notify block of plugins.configs.prox.
type Config struct {
	BarkURL   string  `yaml:"bark_url" json:"bark_url,omitempty"`
	BarkGroup string  `yaml:"bark_group" json:"bark_group,omitempty"`
	BarkIcon  string  `yaml:"bark_icon" json:"bark_icon,omitempty"`
	Webhook   Webhook `yaml:"webhook" json:"webhook"`
	// Thresholds are remaining-percent alert levels, highest first.
	Thresholds     []float64    `yaml:"thresholds" json:"thresholds"`
	Recovery       WindowPolicy `yaml:"recovery" json:"recovery"`
	ResetReminder  WindowPolicy `yaml:"reset_reminder" json:"reset_reminder"`
	IgnitionEvents bool         `yaml:"ignition_events" json:"ignition_events"`
	TimeoutSeconds int          `yaml:"timeout_seconds" json:"timeout_seconds"`
	Timezone       string       `yaml:"timezone" json:"timezone,omitempty"`

	loc *time.Location
}

// Default returns notification defaults.
func Default() Config {
	return Config{
		BarkGroup:      "prox",
		Thresholds:     []float64{50, 20, 10},
		Recovery:       WindowPolicy{FiveHour: PolicyAfterExhausted, Weekly: PolicyAll},
		ResetReminder:  WindowPolicy{FiveHour: PolicyOff, Weekly: PolicyHasRemaining},
		IgnitionEvents: true,
		TimeoutSeconds: 20,
	}
}

// Decode reads the notify block from the prox settings YAML.
func Decode(raw []byte) (Config, error) {
	var wrapper struct {
		Notify yaml.Node `yaml:"notify"`
	}
	cfg := Default()
	if len(strings.TrimSpace(string(raw))) != 0 {
		if err := yaml.Unmarshal(raw, &wrapper); err != nil {
			return Config{}, fmt.Errorf("notify: invalid YAML")
		}
		if wrapper.Notify.Kind != 0 {
			if err := wrapper.Notify.Decode(&cfg); err != nil {
				return Config{}, fmt.Errorf("notify: %w", err)
			}
		}
	}
	return cfg, cfg.validate()
}

func (c *Config) validate() error {
	for _, u := range []string{c.BarkURL, c.Webhook.URL} {
		if u == "" {
			continue
		}
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return fmt.Errorf("notify: %q is not an http(s) URL", u)
		}
	}
	c.Webhook.Method = strings.ToUpper(strings.TrimSpace(c.Webhook.Method))
	if c.Webhook.Method == "" {
		c.Webhook.Method = "POST"
	}
	switch c.Webhook.Method {
	case "GET":
		if c.Webhook.Body != "" {
			return fmt.Errorf("notify: webhook body is not allowed with GET")
		}
	case "POST", "PUT":
	default:
		return fmt.Errorf("notify: webhook method must be GET, POST or PUT")
	}
	if len(c.Thresholds) != 3 {
		return fmt.Errorf("notify: thresholds must list three values (notice, low, critical)")
	}
	for i, t := range c.Thresholds {
		if t < 0 || t > 100 || (i > 0 && t > c.Thresholds[i-1]) {
			return fmt.Errorf("notify: thresholds must be within 0-100 and descending")
		}
	}
	for _, p := range []string{c.Recovery.FiveHour, c.Recovery.Weekly} {
		if p != PolicyOff && p != PolicyAll && p != PolicyAfterExhausted {
			return fmt.Errorf("notify: recovery must be off, all or after_exhausted")
		}
	}
	for _, p := range []string{c.ResetReminder.FiveHour, c.ResetReminder.Weekly} {
		if p != PolicyOff && p != PolicyAll && p != PolicyHasRemaining {
			return fmt.Errorf("notify: reset_reminder must be off, all or has_remaining")
		}
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 {
		return fmt.Errorf("notify: timeout_seconds must be 1-120")
	}
	c.loc = time.Local
	if tz := strings.TrimSpace(c.Timezone); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return fmt.Errorf("notify: unknown timezone %q", tz)
		}
		c.loc = loc
	}
	return nil
}

// Enabled reports whether any channel is configured.
func (c Config) Enabled() bool { return c.BarkURL != "" || c.Webhook.URL != "" }

func (c Config) location() *time.Location {
	if c.loc == nil {
		return time.Local
	}
	return c.loc
}
