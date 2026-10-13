package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

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

// kimiResponsesCredentialCounter rotates the pinned credential among the
// compatible entries so one ID does not permanently capture the backend when
// the host has several.
var kimiResponsesCredentialCounter atomic.Uint64

// kimiResponsesModelFamily buckets a configured kimi_responses_model into the
// credential family that can serve it. It accepts the same alias set the host
// upstream normalizer recognizes (internal/runtime/executor/kimi_executor.go),
// including CLIProxyAPI "kimi-" prefixes, "[1m]" context suffixes, and thinking
// suffixes such as "(1024)" — stripped in the host's order (thinking suffix
// first, then "[1m]") so combined forms classify too. The second return value
// is false for models whose family is unknown; those must not pin a
// possibly-incompatible credential.
func kimiResponsesModelFamily(model string) (string, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.Index(m, "("); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	m = strings.TrimSuffix(m, "[1m]")
	switch m {
	case "kimi-k2.8", "k2.8",
		"kimi-k2.8-code", "k2.8-code",
		"kimi-k2.8-preview", "k2.8-preview",
		"kimi-k2.7-code", "k2.7-code",
		"kimi-for-coding", "for-coding",
		"kimi-k2.7-code-highspeed", "k2.7-code-highspeed",
		"kimi-for-coding-highspeed", "for-coding-highspeed":
		return "coding", true
	}
	// K2 and K3 registered platform models (internal/registry/models/models.json);
	// the host normalizer accepts them via generic prefix stripping.
	switch {
	case m == "k2" || m == "kimi-k2" ||
		m == "k2-thinking" || m == "kimi-k2-thinking" ||
		m == "k2.5" || m == "kimi-k2.5" ||
		m == "k2.6" || m == "kimi-k2.6":
		return "platform", true
	case m == "k3" || m == "kimi-k3" || strings.HasPrefix(m, "k3-") || strings.HasPrefix(m, "kimi-k3-"):
		return "platform", true
	}
	return "", false
}

// kimiCredentialPools splits kimi-family entries into coding-plan and
// Moonshot platform credentials, skipping disabled and unavailable ones.
func kimiCredentialPools(entries []pluginapi.HostAuthFileEntry) (coding, platform []*pluginapi.HostAuthFileEntry) {
	for i := range entries {
		entry := &entries[i]
		if !isKimiProviderEntry(entry.Provider) || entry.Disabled || entry.Unavailable {
			continue
		}
		if kimiResponsesModelForBaseURL(entry.BaseURL) == kimiResponsesCodingModel {
			coding = append(coding, entry)
		} else {
			platform = append(platform, entry)
		}
	}
	return coding, platform
}

// resolveKimiResponsesTargetForModel picks the credential and model for the
// host-routed kimi Responses backend, consuming one rotation tick. Coding-plan
// credentials are preferred over Moonshot platform keys unless a model
// override names a platform model. When the override names a model no
// credential family can serve (or no compatible credential exists), the
// second return value is false so the caller can fall back to an unpinned
// override.
func resolveKimiResponsesTargetForModel(entries []pluginapi.HostAuthFileEntry, modelOverride string) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetAt(entries, modelOverride, func() uint64 {
		return kimiResponsesCredentialCounter.Add(1)
	})
}

// peekKimiResponsesTargetForModel resolves the target without consuming a
// rotation tick; availability probes use it so probing does not skew which
// credential the actual execution pins.
func peekKimiResponsesTargetForModel(entries []pluginapi.HostAuthFileEntry, modelOverride string) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetAt(entries, modelOverride, func() uint64 {
		return kimiResponsesCredentialCounter.Load() + 1
	})
}

func resolveKimiResponsesTargetAt(entries []pluginapi.HostAuthFileEntry, modelOverride string, nextCounter func() uint64) (kimiResponsesTarget, bool) {
	coding, platform := kimiCredentialPools(entries)
	pool := coding
	if modelOverride != "" {
		family, known := kimiResponsesModelFamily(modelOverride)
		if !known {
			return kimiResponsesTarget{}, false
		}
		if family == "platform" {
			pool = platform
		}
	} else if len(pool) == 0 {
		pool = platform
	}
	if len(pool) == 0 {
		return kimiResponsesTarget{}, false
	}
	n := nextCounter()
	if n == 0 {
		n = 1
	}
	entry := pool[int(n-1)%len(pool)]
	model := kimiResponsesModelForBaseURL(entry.BaseURL)
	if modelOverride != "" {
		model = modelOverride
	}
	return kimiResponsesTarget{
		model:    model,
		authID:   strings.TrimSpace(entry.ID),
		provider: canonicalKimiSchedulingProvider(entry.Provider),
	}, true
}

// resolveKimiResponsesTargetFromEntries picks the credential and model with no
// override: coding-plan credentials preferred, rotated among compatible ones.
func resolveKimiResponsesTargetFromEntries(entries []pluginapi.HostAuthFileEntry) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetForModel(entries, "")
}

// resolveKimiResponsesTarget resolves the backend target, consuming one
// rotation tick. kimi_responses_model wins for the model; when it names a
// known family a matching credential is pinned, and an unknown model degrades
// to an unpinned override. Otherwise host.auth.list supplies metadata only
// (never secrets). When the list call fails (older host), the coding default
// is tried unpinned so the request still degrades gracefully through the
// fallback chain.
func resolveKimiResponsesTarget(cfg pluginConfig) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetWith(cfg, false)
}

// peekKimiResponsesTarget resolves the backend target without consuming a
// rotation tick; availability probes use it so probing does not skew which
// credential the actual execution pins.
func peekKimiResponsesTarget(cfg pluginConfig) (kimiResponsesTarget, bool) {
	return resolveKimiResponsesTargetWith(cfg, true)
}

func resolveKimiResponsesTargetWith(cfg pluginConfig, peek bool) (kimiResponsesTarget, bool) {
	resolve := resolveKimiResponsesTargetForModel
	if peek {
		resolve = peekKimiResponsesTargetForModel
	}
	entries, listed := listKimiAuthEntries()
	if m := strings.TrimSpace(cfg.KimiResponsesModel); m != "" {
		if target, ok := resolve(entries, m); ok {
			return target, true
		}
		return kimiResponsesTarget{model: m, provider: "kimi"}, true
	}
	if target, ok := resolve(entries, ""); ok {
		return target, true
	}
	if !listed {
		// host.auth.list unavailable: keep the unpinned coding default so an
		// explicit route accepted from AvailableProviders still executes.
		return kimiResponsesTarget{model: kimiResponsesCodingModel, provider: "kimi"}, true
	}
	return kimiResponsesTarget{}, false
}

// listKimiAuthEntries returns the host kimi auth metadata. The second return
// value is false when the host.auth.list call is unavailable or fails, which
// callers distinguish from a successful listing with no usable credential.
func listKimiAuthEntries() ([]pluginapi.HostAuthFileEntry, bool) {
	raw, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, false
	}
	var resp struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, false
	}
	return resp.Files, true
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

func hostModelStreamForwardKimiResponses(ctx context.Context, hostCallbackID string, cfg pluginConfig, body []byte, pluginStreamID string) (int, bool, error) {
	target, ok := resolveKimiResponsesTarget(cfg)
	if !ok {
		return 0, false, fmt.Errorf("kimi responses: no usable kimi credential")
	}
	reqBody, errBody := kimiResponsesBody(body, target.model, true)
	if errBody != nil {
		return 0, false, errBody
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
