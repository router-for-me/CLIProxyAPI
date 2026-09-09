package copilot

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type Model struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Vendor             string   `json:"vendor"`
	SupportedEndpoints []string `json:"supported_endpoints"`
	Capabilities       struct {
		Type   string `json:"type"`
		Limits struct {
			Context int `json:"max_context_window_tokens"`
			Input   int `json:"max_prompt_tokens"`
			Output  int `json:"max_output_tokens"`
		} `json:"limits"`
		Supports struct {
			Vision          bool     `json:"vision"`
			ToolCalls       bool     `json:"tool_calls"`
			Thinking        bool     `json:"thinking"`
			MinThinking     int      `json:"min_thinking_budget"`
			MaxThinking     int      `json:"max_thinking_budget"`
			ReasoningEffort []string `json:"reasoning_effort"`
		} `json:"supports"`
	} `json:"capabilities"`
}

// ModelInfo only advertises chat models with an endpoint the proxy can execute.
func (m Model) ModelInfo() *registry.ModelInfo {
	if m.ID == "" || m.Capabilities.Type != "chat" {
		return nil
	}
	endpoint := "/chat/completions"
	if len(m.SupportedEndpoints) > 0 && !slices.Contains(m.SupportedEndpoints, endpoint) {
		switch {
		case slices.Contains(m.SupportedEndpoints, "/responses"):
			endpoint = "/responses"
		case slices.Contains(m.SupportedEndpoints, "/v1/messages"):
			endpoint = "/v1/messages"
		default:
			return nil
		}
	}
	info := &registry.ModelInfo{ID: m.ID, Object: "model", OwnedBy: m.Vendor, Type: Provider, DisplayName: m.Name, ContextLength: m.Capabilities.Limits.Context, InputTokenLimit: m.Capabilities.Limits.Input, OutputTokenLimit: m.Capabilities.Limits.Output, MaxCompletionTokens: m.Capabilities.Limits.Output, UpstreamEndpoint: endpoint, SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}}
	if m.Capabilities.Supports.Vision {
		info.SupportedInputModalities = append(info.SupportedInputModalities, "image")
	}
	if m.Capabilities.Supports.ToolCalls {
		info.SupportedParameters = append(info.SupportedParameters, "tools", "tool_choice")
	}
	if levels := m.Capabilities.Supports.ReasoningEffort; len(levels) > 0 {
		info.Thinking = &registry.ThinkingSupport{Levels: levels, ZeroAllowed: slices.Contains(levels, "none")}
	} else if m.Capabilities.Supports.Thinking {
		info.Thinking = &registry.ThinkingSupport{Min: m.Capabilities.Supports.MinThinking, Max: m.Capabilities.Supports.MaxThinking, DynamicAllowed: true, ZeroAllowed: true}
	}
	return info
}

func (c *Client) Models(ctx context.Context, githubToken string) ([]*registry.ModelInfo, error) {
	token, err := c.CopilotToken(ctx, githubToken, false)
	if err != nil {
		return nil, err
	}
	var result struct {
		Data []Model `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, token.BaseURL()+"/models", "Bearer "+token.Token, nil, &result); err != nil {
		return nil, err
	}
	models := make([]*registry.ModelInfo, 0, len(result.Data))
	for _, model := range result.Data {
		if info := model.ModelInfo(); info != nil {
			models = append(models, info)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("github-copilot: no supported chat models available for this account")
	}
	return models, nil
}
