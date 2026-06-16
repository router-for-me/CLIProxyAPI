package main

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

const (
	// Default Codex model for Claude web_search → Codex Responses (override with codex_model).
	defaultCodexWebSearchModel = "gpt-5.4-mini"
	// Default xAI model for server-side web_search per https://docs.x.ai/developers/tools/web-search
	defaultXAIWebSearchModel = "grok-4.3"
)

// resolveAntigravityWebSearchTargetModel picks an Antigravity model that can run native googleSearch.
// Config antigravity_model wins; otherwise registry.AntigravityWebSearchModelFor(requested) or the
// first available antigravity model with SupportsWebSearch.
func resolveAntigravityWebSearchTargetModel(configured, requested string) string {
	if m := strings.TrimSpace(configured); m != "" {
		return m
	}
	if m := registry.AntigravityWebSearchModelFor(strings.TrimSpace(requested)); m != "" {
		return m
	}
	for _, model := range registry.GetGlobalRegistry().GetAvailableModelsByProvider("antigravity") {
		if model == nil || !model.SupportsWebSearch {
			continue
		}
		if id := strings.TrimSpace(model.ID); id != "" {
			return id
		}
	}
	return ""
}

// resolveCodexWebSearchTargetModel never forwards the client Claude model to Codex.
func resolveCodexWebSearchTargetModel(configured string) string {
	if m := strings.TrimSpace(configured); m != "" {
		return m
	}
	return defaultCodexWebSearchModel
}

// resolveXAIWebSearchTargetModel never forwards the client Claude model to xAI Responses.
func resolveXAIWebSearchTargetModel(configured string) string {
	if m := strings.TrimSpace(configured); m != "" {
		return m
	}
	return defaultXAIWebSearchModel
}
