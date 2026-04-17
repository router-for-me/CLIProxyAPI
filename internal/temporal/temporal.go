// Package temporal injects a small <temporal_context> XML element into the
// system prompt of outbound LLM requests so long-running agent sessions
// stay grounded in the current wall-clock date and time.
//
// # Opt-in
//
// The feature is disabled by default. Enable it via config.yaml:
//
//	temporal:
//	  enabled: true
//	  inject_interval: 0  # 0 = every request; N = every Nth request
//
// With the block omitted, nothing is injected and behavior is identical
// to the non-temporal build — no surprise request mutation.
//
// # Shape
//
//	<temporal_context>
//	  <temporal day="Friday" date="2026-04-17" time="20:39:59" zone="UTC"
//	            utc="2026-04-17T20:39:59Z" epoch="1776458399" week="16"
//	            day_of_year="107" local_day="Friday" local_date="2026-04-17"
//	            local_time="13:39:59" local_zone="America/Los_Angeles"/>
//	</temporal_context>
//
// # Safety properties
//
//   - No PII: only UTC and local timestamps.
//   - Format-aware: detects Claude (top-level "system") vs OpenAI ("messages")
//     and injects appropriately. No shape-mismatch 400s.
//   - Dedup guard: if the payload already contains a <temporal tag (e.g. from
//     Claude Code's own injection) the hook is a no-op.
//   - Image-model skip: imagen / image-gen models are never modified.
//   - Cloaking-safe: the Claude OAuth executor preserves the block across its
//     system-array rewrite; the Antigravity executor re-applies injection
//     after signature validation.
package temporal

import (
	"bytes"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Config controls temporal injection behavior.
type Config struct {
	Enabled        bool `yaml:"enabled" json:"enabled"`
	InjectInterval int  `yaml:"inject_interval" json:"inject_interval"` // 0 = every request
}

// DefaultConfig returns sensible defaults: disabled.
// Injection alters request payloads, so it is off by default to preserve
// the zero-surprise API behavior community users expect. Opt-in via the
// `temporal` YAML block:
//
//	temporal:
//	  enabled: true
//	  inject_interval: 0  # 0 = every request
func DefaultConfig() Config {
	return Config{Enabled: false, InjectInterval: 0}
}

// ConfigOrDefault resolves an optional *Config to a concrete Config, using
// DefaultConfig() when the pointer is nil (i.e. the YAML block was omitted).
// Helper for executors that need to re-apply injection after validation
// stages strip the conductor-level modification.
func ConfigOrDefault(cfg *Config) Config {
	if cfg == nil {
		return DefaultConfig()
	}
	return *cfg
}

// requestCounter tracks how many requests have been seen for interval-based injection.
var requestCounter uint64

// ShouldInject returns true if a temporal tag should be injected for this request.
// When interval is 0. it injects every time. Otherwise, every Nth request.
// Thread-safe: uses sync/atomic for the counter increment and read.
func ShouldInject(cfg Config) bool {
	if !cfg.Enabled {
		return false
	}
	if cfg.InjectInterval <= 0 {
		return true
	}
	newVal := atomic.AddUint64(&requestCounter, 1)
	return newVal%uint64(cfg.InjectInterval) == 0
}

// BuildTemporalTag generates a temporal XML tag with current date/time metadata.
// Ported from pi-rs/crates/pi-agent/src/system_prompt.rs TemporalDriftDetector pattern.
func BuildTemporalTag(now time.Time) string {
	local := now.Local()
	_, week := now.ISOWeek()
	return fmt.Sprintf(
		`<temporal day="%s" date="%s" time="%s" zone="UTC" utc="%s" epoch="%d" week="%d" day_of_year="%d" local_day="%s" local_date="%s" local_time="%s" local_zone="%s"/>`,
		dayName(now.Weekday()),
		now.Format("2006-01-02"),
		now.Format("15:04:05"),
		now.Format(time.RFC3339),
		now.Unix(),
		week,
		now.YearDay(),
		dayName(local.Weekday()),
		local.Format("2006-01-02"),
		local.Format("15:04:05"),
		local.Location().String(),
	)
}

// IsImageModel returns true if the model name indicates an image generation model
// that uses a different request format where prepending system messages would break
// prompt extraction.
func IsImageModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "imagen") || strings.Contains(lower, "image-gen")
}

// InjectIntoPayload injects temporal context into the request payload.
// It auto-detects the payload format and injects appropriately:
//   - Claude format (top-level "system" field): prepends to the system array
//   - OpenAI format ("messages" array, no top-level "system"): prepends system message
//   - Fallback: returns payload unchanged
// Skips injection for image generation models (imagen, image-gen).
func InjectIntoPayload(payload []byte, model string) []byte {
	if IsImageModel(model) {
		return payload
	}
	// Skip if payload already contains a temporal tag (e.g. from Claude Code's
	// built-in injection). Avoids duplicate/conflicting temporal context.
	if bytes.Contains(payload, []byte("<temporal ")) {
		return payload
	}
	tag := BuildTemporalTag(time.Now().UTC())
	text := fmt.Sprintf("<temporal_context>%s</temporal_context>", tag)

	// Claude format: top-level "system" field exists
	if system := gjson.GetBytes(payload, "system"); system.Exists() {
		return injectIntoClaudeSystem(payload, system, text)
	}

	// OpenAI format: messages array exists
	if messages := gjson.GetBytes(payload, "messages"); messages.IsArray() {
		return injectIntoMessages(payload, messages, text)
	}

	return payload
}

// injectIntoClaudeSystem prepends a temporal text block to the Claude "system" field.
// Handles both array and scalar (string) system values.
func injectIntoClaudeSystem(payload []byte, system gjson.Result, text string) []byte {
	temporalBlock := map[string]any{
		"type": "text",
		"text": text,
	}
	var newSystem []any
	newSystem = append(newSystem, temporalBlock)

	if system.IsArray() {
		// Modern Claude format: system is an array of content blocks
		system.ForEach(func(_, v gjson.Result) bool {
			newSystem = append(newSystem, v.Value())
			return true
		})
	} else {
		// Legacy Claude format: system is a plain string
		// Convert to structured format so all blocks are homogeneous
		newSystem = append(newSystem, map[string]any{
			"type": "text",
			"text": system.String(),
		})
	}

	result, err := sjson.SetBytes(payload, "system", newSystem)
	if err != nil {
		return payload
	}
	return result
}

func injectIntoMessages(payload []byte, messages gjson.Result, text string) []byte {
	// Use plain string content for broad compatibility.
	// Some providers reject structured content arrays in system messages.
	temporalMsg := map[string]any{
		"role":    "system",
		"content": text,
	}
	var newMessages []any
	newMessages = append(newMessages, temporalMsg)
	messages.ForEach(func(_, v gjson.Result) bool {
		newMessages = append(newMessages, v.Value())
		return true
	})
	result, err := sjson.SetBytes(payload, "messages", newMessages)
	if err != nil {
		return payload
	}
	return result
}

func dayName(w time.Weekday) string {
	names := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	return names[w]
}
