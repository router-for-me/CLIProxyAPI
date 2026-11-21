package helps

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// ApplyCopilotThinking uses the selected account's capabilities, never a global
// model entry that could belong to another Copilot account.
func ApplyCopilotThinking(translators *translator.Registry, doc *ojson.Doc, route copilot.Route, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream, updatesChanged bool) error {
	from, to := opts.SourceFormat.String(), route.Format.String()
	summary := translatedRequestSummaryConfigDoc(translators, doc, req.Payload, opts.OriginalRequest, req.Model, from, to)
	if route.Format == translator.FormatClaude && (!doc.Get("max_tokens").Exists() || !copilotHasOutputLimit(ojson.ParseBytes(req.Payload), opts.SourceFormat)) {
		limit := route.DefaultMaxTokens
		if limit <= 0 {
			limit = int(doc.Get("max_tokens").Int())
		}
		if limit <= 0 {
			limit = route.Info.MaxCompletionTokens
		}
		if limit <= 0 {
			limit = 4096
		}
		if route.Info.MaxCompletionTokens > 0 {
			limit = min(limit, route.Info.MaxCompletionTokens)
		}
		if !stream && route.NonStreamingOutput > 0 {
			limit = min(limit, route.NonStreamingOutput)
		}
		_ = doc.Set("max_tokens", limit)
	}
	// Seed only missing target intent. The canonical pipeline still gives the
	// request's explicit source intent and model suffix precedence.
	if !thinking.HasThinkingSuffix(req.Model) {
		switch route.Format {
		case translator.FormatOpenAI:
			if route.DefaultEffort != "" && !doc.Get("reasoning_effort").Exists() {
				_ = doc.Set("reasoning_effort", route.DefaultEffort)
			}
		case translator.FormatClaude:
			mode := doc.Get("thinking.type").String()
			if !doc.Get("output_config.effort").Exists() {
				if route.DefaultEffort != "" && (mode == "adaptive" || !doc.Get("thinking").Exists() || (mode == "" && doc.Get("thinking.display").Exists())) {
					_ = doc.Set("thinking.type", "adaptive")
					_ = doc.Set("output_config.effort", route.DefaultEffort)
				} else if !doc.Get("thinking").Exists() && route.DefaultBudget > 0 && doc.Get("max_tokens").Int() > int64(route.DefaultBudget) {
					_ = doc.Set("thinking.type", "enabled")
					_ = doc.Set("thinking.budget_tokens", route.DefaultBudget)
				}
			}
		case translator.FormatOpenAIResponse:
			if route.DefaultEffort != "" && !doc.Get("reasoning.effort").Exists() {
				_ = doc.Set("reasoning.effort", route.DefaultEffort)
			}
		}
	}
	if summary.Mode == thinking.SummaryUnspecified && (route.DefaultEffort != "" || route.DefaultBudget > 0) {
		switch route.Format {
		case translator.FormatOpenAIResponse:
			summary = thinking.SummaryConfig{Mode: thinking.SummaryEnabled, Detail: "auto"}
		case translator.FormatClaude:
			if mode := doc.Get("thinking.type").String(); mode == "adaptive" || mode == "enabled" {
				summary = thinking.SummaryConfig{Mode: thinking.SummaryEnabled}
			}
		}
	}
	return thinking.ApplyThinkingWithModelInfoAndSummaryDoc(doc, req.Payload, req.Model, from, to, copilot.Provider, route.Info, summary, updatesChanged)
}

// Check source intent because the Claude translators also synthesize max_tokens.
func copilotHasOutputLimit(source ojson.Result, format translator.Format) bool {
	switch format {
	case translator.FormatInteractions:
		cfg := source.Get("generation_config")
		if !cfg.Exists() {
			cfg = source.Get("generationConfig")
		}
		return cfg.Get("max_output_tokens").Exists() || cfg.Get("maxOutputTokens").Exists()
	case translator.FormatGemini:
		return source.Get("generationConfig.maxOutputTokens").Exists()
	case translator.FormatOpenAIResponse, translator.FormatCodex:
		return source.Get("max_output_tokens").Exists()
	default:
		return source.Get("max_tokens").Exists() || source.Get("max_completion_tokens").Exists()
	}
}

