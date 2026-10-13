package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const (
	// kimiResponsesCodingModel is the registry name KimiExecutor normalizes to
	// kimi-for-coding on coding-plan endpoints (api.kimi.com/coding, api.kimi.ai/coding).
	kimiResponsesCodingModel = "kimi-k2.8-code"
	// kimiResponsesPlatformModel is the default for Moonshot Open Platform credentials.
	kimiResponsesPlatformModel = "kimi-k3"
)

// kimiResponsesTarget is the resolved host execution target for the kimi_responses
// backend: a registry model plus the credential ID and provider to pin when known.
// provider carries the host-canonical scheduling key ("kimi" or "kimi-ai") of the
// pinned credential, because the host selects auths within the forced provider
// bucket and rejects a pinned ID whose provider does not match.
type kimiResponsesTarget struct {
	model    string
	authID   string
	provider string
}

// canonicalKimiSchedulingProvider maps a kimi-family provider key to the
// canonical scheduling name the host uses in auth selection: kimi.com
// credentials schedule under "kimi", kimi.ai credentials under "kimi-ai".
func canonicalKimiSchedulingProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "kimi.ai", "kimi-ai":
		return "kimi-ai"
	default:
		return "kimi"
	}
}

// isKimiProviderEntry reports whether an auth entry belongs to the kimi provider family.
func isKimiProviderEntry(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return true
	}
	return false
}

// kimiResponsesModelForBaseURL maps a credential base URL to the registry model whose
// normalized upstream name exists on that endpoint family.
func kimiResponsesModelForBaseURL(baseURL string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(baseURL)), "moonshot") {
		return kimiResponsesPlatformModel
	}
	return kimiResponsesCodingModel
}

// resolveKimiResponsesTargetFromEntries picks the credential and model for the
// host-routed kimi Responses backend. Coding-plan credentials are preferred over
// Moonshot platform keys; the model follows the endpoint family of the chosen entry.
func resolveKimiResponsesTargetFromEntries(entries []pluginapi.HostAuthFileEntry) (kimiResponsesTarget, bool) {
	var platform *pluginapi.HostAuthFileEntry
	for i := range entries {
		entry := &entries[i]
		if !isKimiProviderEntry(entry.Provider) || entry.Disabled || entry.Unavailable {
			continue
		}
		if kimiResponsesModelForBaseURL(entry.BaseURL) == kimiResponsesCodingModel {
			return kimiResponsesTarget{
				model:    kimiResponsesCodingModel,
				authID:   strings.TrimSpace(entry.ID),
				provider: canonicalKimiSchedulingProvider(entry.Provider),
			}, true
		}
		if platform == nil {
			platform = entry
		}
	}
	if platform != nil {
		return kimiResponsesTarget{
			model:    kimiResponsesPlatformModel,
			authID:   strings.TrimSpace(platform.ID),
			provider: canonicalKimiSchedulingProvider(platform.Provider),
		}, true
	}
	return kimiResponsesTarget{}, false
}

// resolveKimiResponsesTarget resolves the backend target. kimi_responses_model wins
// for the model without credential pinning; otherwise host.auth.list supplies
// metadata only (never secrets). When the list call fails (older host), the coding
// default is tried unpinned so the request still degrades gracefully through the
// fallback chain.
func resolveKimiResponsesTarget(cfg pluginConfig) (kimiResponsesTarget, bool) {
	if m := strings.TrimSpace(cfg.KimiResponsesModel); m != "" {
		if target, ok := resolveKimiResponsesTargetFromEntries(listKimiAuthEntries()); ok {
			target.model = m
			return target, true
		}
		return kimiResponsesTarget{model: m, provider: "kimi"}, true
	}
	return resolveKimiResponsesTargetFromEntries(listKimiAuthEntries())
}

// listKimiAuthEntries returns the host kimi auth metadata, or nil when the
// host.auth.list call is unavailable or fails.
func listKimiAuthEntries() []pluginapi.HostAuthFileEntry {
	raw, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil
	}
	var resp struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil
	}
	return resp.Files
}

// kimiResponsesBody converts the Claude Messages request into a Kimi Responses
// payload. History is flattened to text-only input items and the only tool offered
// upstream is web_search; Claude-side tool options stay with the kimi_web_search
// backend where they map to the standalone search APIs.
func kimiResponsesBody(claudeBody []byte, model string, stream bool) ([]byte, error) {
	input := make([]map[string]any, 0, 8)
	for _, message := range gjson.GetBytes(claudeBody, "messages").Array() {
		text := strings.TrimSpace(extractClaudeMessageText(message.Get("content")))
		if text == "" {
			continue
		}
		role, contentType := "user", "input_text"
		if strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "assistant") {
			role, contentType = "assistant", "output_text"
		}
		input = append(input, map[string]any{
			"type":    "message",
			"role":    role,
			"content": []map[string]string{{"type": contentType, "text": text}},
		})
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("kimi responses: claude body carries no text input")
	}
	out := map[string]any{
		"model":   model,
		"input":   input,
		"tools":   []map[string]string{{"type": "web_search"}},
		"include": []string{"web_search_call.action.sources"},
		"store":   false,
		"stream":  stream,
	}
	if instructions := strings.TrimSpace(extractClaudeMessageText(gjson.GetBytes(claudeBody, "system"))); instructions != "" {
		out["instructions"] = instructions
	}
	return json.Marshal(out)
}

// hostModelExecuteKimiResponses routes the request to the host's kimi provider with a
// Responses-format body so KimiExecutor posts to {credential base}/v1/responses under
// the existing kimi auth (OAuth coding plan or Moonshot platform key).
func hostModelExecuteKimiResponses(ctx context.Context, hostCallbackID string, cfg pluginConfig, body []byte, stream bool) ([]byte, int, error) {
	target, ok := resolveKimiResponsesTarget(cfg)
	if !ok {
		return nil, 0, fmt.Errorf("kimi responses: no usable kimi credential")
	}
	reqBody, errBody := kimiResponsesBody(body, target.model, stream)
	if errBody != nil {
		return nil, 0, errBody
	}
	return hostModelExecuteRequest(ctx, hostCallbackID, pluginapi.HostModelExecutionRequest{
		EntryProtocol:  "openai-response",
		ExitProtocol:   "claude",
		Model:          target.model,
		Stream:         stream,
		Body:           reqBody,
		ForcedProvider: target.provider,
		AuthID:         target.authID,
	})
}

func hostModelStreamForwardKimiResponses(ctx context.Context, hostCallbackID string, cfg pluginConfig, body []byte, pluginStreamID string) (int, error) {
	target, ok := resolveKimiResponsesTarget(cfg)
	if !ok {
		return 0, fmt.Errorf("kimi responses: no usable kimi credential")
	}
	reqBody, errBody := kimiResponsesBody(body, target.model, true)
	if errBody != nil {
		return 0, errBody
	}
	return hostModelStreamForwardRequest(ctx, hostCallbackID, pluginapi.HostModelExecutionRequest{
		EntryProtocol:  "openai-response",
		ExitProtocol:   "claude",
		Model:          target.model,
		Stream:         true,
		Body:           reqBody,
		ForcedProvider: target.provider,
		AuthID:         target.authID,
	}, pluginStreamID)
}
