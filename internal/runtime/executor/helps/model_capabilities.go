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
	if shouldReconcileClaudeOpenAICompatToolCallReasoning(req, fromFormat, toFormat) && len(out) > 0 {
		doc := ojson.NewDoc(out)
		if reconcileClaudeOpenAICompatToolCallReasoningDoc(doc) {
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
	if shouldReconcileClaudeOpenAICompatToolCallReasoning(req, fromFormat, toFormat) {
		reconcileClaudeOpenAICompatToolCallReasoningDoc(doc)
	}
	return nil
}

func shouldReconcileClaudeOpenAICompatToolCallReasoning(req cliproxyexecutor.Request, fromFormat, toFormat string) bool {
	return APIKeyModelIsCompat(req) &&
		strings.EqualFold(strings.TrimSpace(fromFormat), "claude") &&
		strings.EqualFold(strings.TrimSpace(toFormat), "openai")
}

func reconcileClaudeOpenAICompatToolCallReasoningDoc(doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	messages := doc.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return false
	}
	reasoningActive := false
	if effortRes := doc.Get("reasoning_effort"); effortRes.Exists() {
		resolvedEffort := strings.ToLower(strings.TrimSpace(effortRes.String()))
		if resolvedEffort != "" && resolvedEffort != string(thinking.LevelNone) && resolvedEffort != "0" && resolvedEffort != "false" {
			reasoningActive = true
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
			if !reasoning.Exists() || (strings.TrimSpace(reasoning.String()) == "" && reasoning.String() != " ") {
				if doc.Set(fmt.Sprintf("messages.%d.reasoning_content", idx), " ") == nil {
					modified = true
				}
			}
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