func NormalizeCopilotRequest(doc *ojson.Doc, route copilot.Route, from translator.Format, source ojson.Result, stream bool) {
	if stream || route.Format == translator.FormatClaude || source.Get("stream").Exists() {
		_ = doc.Set("stream", stream)
	} else {
		_ = doc.Delete("stream")
	}
	if route.Format != translator.FormatOpenAIResponse && (route.DefaultEffort != "" || route.DefaultBudget > 0) && !doc.Get("temperature").Exists() && !doc.Get("top_p").Exists() && !doc.Get("top_k").Exists() {
		_ = doc.Set("temperature", 1)
	}
	switch route.Format {
	case translator.FormatOpenAI:
		if route.DisableSnippy && !doc.Get("snippy").Exists() {
			_ = doc.Set("snippy.enabled", false)
		}
		if route.DefaultToolChoice != "" && doc.Get("tools.0").Exists() && !doc.Get("tool_choice").Exists() {
			_ = doc.Set("tool_choice", route.DefaultToolChoice)
		}
		if route.Info.Thinking == nil {
			_ = doc.Delete("reasoning_effort")
		}
		for i, message := range doc.Get("messages").Array() {
			role := message.Get("role").String()
			if from == translator.FormatClaude {
				// Claude tool_result has no Chat name field. Do not alter native Chat envelopes.
				prefix := "messages." + strconv.Itoa(i)
				if role == "tool" && message.Get("name").Exists() {
					_ = doc.Delete(prefix + ".name")
				}
				if role == "assistant" && message.Get("tool_calls.0").Exists() && message.Get("content").Type == ojson.String && message.Get("content").String() == "" {
					_ = doc.SetRaw(prefix+".content", "null")
				}
			}
			if role == "developer" {
				role = "system"
				_ = doc.Set("messages."+strconv.Itoa(i)+".role", role)
			}
			// Chat represents a single plain text prompt as a string. Do not flatten
			// client-native arrays, multimodal messages, or blocks carrying annotations.
			if from != translator.FormatOpenAI && (role == "system" || role == "user") {
				parts := message.Get("content")
				part := parts.Get("0")
				if parts.IsArray() && parts.Get("#").Int() == 1 && part.Get("type").String() == "text" && part.Get("text").Type == ojson.String && len(part.Map()) == 2 {
					_ = doc.Set("messages."+strconv.Itoa(i)+".content", part.Get("text").String())
				}
			}
		}
		if stream && !doc.Get("stream_options.include_usage").Exists() {
			_ = doc.Set("stream_options.include_usage", true)
		}
	case translator.FormatClaude:
		if len(route.AnthropicBetas) > 0 && doc.Get("thinking.type").String() == "adaptive" && !doc.Get("thinking.block_binding").Exists() {
			_ = doc.Set("thinking.block_binding.prefix_mismatch_behavior", "drop_block")
		}
	case translator.FormatOpenAIResponse:
		if from == translator.FormatOpenAI {
			// The CLI constructs historical function item IDs from call_id, even if
			// the returned item's ID was different. Never change the association ID.
			items := doc.Get("input").Array()
			ids := make(map[string]bool)
			counts := make(map[string]int)
			for _, item := range items {
				ids[item.Get("id").String()] = true
				if item.Get("type").String() == "function_call" {
					counts[item.Get("call_id").String()]++
				}
			}
			for i, item := range items {
				id := item.Get("call_id").String()
				if item.Get("type").String() != "function_call" || id == "" || counts[id] != 1 {
					continue
				}
				path := "input." + strconv.Itoa(i)
				if !item.Get("id").Exists() && !ids["fc_"+id] {
					_ = doc.Set(path+".id", "fc_"+id)
				}
				if !item.Get("status").Exists() {
					_ = doc.Set(path+".status", "completed")
				}
			}
		}
		if !doc.Get("tools").Exists() && !doc.Get("previous_response_id").Exists() && !doc.Get("conversation").Exists() {
			_ = doc.SetRaw("tools", "[]")
		}
		if route.DefaultVerbosity != "" && !doc.Get("text.verbosity").Exists() {
			_ = doc.Set("text.verbosity", route.DefaultVerbosity)
		}
		for i, tool := range doc.Get("tools").Array() {
			if tool.Get("type").String() == "function" && !tool.Get("strict").Exists() {
				_ = doc.Set("tools."+strconv.Itoa(i)+".strict", false)
			}
		}
		if doc.Get("tools.0").Exists() && !doc.Get("parallel_tool_calls").Exists() {
			_ = doc.Set("parallel_tool_calls", true)
		}
		if !doc.Get("store").Exists() {
			_ = doc.Set("store", false)
		}
		if route.Info.Thinking == nil {
			_ = doc.Delete("reasoning")
		} else if effort := doc.Get("reasoning.effort").String(); effort != "" && effort != "none" {
			include := doc.Get("include").Array()
			items := make([]string, 0, len(include)+1)
			for _, item := range include {
				items = append(items, item.String())
			}
			if !slices.Contains(items, "reasoning.encrypted_content") {
				_ = doc.Set("include", append(items, "reasoning.encrypted_content"))
			}
		}
	}
}

func SetCopilotInferenceHeaders(header http.Header, route copilot.Route, root ojson.Result, initiator string, turn CopilotTurn) {
	header.Set("User-Agent", copilot.CLIUserAgent+" client/sdk")
	header.Set("Editor-Version", copilot.CLIEditor)
	header.Set("Copilot-Integration-Id", "copilot-developer-cli")
	header.Set("X-GitHub-Api-Version", "2026-08-01")
	if root.Get("stream").Bool() {
		header.Set("X-Stainless-Helper-Method", "stream")
	}
	if route.Format != translator.FormatClaude {
		header.Set("Accept", "application/json")
		header.Set("Openai-Intent", "conversation-agent")
	}
	header.Set("X-Initiator", initiator)
	header.Set("X-Interaction-Type", "conversation-"+initiator)
	header.Set("X-Interaction-Id", turn.interaction.String())
	header.Set("X-Agent-Task-Id", turn.task.String())
	header.Set("X-Client-Session-Id", turn.session.String())
	path := "messages"
	if route.Format == translator.FormatOpenAIResponse {
		path = "input"
	}
	for _, item := range root.Get(path).Array() {
		if hasCopilotImage(item.Get("content")) || hasCopilotImage(item.Get("output")) {
			header.Set("Copilot-Vision-Request", "true")
			break
		}
	}
	if route.Format == translator.FormatClaude {
		header.Set("Anthropic-Version", "2023-06-01")
		if root.Get("thinking.type").String() == "adaptive" && root.Get("thinking.block_binding").IsObject() && len(route.AnthropicBetas) > 0 {
			header.Set("Anthropic-Beta", strings.Join(route.AnthropicBetas, ","))
		}
	}
}

func hasCopilotImage(content ojson.Result) bool {
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "image", "image_url", "input_image":
			return true
		case "tool_result":
			if hasCopilotImage(part.Get("content")) {
				return true
			}
		}
	}
	return false
}
