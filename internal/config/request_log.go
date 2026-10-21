package config

import (
	"fmt"
	"strings"
	"time"
)

// parseRequestLogUntil parses an RFC3339 or RFC3339Nano timestamp string.
// Non-empty whitespace-only strings fail.
func parseRequestLogUntil(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, trimmed)
}

// ValidateRequestLog validates the request logging configuration settings.
func (c *SDKConfig) ValidateRequestLog() error {
	if c == nil {
		return nil
	}
	if c.RequestLogUntil != "" {
		if _, err := parseRequestLogUntil(c.RequestLogUntil); err != nil {
			return fmt.Errorf("invalid request-log-until: %w", err)
		}
	}
	return nil
}

// RequestLogAdmitted evaluates whether request logging should capture a request
// given the configuration, commercial mode flag, client principal, and evaluation timestamp.
func (c *SDKConfig) RequestLogAdmitted(commercialMode bool, principal string, now time.Time) bool {
	if c == nil || !c.RequestLog || commercialMode {
		return false
	}
	if c.RequestLogUntil != "" {
		until, err := parseRequestLogUntil(c.RequestLogUntil)
		if err != nil || !now.Before(until) {
			return false
		}
	}
	if len(c.RequestLogAPIKeys) > 0 {
		if principal == "" {
			return false
		}
		for _, key := range c.RequestLogAPIKeys {
			if key == principal {
				return true
			}
		}
		return false
	}
	return true
}

// RequestLogCaptureActive reports whether request logging capture is active generally
// (ignoring caller principal allowlists).
func (c *SDKConfig) RequestLogCaptureActive(commercialMode bool, now time.Time) bool {
	if c == nil || !c.RequestLog || commercialMode {
		return false
	}
	if c.RequestLogUntil != "" {
		until, err := parseRequestLogUntil(c.RequestLogUntil)
		if err != nil || !now.Before(until) {
			return false
		}
	}
	return true
}

// ValidateRequestLog validates the request logging configuration settings.
func (cfg *Config) ValidateRequestLog() error {
	if cfg == nil {
		return nil
	}
	return cfg.SDKConfig.ValidateRequestLog()
}

// RequestLogAdmitted evaluates whether request logging should capture a request.
func (cfg *Config) RequestLogAdmitted(principal string, now time.Time) bool {
	if cfg == nil {
		return false
	}
	return cfg.SDKConfig.RequestLogAdmitted(cfg.CommercialMode, principal, now)
}

// RequestLogCaptureActive reports whether request logging capture is generally active.
func (cfg *Config) RequestLogCaptureActive(now time.Time) bool {
	if cfg == nil {
		return false
	}
	return cfg.SDKConfig.RequestLogCaptureActive(cfg.CommercialMode, now)
}
