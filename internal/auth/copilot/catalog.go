package copilot

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	ModelPrefix   = Provider + "/"
	Chat          = "/chat/completions"
	Messages      = "/v1/messages"
	Responses     = "/responses"
	CatalogMaxAge = 2 * time.Hour
)

// The embedded fallbacks reflect the official CLI model catalog and HTTP routes.
// They do not grant entitlement or override account-advertised capabilities.
//
//go:embed compat.json
var compatJSON []byte

var compatibility = func() map[string]Compatibility {
	var entries map[string]Compatibility
	if err := json.Unmarshal(compatJSON, &entries); err != nil {
		panic(fmt.Errorf("copilot: invalid embedded compatibility catalog: %w", err))
	}
	return entries
}()

type Compatibility struct {
	SystemPrompt       string   `json:"system_prompt"`
	Endpoint           string   `json:"endpoint"`
	AdaptiveMode       string   `json:"adaptive_mode"`
	NonStreamingOutput int      `json:"max_non_streaming_output_tokens"`
	LimitEfforts       bool     `json:"limit_efforts"`
	DefaultVerbosity   string   `json:"default_verbosity"`
	DefaultToolChoice  string   `json:"default_tool_choice"`
	DisableSnippy      bool     `json:"disable_snippy"`
	DefaultEffort      string   `json:"default_effort"`
	DefaultBudget      int      `json:"default_budget"`
	DefaultMaxTokens   int      `json:"default_max_tokens"`
	AnthropicBetas     []string `json:"anthropic_betas"`
}

type Model struct {
	ID            string   `json:"id"`
	DefaultEffort string   `json:"default_reasoning_effort,omitempty"`
	Name          string   `json:"name"`
	Endpoints     []string `json:"supported_endpoints"`
	Picker        *bool    `json:"model_picker_enabled,omitempty"`
	Policy        struct {
		State string `json:"state,omitempty"`
	} `json:"policy"`
	Capabilities struct {
		Type     string `json:"type,omitempty"`
		Supports struct {
			Tools     *bool           `json:"tool_calls,omitempty"`
			Vision    *bool           `json:"vision,omitempty"`
			Thinking  *bool           `json:"thinking,omitempty"`
			Adaptive  json.RawMessage `json:"adaptive_thinking,omitempty"`
			Reasoning json.RawMessage `json:"reasoning_effort,omitempty"`
			MinBudget int             `json:"min_thinking_budget,omitempty"`
			MaxBudget int             `json:"max_thinking_budget,omitempty"`
		} `json:"supports"`
		Limits struct {
			Context            int `json:"max_context_window_tokens,omitempty"`
			Input              int `json:"max_prompt_tokens,omitempty"`
			Output             int `json:"max_output_tokens,omitempty"`
			NonStreamingOutput int `json:"max_non_streaming_output_tokens,omitempty"`
		} `json:"limits"`
	} `json:"capabilities"`
}

// Catalog is an immutable account snapshot shared by runtime auth clones.
type Catalog struct {
	fetchedAt time.Time
	models    map[string]Model
}

// PresetCatalog is used only while a usable credential awaits its first discovery.
var PresetCatalog = &Catalog{}

// UnavailableCatalog records a revoked account until discovery succeeds again.
var UnavailableCatalog = &Catalog{}

// NewCatalog takes ownership of the nested model fields. The first entry for an ID wins.
func NewCatalog(models []Model, fetchedAt time.Time) *Catalog {
	byID := make(map[string]Model, len(models))
	for _, model := range models {
		if _, exists := byID[model.ID]; !exists {
			byID[model.ID] = model
		}
	}
	return &Catalog{models: byID, fetchedAt: fetchedAt}
}

// FetchModels returns the complete upstream response, including unknown fields.
func FetchModels(ctx context.Context, client *http.Client, base, token string) (json.RawMessage, error) {
	if !TrustedAPI(base) {
		return nil, fmt.Errorf("copilot: untrusted models endpoint")
	}
	var raw json.RawMessage
	if err := RequestJSON(ctx, client, strings.TrimRight(base, "/")+"/models", token, nil, &raw); err != nil {
		return nil, err
	}
	if !ojson.GetBytesNoCopy(raw, "data").IsArray() {
		return nil, fmt.Errorf("copilot: missing models array")
	}
	return raw, nil
}

