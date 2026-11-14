package helps

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// APIKeyModelIsCompat reports whether the selected API-key model enables
// compatibility handling for Claude thinking blocks.
func APIKeyModelIsCompat(req cliproxyexecutor.Request) bool {
	modelInfo, ok := cliproxyauth.ResolvedModelInfo(req)
	return ok && modelInfo != nil && modelInfo.IsCompat
}

// ApplyRequestThinking preserves the registry lookup path unless the auth
// manager bound authoritative model capabilities to this execution attempt.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return ApplyRequestThinkingWithModelInfo(body, req, opts, fromFormat, toFormat, provider, nil, normalizedUpdatesChanged...)
}

// ApplyRequestThinkingDoc applies request thinking onto doc in place, preserving
// the registry lookup path unless the auth manager bound authoritative model
// capabilities to this execution attempt.
func ApplyRequestThinkingDoc(doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, providerKey string, PreserveConfigurationUpdates ...bool) error {
	return ApplyRequestThinkingWithModelInfoDoc(doc, req, opts, fromFormat, toFormat, providerKey, nil, PreserveConfigurationUpdates...)
}

// ApplyRequestThinkingWithModelInfo applies request thinking and falls back to
// fallbackModelInfo when neither request metadata nor the global registry
// resolves a model definition.
func ApplyRequestThinkingWithModelInfo(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, fallbackModelInfo *registry.ModelInfo, normalizedUpdatesChanged ...bool) ([]byte, error) {
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	source := req.Payload
	if len(source) == 0 {
		source = opts.OriginalRequest
	}
	summaryConfig := translatedRequestSummaryConfig(body, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	var out []byte
	var err error
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok {
		out, err = thinking.ApplyThinkingWithModelInfoAndSummary(body, source, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig, normalizedUpdatesChanged...)
	} else {
		out, err = thinking.ApplyThinkingWithFallbackModelInfoAndSummary(body, source, req.Model, fromFormat, toFormat, provider, fallbackModelInfo, summaryConfig, normalizedUpdatesChanged...)
	}
	if err != nil {
		return out, err
	}
	if shouldReconcileOpenAICompatToolCallReasoning(req, fromFormat, toFormat) && len(out) > 0 {
		doc := ojson.NewDoc(out)
		if reconcileOpenAICompatToolCallReasoningDoc(doc, openAICompatReasoningDefaultsActive(req, fromFormat), fromFormat) {
			out = doc.Bytes()
		}
	}
	return out, nil
}

// ApplyRequestThinkingWithModelInfoDoc applies request thinking onto doc in place
// and falls back to fallbackModelInfo when neither request metadata nor the global
// registry resolves a model definition.
func ApplyRequestThinkingWithModelInfoDoc(doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string, fallbackModelInfo *registry.ModelInfo, normalizedUpdatesChanged ...bool) error {
	if doc == nil {
		return nil
	}
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	source := req.Payload
	if len(source) == 0 {
		source = opts.OriginalRequest
	}
	summaryConfig := translatedRequestSummaryConfigDoc(doc, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	var err error
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok {
		err = thinking.ApplyThinkingWithModelInfoAndSummaryDoc(doc, source, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig, normalizedUpdatesChanged...)
	} else {
		err = thinking.ApplyThinkingWithFallbackModelInfoAndSummaryDoc(doc, source, req.Model, fromFormat, toFormat, provider, fallbackModelInfo, summaryConfig, normalizedUpdatesChanged...)
	}
	if err != nil {
		return err
	}
	if shouldReconcileOpenAICompatToolCallReasoning(req, fromFormat, toFormat) {
		reconcileOpenAICompatToolCallReasoningDoc(doc, openAICompatReasoningDefaultsActive(req, fromFormat), fromFormat)
	}
	return nil
}

func shouldReconcileOpenAICompatToolCallReasoning(req cliproxyexecutor.Request, fromFormat, toFormat string) bool {
	if !APIKeyModelIsCompat(req) || !strings.EqualFold(strings.TrimSpace(toFormat), "openai") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(fromFormat)) {
	case "claude", "openai":
		return true
	}
	return false
}

// openAICompatReasoningDefaultsActive reports whether a request that carries no
// explicit reasoning_effort should be treated as reasoning-active. Claude clients
// always state their thinking configuration, so they keep the previous default.
// OpenAI-format clients rely on the upstream default instead, which is thinking
// mode for the compat models that declare thinking support.
func openAICompatReasoningDefaultsActive(req cliproxyexecutor.Request, fromFormat string) bool {
	if !strings.EqualFold(strings.TrimSpace(fromFormat), "openai") {
		return false
	}
	info, ok := cliproxyauth.ResolvedModelInfo(req)
	if !ok || info == nil {
		return false
	}
	return info.Thinking != nil
}

func reconcileOpenAICompatToolCallReasoningDoc(doc *ojson.Doc, defaultActive bool, fromFormat string) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	messages := doc.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return false
	}
	// OpenAI ingress is a pass-through path: only fill genuinely absent values and
	// never rewrite what the client already sent. Claude ingress keeps the strict
	// whitespace fill the upstream reconcile introduced.
	openAIFormat := strings.EqualFold(strings.TrimSpace(fromFormat), "openai")
	reasoningActive := defaultActive
	if effortRes := doc.Get("reasoning_effort"); effortRes.Exists() {
		if resolvedEffort := strings.ToLower(strings.TrimSpace(effortRes.String())); resolvedEffort != "" {
			reasoningActive = resolvedEffort != string(thinking.LevelNone) && resolvedEffort != "0" && resolvedEffort != "false"
		}
	}

	modified := false
	for idx, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		toolCalls := msg.Get("tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() || len(toolCalls.Array()) == 0 {
			continue
		}
		reasoning := msg.Get("reasoning_content")
		if reasoningActive {
			// Absent values are always filled. Blank-but-present values are also
			// filled on the Claude path, matching the upstream reconcile; on the
			// OpenAI pass-through path they are left as the client sent them.
			fill := !reasoning.Exists() || reasoning.Type == ojson.Null
			if !openAIFormat && reasoning.Exists() && strings.TrimSpace(reasoning.String()) == "" && reasoning.String() != " " {
				fill = true
			}
			if fill {
				if doc.Set(fmt.Sprintf("messages.%d.reasoning_content", idx), " ") == nil {
					modified = true
				}
			}
			continue
		}
		if openAIFormat {
			// Pass-through ingress: do not strip what the client already sent.
			continue
		}
		if reasoning.Exists() && strings.TrimSpace(reasoning.String()) == "" {
			if doc.Delete(fmt.Sprintf("messages.%d.reasoning_content", idx)) == nil {
				modified = true
			}
		}
	}
	return modified
}
