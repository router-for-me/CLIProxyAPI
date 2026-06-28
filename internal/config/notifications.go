package config

import "strings"

const (
	DefaultNotificationWebhookAdapter        = "generic"
	DefaultNotificationWebhookTimeoutSeconds = 5
	MaxNotificationWebhookTimeoutSeconds     = 60
	MaxNotificationWebhookRetry              = 5
)

// NotificationsConfig holds optional outbound notification settings.
type NotificationsConfig struct {
	// Webhooks receives configured notification events.
	Webhooks []NotificationWebhookConfig `yaml:"webhooks" json:"webhooks"`
}

// NotificationWebhookConfig configures one best-effort HTTP webhook sink.
type NotificationWebhookConfig struct {
	Name           string   `yaml:"name,omitempty" json:"name,omitempty"`
	URL            string   `yaml:"url" json:"url"`
	Adapter        string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	Format         string   `yaml:"format,omitempty" json:"format,omitempty"` // Legacy alias for adapter.
	Target         string   `yaml:"target,omitempty" json:"target,omitempty"`
	Disabled       bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Events         []string `yaml:"events,omitempty" json:"events,omitempty"`
	Providers      []string `yaml:"providers,omitempty" json:"providers,omitempty"`
	StatusCodes    []int    `yaml:"status-codes,omitempty" json:"status-codes,omitempty"`
	TimeoutSeconds int      `yaml:"timeout-seconds,omitempty" json:"timeout-seconds,omitempty"`
	Retry          int      `yaml:"retry,omitempty" json:"retry,omitempty"`
	DedupeSeconds  int      `yaml:"dedupe-seconds,omitempty" json:"dedupe-seconds,omitempty"`
}

// NormalizeNotificationsConfig applies safe defaults and clamps to notification settings.
func (c *Config) NormalizeNotificationsConfig() {
	if c == nil {
		return
	}
	for i := range c.Notifications.Webhooks {
		hook := &c.Notifications.Webhooks[i]
		hook.Name = strings.TrimSpace(hook.Name)
		hook.URL = strings.TrimSpace(hook.URL)
		hook.Target = strings.TrimSpace(hook.Target)
		hook.Adapter = strings.ToLower(strings.TrimSpace(hook.Adapter))
		hook.Format = strings.ToLower(strings.TrimSpace(hook.Format))
		if hook.Adapter == "" {
			hook.Adapter = hook.Format
		}
		if hook.Adapter == "" {
			hook.Adapter = DefaultNotificationWebhookAdapter
		}
		if hook.TimeoutSeconds <= 0 {
			hook.TimeoutSeconds = DefaultNotificationWebhookTimeoutSeconds
		} else if hook.TimeoutSeconds > MaxNotificationWebhookTimeoutSeconds {
			hook.TimeoutSeconds = MaxNotificationWebhookTimeoutSeconds
		}
		if hook.Retry < 0 {
			hook.Retry = 0
		} else if hook.Retry > MaxNotificationWebhookRetry {
			hook.Retry = MaxNotificationWebhookRetry
		}
		if hook.DedupeSeconds < 0 {
			hook.DedupeSeconds = 0
		}
		normalizedEvents := hook.Events[:0]
		for _, event := range hook.Events {
			event = strings.ToLower(strings.TrimSpace(event))
			if event != "" {
				normalizedEvents = append(normalizedEvents, event)
			}
		}
		hook.Events = normalizedEvents
		normalizedProviders := hook.Providers[:0]
		for _, provider := range hook.Providers {
			provider = strings.ToLower(strings.TrimSpace(provider))
			if provider != "" {
				normalizedProviders = append(normalizedProviders, provider)
			}
		}
		hook.Providers = normalizedProviders
		normalizedCodes := hook.StatusCodes[:0]
		for _, code := range hook.StatusCodes {
			if code > 0 {
				normalizedCodes = append(normalizedCodes, code)
			}
		}
		hook.StatusCodes = normalizedCodes
	}
}