func FetchCatalog(ctx context.Context, client *http.Client, base, token string) (*Catalog, error) {
	raw, err := FetchModels(ctx, client, base, token)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Models []Model `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("copilot: invalid model directory: %w", err)
	}
	return NewCatalog(payload.Models, time.Now()), nil
}

func (c *Catalog) Active(now time.Time) *Catalog {
	if c == nil || now.Before(c.fetchedAt) || !now.Before(c.ExpiresAt()) {
		return nil
	}
	return c
}

func (c *Catalog) ExpiresAt() time.Time {
	if c == nil || c.fetchedAt.IsZero() {
		return time.Time{}
	}
	return c.fetchedAt.Add(CatalogMaxAge)
}

func (c *Catalog) SameModels(other *Catalog) bool {
	return c == other || (c != nil && other != nil && c != PresetCatalog && other != PresetCatalog && reflect.DeepEqual(c.models, other.models))
}

func (c *Catalog) Routes() []Route {
	if c == nil {
		return nil
	}
	var routes []Route
	for _, preset := range registry.GetCopilotModels() {
		if route, ok := c.resolve(preset); ok {
			routes = append(routes, route)
		}
	}
	return routes
}

func (c *Catalog) Route(modelID string) (Route, bool) {
	if c == nil {
		return Route{}, false
	}
	return c.resolve(registry.LookupStaticModelInfoByChannel(modelID, Provider))
}

// resolve combines a detached preset with the selected account's capabilities.
func (c *Catalog) resolve(preset *registry.ModelInfo) (Route, bool) {
	if preset == nil {
		return Route{}, false
	}
	if c == PresetCatalog {
		return (Model{ID: preset.ID}).route(preset, true)
	}
	model, ok := c.models[preset.ID]
	if !ok {
		return Route{}, false
	}
	return model.route(preset, false)
}

// HomeRoute applies CLI compatibility to the model selected by Home.
func HomeRoute(modelID string, info *registry.ModelInfo) (Route, bool) {
	selected := registry.ModelInfo{ID: modelID}
	// Older Homes send native IDs; newer Homes send canonical provider-qualified IDs.
	if info != nil && strings.TrimPrefix(info.ID, ModelPrefix) == modelID {
		selected = *info
		selected.ID = modelID
		selected.SupportedInputModalities = slices.Clone(info.SupportedInputModalities)
	}
	return (Model{ID: modelID}).route(&selected, true)
}

type Route struct {
	SystemPrompt       string
	Endpoint           string
	Format             translator.Format
	Info               *registry.ModelInfo
	DefaultEffort      string
	NonStreamingOutput int
	DefaultVerbosity   string
	DefaultToolChoice  string
	DisableSnippy      bool
	DefaultBudget      int
	DefaultMaxTokens   int
	AnthropicBetas     []string
}

func (m Model) route(info *registry.ModelInfo, cold bool) (Route, bool) {
	if m.ID == "auto" || (m.Policy.State != "" && m.Policy.State != "enabled") || (m.Picker != nil && !*m.Picker) || (m.Capabilities.Type != "" && m.Capabilities.Type != "chat") {
		return Route{}, false
	}
	profile := compatibility[m.ID]
	endpoints := m.Endpoints
	if endpoints == nil && profile.Endpoint != "" {
		endpoints = []string{profile.Endpoint}
	}
	var route Route
	for _, endpoint := range []string{profile.Endpoint, Responses, Messages, Chat} {
		if !slices.Contains(endpoints, endpoint) {
			continue
		}
		switch endpoint {
		case Chat:
			route.Format = translator.FormatOpenAI
		case Messages:
			route.Format = translator.FormatClaude
		case Responses:
			route.Format = translator.FormatOpenAIResponse
		default:
			continue
		}
		route.Endpoint = endpoint
		break
	}
	if route.Format == "" {
		return Route{}, false
	}
	var presetEfforts []string
	if info.Thinking != nil {
		presetEfforts = info.Thinking.Levels
	}
	if profile.Endpoint != "" && route.Endpoint != profile.Endpoint {
		// These CLI defaults and fallback levels were verified for the preferred protocol.
		profile = Compatibility{}
		presetEfforts = nil
	}
	info.Type, info.OwnedBy, info.Object = Provider, Provider, "model"
	if cold {
		route.NonStreamingOutput = profile.NonStreamingOutput
		for i, modality := range info.SupportedInputModalities {
			info.SupportedInputModalities[i] = strings.ToUpper(modality)
		}
	} else {
		if m.Name != "" {
			info.DisplayName = m.Name
		}
		info.ContextLength, info.InputTokenLimit = m.Capabilities.Limits.Context, m.Capabilities.Limits.Input
		info.OutputTokenLimit, info.MaxCompletionTokens = m.Capabilities.Limits.Output, m.Capabilities.Limits.Output
		info.SupportedInputModalities = []string{"TEXT"}
		if vision := m.Capabilities.Supports.Vision; vision != nil && *vision {
			info.SupportedInputModalities = append(info.SupportedInputModalities, "IMAGE")
		}
		info.Thinking = m.accountThinking(route.Endpoint, profile, presetEfforts)
		route.NonStreamingOutput = m.Capabilities.Limits.NonStreamingOutput
	}
	if thinking := info.Thinking; thinking != nil {
		switch {
		case slices.Contains(thinking.Levels, m.DefaultEffort):
			route.DefaultEffort = m.DefaultEffort
		case slices.Contains(thinking.Levels, profile.DefaultEffort):
			route.DefaultEffort = profile.DefaultEffort
		case len(thinking.Levels) == 0 && profile.DefaultBudget > 0 && thinking.Max > 0:
			route.DefaultBudget = min(max(profile.DefaultBudget, thinking.Min), thinking.Max)
		}
	}
	route.Info = info
	route.SystemPrompt = profile.SystemPrompt
	route.DefaultVerbosity = profile.DefaultVerbosity
	route.DefaultToolChoice = profile.DefaultToolChoice
	route.DisableSnippy = profile.DisableSnippy
	route.DefaultMaxTokens = profile.DefaultMaxTokens
	route.AnthropicBetas = profile.AnthropicBetas
	return route, true
}

func (m Model) accountThinking(endpoint string, profile Compatibility, presetEfforts []string) *registry.ThinkingSupport {
	supports := m.Capabilities.Supports
	minBudget, maxBudget := supports.MinBudget, supports.MaxBudget
	var efforts []string
	if len(supports.Reasoning) > 0 {
		var enabled bool
		if err := json.Unmarshal(supports.Reasoning, &efforts); err != nil && json.Unmarshal(supports.Reasoning, &enabled) == nil {
			if enabled {
				efforts = presetEfforts
				if len(efforts) == 0 {
					efforts = []string{"low", "medium", "high"}
				}
			} else {
				efforts = []string{}
			}
		}
	}
	if efforts == nil {
		efforts = presetEfforts
	} else if profile.LimitEfforts {
		// The CLI accepts only a subset of these accounts' advertised Chat levels.
		efforts = slices.DeleteFunc(slices.Clone(efforts), func(level string) bool { return !slices.Contains(presetEfforts, level) })
	}
	adaptiveMode := profile.AdaptiveMode
	switch string(supports.Adaptive) {
	case `false`, `"unsupported"`:
		adaptiveMode = ""
	case `true`:
		if adaptiveMode == "" {
			adaptiveMode = "optional"
		}
	case `"optional"`, `"required"`, `"adaptive_only"`:
		adaptiveMode = string(supports.Adaptive[1 : len(supports.Adaptive)-1])
	}
	if endpoint == Messages {
		if adaptiveMode != "" && efforts == nil {
			efforts = []string{"low", "medium", "high"}
		}
		if adaptiveMode == "" {
			efforts = nil
		}
	}
	if endpoint == Messages && (adaptiveMode == "required" || adaptiveMode == "adaptive_only") {
		minBudget, maxBudget = 0, 0
	}
	if supports.Thinking == nil || *supports.Thinking {
		if len(efforts) > 0 || (endpoint == Messages && maxBudget > 0) {
			return &registry.ThinkingSupport{Levels: efforts, Min: minBudget, Max: maxBudget, ZeroAllowed: (endpoint == Messages && adaptiveMode != "adaptive_only") || (endpoint != Messages && slices.Contains(efforts, "none"))}
		}
	}
	return nil
}
