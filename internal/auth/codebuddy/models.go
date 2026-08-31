package codebuddy

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// nonModelEntries are placeholder entries in the /v3/config model lists that
// do not refer to a concrete model and must not be exposed as routable models.
var nonModelEntries = map[string]bool{
	"auto": true,
}

// IsRoutableModel reports whether the given model ID refers to a concrete
// routable model rather than a placeholder entry such as "auto".
func IsRoutableModel(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && !nonModelEntries[id]
}

// ConfigModel is a single entry in the top-level models list of /v3/config.
type ConfigModel struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Credits            string `json:"credits"`
	MaxInputTokens     int    `json:"maxInputTokens"`
	MaxOutputTokens    int    `json:"maxOutputTokens"`
	SupportsImages     bool   `json:"supportsImages"`
	SupportsReasoning  bool   `json:"supportsReasoning"`
	SupportsToolCall   bool   `json:"supportsToolCall"`
	DisabledMultimodal bool   `json:"disabledMultimodal"`
}

// ConfigAgent is an agent entry whose models list also contains usable models.
type ConfigAgent struct {
	Name   string   `json:"name"`
	AsTool bool     `json:"asTool"`
	Models []string `json:"models"`
}

// configResponse is the relevant subset of the /v3/config payload.
type configResponse struct {
	Agents []ConfigAgent `json:"agents"`
	Models []ConfigModel `json:"models"`
}

// ModelInfo is a parsed CodeBuddy model with billing and context metadata.
type ModelInfo struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Credits           string  `json:"credits,omitempty"`
	CreditMultiplier  float64 `json:"credit_multiplier,omitempty"`
	MaxInputTokens    int     `json:"max_input_tokens,omitempty"`
	MaxOutputTokens   int     `json:"max_output_tokens,omitempty"`
	SupportsImages    bool    `json:"supports_images,omitempty"`
	SupportsReasoning bool    `json:"supports_reasoning,omitempty"`
	SupportsToolCall  bool    `json:"supports_tool_call,omitempty"`
}

// parseCreditMultiplier converts a "x2.20 credits" style string into a float.
// Empty, zero, or unparsable values return 0.
func parseCreditMultiplier(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "x")
	s = strings.TrimSuffix(s, "credits")
	s = strings.TrimSuffix(s, "credit")
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseConfigBody decodes a /v3/config data payload, tolerating both the plain
// {models, agents} shape and a {"data": {...}} wrapped shape.
func parseConfigBody(configBody []byte) (*configResponse, error) {
	if len(configBody) == 0 {
		return nil, nil
	}
	var cfg configResponse
	if err := json.Unmarshal(configBody, &cfg); err != nil {
		return nil, err
	}
	if len(cfg.Agents) == 0 && len(cfg.Models) == 0 {
		var wrapped struct {
			Data configResponse `json:"data"`
		}
		if err := json.Unmarshal(configBody, &wrapped); err == nil {
			cfg = wrapped.Data
		}
	}
	return &cfg, nil
}

// ParseModels parses the full model catalog (with billing and context
// metadata) from a /v3/config data payload.
func ParseModels(configBody []byte) ([]ModelInfo, error) {
	cfg, err := parseConfigBody(configBody)
	if err != nil || cfg == nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out = append(out, ModelInfo{
			ID:                id,
			Name:              m.Name,
			Credits:           m.Credits,
			CreditMultiplier:  parseCreditMultiplier(m.Credits),
			MaxInputTokens:    m.MaxInputTokens,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportsImages:    m.SupportsImages,
			SupportsReasoning: m.SupportsReasoning,
			SupportsToolCall:  m.SupportsToolCall,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ParseEnabledModels parses the routable model IDs from a /v3/config data
// payload. It aggregates the top-level models list and every agent models
// list, drops placeholder entries, deduplicates, and sorts the result.
func ParseEnabledModels(configBody []byte) ([]string, error) {
	cfg, err := parseConfigBody(configBody)
	if err != nil || cfg == nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	models := make([]string, 0)
	addModel := func(name string) {
		if !IsRoutableModel(name) {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		models = append(models, name)
	}
	for _, m := range cfg.Models {
		addModel(m.ID)
	}
	for _, agent := range cfg.Agents {
		for _, m := range agent.Models {
			addModel(m)
		}
	}
	sort.Strings(models)
	return models, nil
}
