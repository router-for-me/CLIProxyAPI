package copilot

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	CatalogKey    = "copilot_catalog"
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
	SystemPrompt      string   `json:"system_prompt"`
	Endpoint          string   `json:"endpoint"`
	AdaptiveMode      string   `json:"adaptive_mode"`
	Efforts           []string `json:"efforts"`
	LimitEfforts      bool     `json:"limit_efforts"`
	DefaultVerbosity  string   `json:"default_verbosity"`
	DefaultToolChoice string   `json:"default_tool_choice"`
	DisableSnippy     bool     `json:"disable_snippy"`
	DefaultEffort     string   `json:"default_effort"`
	DefaultBudget     int      `json:"default_budget"`
	DefaultMaxTokens  int      `json:"default_max_tokens"`
	AnthropicBetas    []string `json:"anthropic_betas"`
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

type Catalog struct {
	FetchedAt time.Time `json:"fetched_at"`
	Models    []Model   `json:"data"`
}

func FetchCatalog(ctx context.Context, client *http.Client, base, token string) (string, error) {
	if !TrustedAPI(base) {
		return "", fmt.Errorf("copilot: untrusted models endpoint")
	}
	var catalog Catalog
	if err := RequestJSON(ctx, client, base+"/models", token, nil, &catalog); err != nil {
		return "", err
	}
	if catalog.Models == nil {
		return "", fmt.Errorf("copilot: missing models array")
	}
	catalog.FetchedAt = time.Now().UTC()
	data, err := json.Marshal(catalog)
	return string(data), err
}

func ReadCatalog(raw string, now time.Time) Catalog {
	var catalog Catalog
	if raw == "" || json.Unmarshal([]byte(raw), &catalog) != nil || catalog.FetchedAt.IsZero() || catalog.FetchedAt.After(now) || !now.Before(catalog.FetchedAt.Add(CatalogMaxAge)) {
		return Catalog{}
	}
	return catalog
}

func (c Catalog) Routes() []Route {
	var routes []Route
	seen := make(map[string]bool, len(c.Models))
	for _, model := range c.Models {
		if model.ID == "" || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		if route, ok := model.route(); ok {
			routes = append(routes, route)
		}
	}
	return routes
}

func (c Catalog) Route(modelID string) (Route, bool) {
	for _, model := range c.Models {
		if model.ID == modelID {
			return model.route()
		}
	}
	return Route{}, false
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

func (m Model) route() (Route, bool) {
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
	if route.Endpoint != profile.Endpoint {
		profile = Compatibility{}
	}
	info := &registry.ModelInfo{ID: m.ID, Object: "model", OwnedBy: Provider, Type: Provider, DisplayName: m.Name, ContextLength: m.Capabilities.Limits.Context, InputTokenLimit: m.Capabilities.Limits.Input, OutputTokenLimit: m.Capabilities.Limits.Output, MaxCompletionTokens: m.Capabilities.Limits.Output}
	supports := m.Capabilities.Supports
	info.SupportedInputModalities = []string{"TEXT"}
	if supports.Vision != nil && *supports.Vision {
		info.SupportedInputModalities = append(info.SupportedInputModalities, "IMAGE")
	}
	var efforts []string
	if len(supports.Reasoning) > 0 {
		var enabled bool
		if err := json.Unmarshal(supports.Reasoning, &efforts); err != nil && json.Unmarshal(supports.Reasoning, &enabled) == nil {
			if enabled {
				efforts = profile.Efforts
				if len(efforts) == 0 {
					efforts = []string{"low", "medium", "high"}
				}
			} else {
				efforts = []string{}
			}
		}
	}
	if efforts == nil {
		efforts = profile.Efforts
	} else if profile.LimitEfforts {
		// The CLI accepts only a subset of these accounts' advertised Chat levels.
		efforts = slices.DeleteFunc(slices.Clone(efforts), func(level string) bool { return !slices.Contains(profile.Efforts, level) })
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
	if route.Endpoint == Messages {
		if adaptiveMode != "" && efforts == nil {
			efforts = []string{"low", "medium", "high"}
		}
		if adaptiveMode == "" {
			efforts = nil
		}
	}
	minBudget, maxBudget := supports.MinBudget, supports.MaxBudget
	if route.Endpoint == Messages && (adaptiveMode == "required" || adaptiveMode == "adaptive_only") {
		minBudget, maxBudget = 0, 0
	}
	if supports.Thinking == nil || *supports.Thinking {
		if len(efforts) > 0 || (route.Endpoint == Messages && maxBudget > 0) {
			info.Thinking = &registry.ThinkingSupport{Levels: efforts, Min: minBudget, Max: maxBudget, ZeroAllowed: (route.Endpoint == Messages && adaptiveMode != "adaptive_only") || (route.Endpoint != Messages && slices.Contains(efforts, "none"))}
			if slices.Contains(efforts, m.DefaultEffort) {
				route.DefaultEffort = m.DefaultEffort
			} else if slices.Contains(efforts, profile.DefaultEffort) {
				route.DefaultEffort = profile.DefaultEffort
			}
			if len(efforts) == 0 && profile.DefaultBudget > 0 && maxBudget > 0 {
				route.DefaultBudget = min(max(profile.DefaultBudget, minBudget), maxBudget)
			}
		}
	}
	route.Info = info
	route.NonStreamingOutput = m.Capabilities.Limits.NonStreamingOutput
	route.SystemPrompt = profile.SystemPrompt
	route.DefaultVerbosity = profile.DefaultVerbosity
	route.DefaultToolChoice = profile.DefaultToolChoice
	route.DisableSnippy = profile.DisableSnippy
	route.DefaultMaxTokens = profile.DefaultMaxTokens
	route.AnthropicBetas = profile.AnthropicBetas
	return route, true
}
