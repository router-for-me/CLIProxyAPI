package toolconv

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	sharedgemini "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/gemini"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func AttachRequest(format types.RelayFormat, request any, set Set, options *convmeta.Options) (any, []types.ConversionDiagnostic, error) {
	if set.Empty() {
		return request, nil, nil
	}
	var (
		value       any
		diagnostics []types.ConversionDiagnostic
		err         error
	)
	switch format {
	case types.RelayFormatOpenAI:
		value, diagnostics, err = attachOpenAIChatRequest(request, set)
	case types.RelayFormatOpenAIResponses:
		value, diagnostics, err = attachOpenAIResponsesRequest(request, set)
	case types.RelayFormatClaude:
		value, diagnostics, err = attachClaudeRequest(request, set, options)
	case types.RelayFormatGemini:
		value, diagnostics, err = attachGeminiRequest(request, set)
	default:
		value = request
	}
	if err != nil {
		return nil, diagnostics, err
	}
	for index := range diagnostics {
		diagnostics[index].From = set.Source
		diagnostics[index].To = format
	}
	if err := types.RejectConversionLoss(options.EffectiveToolLossPolicy(), diagnostics); err != nil {
		return nil, diagnostics, err
	}
	return value, diagnostics, nil
}

func attachOpenAIChatRequest(request any, set Set) (any, []types.ConversionDiagnostic, error) {
	target, ok := request.(*dto.GeneralOpenAIRequest)
	if !ok || target == nil {
		return nil, nil, fmt.Errorf("expected OpenAI chat completions request, got %T", request)
	}
	var diagnostics []types.ConversionDiagnostic
	for index, definition := range set.Definitions {
		switch definition.Kind {
		case KindFunction:
			if definition.Function == nil {
				continue
			}
			target.Tools = append(target.Tools, dto.ToolCallRequest{
				Type: "function",
				Function: dto.FunctionRequest{
					Name:        definition.Function.Name,
					Description: definition.Function.Description,
					Parameters:  definition.Function.Parameters,
					Strict:      definition.Function.Strict,
				},
			})
		case KindWebSearch:
			if set.Source == types.RelayFormatGemini {
				diagnostics = append(diagnostics, geminiNativeWebSearchDiagnostics(index, definition, types.RelayFormatOpenAI)...)
			}
			if target.WebSearchOptions != nil {
				return nil, diagnostics, fmt.Errorf("tools[%d]: multiple hosted web-search definitions cannot be represented by Chat Completions", index)
			}
			options := &dto.WebSearchOptions{}
			if definition.WebSearch != nil {
				options.SearchContextSize = definition.WebSearch.SearchContextSize
				if definition.WebSearch.Location != nil {
					location := map[string]any{
						"type":        "approximate",
						"approximate": locationMap(definition.WebSearch.Location),
					}
					options.UserLocation, _ = kitutil.Marshal(location)
				}
				diagnostics = append(diagnostics, openAIChatWebSearchDiagnostics(index, definition.WebSearch)...)
			}
			target.WebSearchOptions = options
		default:
			diagnostics = append(diagnostics, semanticLoss(
				fmt.Sprintf("tools[%d]", index),
				"unsupported_hosted_tool",
				fmt.Sprintf("OpenAI Chat Completions cannot represent hosted tool %q", definition.NativeType),
			))
		}
	}

	normalizedChoice, allowedChoiceDiagnostics := narrowAllowedFunctionChoice(set.Choice, types.RelayFormatOpenAI)
	choice, choiceDiagnostics := encodeOpenAIChatChoice(normalizedChoice)
	target.ToolChoice = choice
	target.ParallelTooCalls = set.ParallelAllowed
	diagnostics = append(diagnostics, allowedChoiceDiagnostics...)
	diagnostics = append(diagnostics, choiceDiagnostics...)
	diagnostics = append(diagnostics, unsupportedHostedHistoryDiagnostics(types.RelayFormatOpenAI, set.History)...)
	return target, diagnostics, nil
}

func attachOpenAIResponsesRequest(request any, set Set) (any, []types.ConversionDiagnostic, error) {
	target, ok := request.(*dto.OpenAIResponsesRequest)
	if !ok || target == nil {
		return nil, nil, fmt.Errorf("expected OpenAI Responses request, got %T", request)
	}
	tools := make([]any, 0, len(set.Definitions))
	var diagnostics []types.ConversionDiagnostic
	for index, definition := range set.Definitions {
		switch definition.Kind {
		case KindFunction:
			if definition.Function == nil {
				continue
			}
			tool := map[string]any{
				"type":        "function",
				"name":        definition.Function.Name,
				"description": definition.Function.Description,
				"parameters":  definition.Function.Parameters,
			}
			if definition.Function.Strict != nil {
				tool["strict"] = *definition.Function.Strict
			}
			deleteEmptyStrings(tool)
			tools = append(tools, tool)
		case KindWebSearch:
			if set.Source == types.RelayFormatGemini {
				diagnostics = append(diagnostics, geminiNativeWebSearchDiagnostics(index, definition, types.RelayFormatOpenAIResponses)...)
			}
			webSearch := definition.WebSearch
			toolType := "web_search"
			if set.Source == types.RelayFormatOpenAIResponses && definition.NativeType != "" {
				toolType = definition.NativeType
			}
			tool := map[string]any{"type": toolType}
			if webSearch != nil {
				if webSearch.SearchContextSize != "" {
					tool["search_context_size"] = webSearch.SearchContextSize
				}
				if webSearch.Location != nil {
					location := locationMap(webSearch.Location)
					location["type"] = "approximate"
					tool["user_location"] = location
				}
				if len(webSearch.AllowedDomains) > 0 {
					tool["filters"] = map[string]any{"allowed_domains": webSearch.AllowedDomains}
				}
				if webSearch.ExternalWebAccess != nil {
					tool["external_web_access"] = *webSearch.ExternalWebAccess
				}
				if len(webSearch.ReturnTokenBudget) > 0 {
					var budget any
					if err := kitutil.Unmarshal(webSearch.ReturnTokenBudget, &budget); err != nil {
						return nil, diagnostics, fmt.Errorf("tools[%d].return_token_budget: %w", index, err)
					}
					tool["return_token_budget"] = budget
				}
				diagnostics = append(diagnostics, openAIResponsesWebSearchDiagnostics(index, webSearch)...)
			}
			tools = append(tools, tool)
		default:
			if set.Source == types.RelayFormatOpenAIResponses && len(definition.Raw) > 0 {
				var tool any
				if err := kitutil.Unmarshal(definition.Raw, &tool); err != nil {
					return nil, diagnostics, err
				}
				tools = append(tools, tool)
				continue
			}
			diagnostics = append(diagnostics, semanticLoss(
				fmt.Sprintf("tools[%d]", index),
				"unsupported_hosted_tool",
				fmt.Sprintf("OpenAI Responses has no verified mapping for hosted tool %q", definition.NativeType),
			))
		}
	}
	if len(tools) > 0 {
		encoded, err := kitutil.Marshal(tools)
		if err != nil {
			return nil, diagnostics, err
		}
		target.Tools = encoded
	}
	normalizedChoice, allowedChoiceDiagnostics := narrowAllowedFunctionChoice(set.Choice, types.RelayFormatOpenAIResponses)
	choice, choiceDiagnostics, err := encodeOpenAIResponsesChoice(normalizedChoice, set.Source)
	if err != nil {
		return nil, diagnostics, err
	}
	target.ToolChoice = choice
	if set.ParallelAllowed != nil {
		target.ParallelToolCalls, _ = kitutil.Marshal(*set.ParallelAllowed)
	}
	diagnostics = append(diagnostics, allowedChoiceDiagnostics...)
	diagnostics = append(diagnostics, choiceDiagnostics...)
	historyDiagnostics, err := appendHostedHistoryToOpenAIResponses(target, set)
	if err != nil {
		return nil, diagnostics, err
	}
	diagnostics = append(diagnostics, historyDiagnostics...)
	return target, diagnostics, nil
}

func attachClaudeRequest(request any, set Set, options *convmeta.Options) (any, []types.ConversionDiagnostic, error) {
	target, ok := request.(*dto.ClaudeRequest)
	if !ok || target == nil {
		return nil, nil, fmt.Errorf("expected Claude Messages request, got %T", request)
	}
	tools := make([]any, 0, len(set.Definitions))
	var diagnostics []types.ConversionDiagnostic
	for index, definition := range set.Definitions {
		switch definition.Kind {
		case KindFunction:
			if definition.Function == nil {
				continue
			}
			inputSchema, err := functionParametersMap(definition.Function.Parameters)
			if err != nil {
				return nil, diagnostics, fmt.Errorf("tools[%d].input_schema: %w", index, err)
			}
			tools = append(tools, &dto.Tool{
				Name:        definition.Function.Name,
				Description: definition.Function.Description,
				InputSchema: inputSchema,
				Strict:      definition.Function.Strict,
			})
		case KindWebSearch:
			if set.Source == types.RelayFormatGemini {
				diagnostics = append(diagnostics, geminiNativeWebSearchDiagnostics(index, definition, types.RelayFormatClaude)...)
			}
			toolType := "web_search_20250305"
			if set.Source == types.RelayFormatClaude && isKnownClaudeWebSearchType(definition.NativeType) {
				toolType = definition.NativeType
			} else if options != nil && options.Claude.WebSearchToolVersion != "" {
				if !isKnownClaudeWebSearchType(options.Claude.WebSearchToolVersion) {
					return nil, diagnostics, fmt.Errorf("unsupported Claude web-search tool version %q", options.Claude.WebSearchToolVersion)
				}
				toolType = options.Claude.WebSearchToolVersion
			}
			webSearch := definition.WebSearch
			if webSearch != nil && len(webSearch.AllowedDomains) > 0 && len(webSearch.BlockedDomains) > 0 {
				return nil, diagnostics, fmt.Errorf("tools[%d]: allowed_domains and blocked_domains are mutually exclusive", index)
			}
			if webSearch != nil && webSearch.ResponseInclusion != "" && !claudeWebSearchSupportsResponseInclusion(toolType) {
				return nil, diagnostics, fmt.Errorf("tools[%d].response_inclusion requires Claude web_search_20260318", index)
			}
			tool := map[string]any{"type": toolType, "name": "web_search"}
			if webSearch != nil {
				if webSearch.Location != nil {
					location := locationMap(webSearch.Location)
					location["type"] = "approximate"
					tool["user_location"] = location
				}
				if len(webSearch.AllowedDomains) > 0 {
					tool["allowed_domains"] = webSearch.AllowedDomains
				}
				if len(webSearch.BlockedDomains) > 0 {
					tool["blocked_domains"] = webSearch.BlockedDomains
				}
				if webSearch.MaxUses != nil {
					tool["max_uses"] = *webSearch.MaxUses
				}
				if len(webSearch.AllowedCallers) > 0 {
					tool["allowed_callers"] = webSearch.AllowedCallers
				}
				if webSearch.ResponseInclusion != "" {
					tool["response_inclusion"] = webSearch.ResponseInclusion
				}
				diagnostics = append(diagnostics, claudeWebSearchDiagnostics(index, webSearch)...)
			}
			tools = append(tools, tool)
		default:
			if set.Source == types.RelayFormatClaude && len(definition.Raw) > 0 {
				var tool any
				if err := kitutil.Unmarshal(definition.Raw, &tool); err != nil {
					return nil, diagnostics, err
				}
				tools = append(tools, tool)
				continue
			}
			diagnostics = append(diagnostics, semanticLoss(
				fmt.Sprintf("tools[%d]", index),
				"unsupported_hosted_tool",
				fmt.Sprintf("Claude Messages has no verified mapping for hosted tool %q", definition.NativeType),
			))
		}
	}
	if len(tools) > 0 {
		target.Tools = tools
	}
	normalizedChoice, allowedChoiceDiagnostics := narrowAllowedFunctionChoice(set.Choice, types.RelayFormatClaude)
	choice, choiceDiagnostics := encodeClaudeChoice(normalizedChoice, set.ParallelAllowed, set.Source)
	target.ToolChoice = choice
	diagnostics = append(diagnostics, allowedChoiceDiagnostics...)
	diagnostics = append(diagnostics, choiceDiagnostics...)
	historyDiagnostics, err := appendHostedHistoryToClaude(target, set)
	if err != nil {
		return nil, diagnostics, err
	}
	diagnostics = append(diagnostics, historyDiagnostics...)
	return target, diagnostics, nil
}

func attachGeminiRequest(request any, set Set) (any, []types.ConversionDiagnostic, error) {
	target, ok := request.(*dto.GeminiChatRequest)
	if !ok || target == nil {
		return nil, nil, fmt.Errorf("expected Gemini generateContent request, got %T", request)
	}
	if set.Source == types.RelayFormatGemini {
		tools, err := rebuildGeminiToolGroups(set.Definitions)
		if err != nil {
			return nil, nil, err
		}
		if len(tools) > 0 {
			target.Tools, err = kitutil.Marshal(tools)
			if err != nil {
				return nil, nil, err
			}
		}
		if len(set.NativeToolConfig) > 0 {
			var config dto.ToolConfig
			if err := kitutil.Unmarshal(set.NativeToolConfig, &config); err != nil {
				return nil, nil, fmt.Errorf("toolConfig: %w", err)
			}
			target.ToolConfig = &config
		}
		return target, unsupportedHostedHistoryDiagnostics(types.RelayFormatGemini, set.History), nil
	}
	var (
		functions   []map[string]any
		tools       []map[string]any
		diagnostics []types.ConversionDiagnostic
	)
	for index, definition := range set.Definitions {
		switch definition.Kind {
		case KindFunction:
			if definition.Function == nil {
				continue
			}
			parameters := definition.Function.Parameters
			if parameters != nil {
				cloned, err := kitutil.Any2Type[any](parameters)
				if err != nil {
					return nil, diagnostics, fmt.Errorf("tools[%d].parameters: %w", index, err)
				}
				if params, ok := cloned.(map[string]any); ok {
					if properties, exists := params["properties"].(map[string]any); exists && len(properties) == 0 {
						cloned = nil
					}
				}
				parameters = sharedgemini.CleanFunctionParameters(cloned)
			}
			function := map[string]any{
				"name":        definition.Function.Name,
				"description": definition.Function.Description,
				"parameters":  parameters,
			}
			deleteEmptyStrings(function)
			functions = append(functions, function)
			if definition.Function.Strict != nil {
				diagnostics = append(diagnostics, presentationLoss(fmt.Sprintf("tools[%d].strict", index), "unsupported_function_strict", "Gemini does not expose OpenAI function strictness"))
			}
		case KindWebSearch:
			if set.Source == types.RelayFormatGemini && len(definition.Raw) > 0 {
				var tool map[string]any
				if err := kitutil.Unmarshal(definition.Raw, &tool); err != nil {
					return nil, diagnostics, err
				}
				tools = append(tools, tool)
			} else {
				tools = append(tools, map[string]any{"googleSearch": map[string]any{}})
			}
			if definition.WebSearch != nil {
				diagnostics = append(diagnostics, geminiWebSearchDiagnostics(index, definition.WebSearch)...)
			}
		case KindCodeExecution:
			if set.Source == types.RelayFormatGemini || definition.NativeType == "codeExecution" {
				tools = append(tools, map[string]any{"codeExecution": map[string]any{}})
				continue
			}
			diagnostics = append(diagnostics, semanticLoss(fmt.Sprintf("tools[%d]", index), "unverified_tool_mapping", "code execution semantics differ across providers"))
		case KindURLContext:
			if set.Source == types.RelayFormatGemini || definition.NativeType == "urlContext" {
				tools = append(tools, map[string]any{"urlContext": map[string]any{}})
				continue
			}
			diagnostics = append(diagnostics, semanticLoss(fmt.Sprintf("tools[%d]", index), "unverified_tool_mapping", "URL context has no verified mapping from the source protocol"))
		default:
			if set.Source == types.RelayFormatGemini && len(definition.Raw) > 0 {
				var tool map[string]any
				if err := kitutil.Unmarshal(definition.Raw, &tool); err != nil {
					return nil, diagnostics, err
				}
				tools = append(tools, tool)
				continue
			}
			// The established Responses-to-Gemini compatibility path removes
			// free-form/unknown tools together with custom call history in
			// PrepareOpenAIResponsesRequest. Keep that explicit downgrade as a
			// diagnostic; other opaque tools may be server-executed and remain a
			// semantic loss under the default Safe policy.
			if set.Source == types.RelayFormatOpenAIResponses && (definition.NativeType == "custom" || definition.NativeType == "unknown") {
				diagnostics = append(diagnostics, presentationLoss(
					fmt.Sprintf("tools[%d]", index),
					"custom_tool_omitted",
					"Gemini cannot represent this OpenAI free-form or unknown tool; its preprocessed call history and definition were omitted",
				))
				continue
			}
			if set.Source == types.RelayFormatOpenAIResponses && definition.Kind == KindNative {
				diagnostics = append(diagnostics, semanticLoss(
					fmt.Sprintf("tools[%d]", index),
					"unsupported_opaque_tool",
					fmt.Sprintf("Gemini cannot represent OpenAI opaque tool %q; the definition was omitted", definition.NativeType),
				))
				continue
			}
			diagnostics = append(diagnostics, semanticLoss(
				fmt.Sprintf("tools[%d]", index),
				"unsupported_hosted_tool",
				fmt.Sprintf("Gemini generateContent has no verified mapping for hosted tool %q", definition.NativeType),
			))
		}
	}
	if len(functions) > 0 {
		tools = append(tools, map[string]any{"functionDeclarations": functions})
	}
	if len(tools) > 0 {
		encoded, err := kitutil.Marshal(tools)
		if err != nil {
			return nil, diagnostics, err
		}
		target.Tools = encoded
	}
	config, choiceDiagnostics := encodeGeminiChoice(set.Choice)
	target.ToolConfig = config
	diagnostics = append(diagnostics, choiceDiagnostics...)
	if set.ParallelAllowed != nil && !*set.ParallelAllowed {
		diagnostics = append(diagnostics, semanticLoss(
			"parallel_tool_calls",
			"unsupported_parallel_tool_control",
			"Gemini generateContent does not expose a request field equivalent to parallel_tool_calls",
		))
	}
	diagnostics = append(diagnostics, unsupportedHostedHistoryDiagnostics(types.RelayFormatGemini, set.History)...)
	return target, diagnostics, nil
}

func rebuildGeminiToolGroups(definitions []Definition) ([]map[string]any, error) {
	groups := make(map[int]map[string]any)
	indexes := make([]int, 0)
	for index, definition := range definitions {
		if len(definition.Raw) == 0 {
			return nil, fmt.Errorf("tools[%d]: missing native Gemini tool payload", index)
		}
		var fragment map[string]any
		if err := kitutil.Unmarshal(definition.Raw, &fragment); err != nil {
			return nil, fmt.Errorf("tools[%d]: %w", index, err)
		}
		group, exists := groups[definition.Group]
		if !exists {
			group = make(map[string]any)
			groups[definition.Group] = group
			indexes = append(indexes, definition.Group)
		}
		for key, value := range fragment {
			if key == "functionDeclarations" {
				existing, _ := group[key].([]any)
				incoming, ok := value.([]any)
				if !ok {
					return nil, fmt.Errorf("tools[%d].functionDeclarations must be an array", index)
				}
				group[key] = append(existing, incoming...)
				continue
			}
			group[key] = value
		}
	}
	sort.Ints(indexes)
	tools := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		tools = append(tools, groups[index])
	}
	return tools, nil
}

func appendHostedHistoryToOpenAIResponses(target *dto.OpenAIResponsesRequest, set Set) ([]types.ConversionDiagnostic, error) {
	if len(set.History) == 0 {
		return nil, nil
	}
	input, err := responsesInputItems(target.Input)
	if err != nil {
		return nil, err
	}
	itemsBySourceIndex := make(map[int][]map[string]any)
	mixedBySourceIndex := make(map[int]bool)
	convertedByID := make(map[string]map[string]any)
	var diagnostics []types.ConversionDiagnostic
	for index, history := range set.History {
		if history.MessageHasRegular {
			mixedBySourceIndex[history.MessageIndex] = true
		}
		var item map[string]any
		if set.Source == types.RelayFormatOpenAIResponses && len(history.Raw) > 0 {
			if err := kitutil.Unmarshal(history.Raw, &item); err != nil {
				return nil, fmt.Errorf("hosted_history[%d]: %w", index, err)
			}
		} else {
			outputType := responsesTypeFromHostedKind(history.Kind)
			if outputType == "" {
				diagnostics = append(diagnostics, semanticLoss(
					fmt.Sprintf("hosted_history[%d]", index),
					"hosted_tool_history_unsupported",
					fmt.Sprintf("%s cannot preserve hosted-tool continuation item %q", types.RelayFormatOpenAIResponses, history.NativeType),
				))
				continue
			}
			id := history.ID
			if id == "" {
				id = history.CallID
			}
			if strings.HasSuffix(history.NativeType, "_tool_result") || history.NativeType == "mcp_tool_result" {
				if call, exists := convertedByID[history.CallID]; exists {
					failed := history.Status == "failed"
					if failed {
						call["status"] = "failed"
					} else {
						call["status"] = "completed"
					}
					if history.Kind == KindMCP {
						if len(history.Results) > 0 {
							var (
								encoded    json.RawMessage
								normalized bool
								err        error
							)
							if failed {
								encoded, normalized, err = responsesMCPErrorFromClaudeContent(history.Results, "")
							} else {
								encoded, normalized, err = responsesMCPStringFromClaudeContent(history.Results)
							}
							if err != nil {
								diagnostics = append(diagnostics, semanticLoss(
									fmt.Sprintf("hosted_history[%d].content", index),
									"mcp_result_unrepresentable",
									err.Error(),
								))
								continue
							}
							var value string
							if err := kitutil.Unmarshal(encoded, &value); err != nil {
								return nil, fmt.Errorf("hosted_history[%d].content: %w", index, err)
							}
							if failed {
								call["error"] = value
							} else {
								call["output"] = value
							}
							if normalized {
								diagnostics = append(diagnostics, presentationLoss(
									fmt.Sprintf("hosted_history[%d].content", index),
									"mcp_text_result_normalized",
									"Claude's single MCP text block was normalized to a Responses output string",
								))
							}
						}
					} else if history.Kind == KindWebSearch && len(history.Results) > 0 {
						diagnostics = append(diagnostics, presentationLoss(
							fmt.Sprintf("hosted_history[%d].content", index),
							"web_search_result_omitted",
							"Claude web-search result content has no field on a Responses web_search_call",
						))
					}
					continue
				}
			}
			item = map[string]any{
				"type":   outputType,
				"id":     id,
				"status": history.Status,
			}
			if item["status"] == "" {
				item["status"] = "in_progress"
			}
			switch history.Kind {
			case KindWebSearch:
				action, err := dto.NormalizeResponsesWebSearchAction(history.Action)
				if err != nil {
					return nil, fmt.Errorf("hosted_history[%d].action: %w", index, err)
				}
				var actionValue any
				if err := kitutil.Unmarshal(action, &actionValue); err != nil {
					return nil, fmt.Errorf("hosted_history[%d].action: %w", index, err)
				}
				item["action"] = actionValue
			case KindMCP:
				if strings.TrimSpace(history.Name) == "" || strings.TrimSpace(history.ServerName) == "" {
					diagnostics = append(diagnostics, semanticLoss(
						fmt.Sprintf("hosted_history[%d]", index),
						"mcp_identity_missing",
						"Claude MCP history requires both name and server_name for Responses mapping",
					))
					continue
				}
				item["name"] = history.Name
				item["server_label"] = history.ServerName
				if len(history.Action) > 0 {
					arguments, err := responsesMCPArgumentsFromClaude(history.Action)
					if err != nil {
						diagnostics = append(diagnostics, semanticLoss(
							fmt.Sprintf("hosted_history[%d].input", index),
							"mcp_arguments_unrepresentable",
							err.Error(),
						))
						continue
					}
					var value string
					if err := kitutil.Unmarshal(arguments, &value); err != nil {
						return nil, fmt.Errorf("hosted_history[%d].input: %w", index, err)
					}
					item["arguments"] = value
				} else {
					diagnostics = append(diagnostics, semanticLoss(
						fmt.Sprintf("hosted_history[%d].input", index),
						"mcp_arguments_missing",
						"Claude MCP history has no input object",
					))
					continue
				}
				if len(history.Results) > 0 {
					failed := history.Status == "failed"
					var encoded json.RawMessage
					var normalized bool
					if failed {
						encoded, normalized, err = responsesMCPErrorFromClaudeContent(history.Results, "")
					} else {
						encoded, normalized, err = responsesMCPStringFromClaudeContent(history.Results)
					}
					if err != nil {
						diagnostics = append(diagnostics, semanticLoss(
							fmt.Sprintf("hosted_history[%d].content", index),
							"mcp_result_unrepresentable",
							err.Error(),
						))
						continue
					}
					var value string
					if err := kitutil.Unmarshal(encoded, &value); err != nil {
						return nil, fmt.Errorf("hosted_history[%d].content: %w", index, err)
					}
					if failed {
						item["error"] = value
						item["status"] = "failed"
					} else {
						item["output"] = value
						item["status"] = "completed"
					}
					if normalized {
						diagnostics = append(diagnostics, presentationLoss(
							fmt.Sprintf("hosted_history[%d].content", index),
							"mcp_text_result_normalized",
							"Claude's single MCP text block was normalized to a Responses output string",
						))
					}
				}
				if len(history.Caller) > 0 {
					var caller any
					if err := kitutil.Unmarshal(history.Caller, &caller); err != nil {
						return nil, fmt.Errorf("hosted_history[%d].caller: %w", index, err)
					}
					item["caller"] = caller
				}
			}
			if id != "" {
				convertedByID[id] = item
			}
		}
		itemsBySourceIndex[history.MessageIndex] = append(itemsBySourceIndex[history.MessageIndex], item)
	}
	if len(itemsBySourceIndex) == 0 {
		if len(diagnostics) > 0 {
			return diagnostics, nil
		}
		return unsupportedHostedHistoryDiagnostics(types.RelayFormatOpenAIResponses, set.History), nil
	}
	merged := make([]map[string]any, 0, len(input)+len(set.History))
	inputIndex := 0
	maxSourceIndex := 0
	for _, history := range set.History {
		if history.MessageIndex > maxSourceIndex {
			maxSourceIndex = history.MessageIndex
		}
	}
	for sourceIndex := 0; sourceIndex <= maxSourceIndex || inputIndex < len(input); sourceIndex++ {
		if hostedItems := itemsBySourceIndex[sourceIndex]; len(hostedItems) > 0 {
			if !mixedBySourceIndex[sourceIndex] {
				merged = append(merged, hostedItems...)
				continue
			}
			merged = append(merged, hostedItems...)
			if inputIndex < len(input) {
				merged = append(merged, input[inputIndex])
				inputIndex++
			}
			diagnostics = append(diagnostics, semanticLoss(
				fmt.Sprintf("hosted_history[%d]", sourceIndex),
				"hosted_tool_order_unrepresentable",
				"hosted and ordinary blocks share one source message, but Responses represents hosted calls as separate input items",
			))
			continue
		}
		if inputIndex < len(input) {
			merged = append(merged, input[inputIndex])
			inputIndex++
		}
	}
	encoded, err := kitutil.Marshal(merged)
	if err != nil {
		return nil, err
	}
	target.Input = encoded
	diagnostics = append(diagnostics, presentationLoss(
		"input",
		"hosted_tool_history_approximated",
		"hosted-tool continuation state is preserved, but provider-specific item fields may differ",
	))
	return diagnostics, nil
}

func appendHostedHistoryToClaude(target *dto.ClaudeRequest, set Set) ([]types.ConversionDiagnostic, error) {
	if len(set.History) == 0 {
		return nil, nil
	}
	blocksBySourceIndex := make(map[int][]any)
	mixedBySourceIndex := make(map[int]bool)
	var diagnostics []types.ConversionDiagnostic
	for index, history := range set.History {
		if history.MessageHasRegular {
			mixedBySourceIndex[history.MessageIndex] = true
		}
		var blocks []any
		if set.Source == types.RelayFormatClaude && len(history.Raw) > 0 {
			var block any
			if err := kitutil.Unmarshal(history.Raw, &block); err != nil {
				return nil, fmt.Errorf("hosted_history[%d]: %w", index, err)
			}
			blocksBySourceIndex[history.MessageIndex] = append(blocksBySourceIndex[history.MessageIndex], block)
			continue
		}
		if set.Source == types.RelayFormatOpenAIResponses && history.Kind == KindWebSearch {
			diagnostics = append(diagnostics, semanticLoss(
				fmt.Sprintf("hosted_history[%d]", index),
				"web_search_continuation_unrepresentable",
				"Responses web-search history cannot reconstruct Claude's encrypted web_search_tool_result continuation state",
			))
			continue
		}
		if history.Kind == KindMCP {
			if history.Name == "" || history.ServerName == "" {
				diagnostics = append(diagnostics, semanticLoss(
					fmt.Sprintf("hosted_history[%d]", index),
					"mcp_identity_missing",
					"Responses MCP history requires both name and server_label for Claude mapping",
				))
				continue
			}
			id := history.ID
			if id == "" {
				id = history.CallID
			}
			if id == "" {
				diagnostics = append(diagnostics, semanticLoss(
					fmt.Sprintf("hosted_history[%d].id", index),
					"hosted_tool_id_missing",
					"Responses MCP history has no id for pairing the call with its result",
				))
				continue
			}
			if history.Status != "" && history.Status != "in_progress" && history.Status != "completed" && history.Status != "failed" {
				diagnostics = append(diagnostics, semanticLoss(
					fmt.Sprintf("hosted_history[%d].status", index),
					"hosted_tool_status_unrepresentable",
					fmt.Sprintf("Claude cannot preserve Responses MCP status %q", history.Status),
				))
			}
			mcpResult := history.Results
			mcpFailed := history.Status == "failed"
			if len(history.Raw) > 0 {
				var rawFields struct {
					ApprovalRequestID string          `json:"approval_request_id"`
					Output            json.RawMessage `json:"output"`
					Error             json.RawMessage `json:"error"`
				}
				if err := kitutil.Unmarshal(history.Raw, &rawFields); err != nil {
					return nil, fmt.Errorf("hosted_history[%d]: %w", index, err)
				}
				if strings.TrimSpace(rawFields.ApprovalRequestID) != "" {
					diagnostics = append(diagnostics, semanticLoss(
						fmt.Sprintf("hosted_history[%d].approval_request_id", index),
						"mcp_approval_state_unrepresentable",
						"Claude MCP history cannot preserve a Responses approval_request_id",
					))
				}
				if rawJSONPresent(rawFields.Output) && rawJSONPresent(rawFields.Error) {
					diagnostics = append(diagnostics, semanticLoss(
						fmt.Sprintf("hosted_history[%d]", index),
						"mcp_result_ambiguous",
						"Responses MCP history contains both output and error",
					))
				}
				if rawJSONPresent(rawFields.Error) {
					mcpResult = rawFields.Error
					mcpFailed = true
				} else if rawJSONPresent(rawFields.Output) {
					mcpResult = rawFields.Output
				}
			}
			input, inputErr := claudeMCPInputFromResponses(history.Action)
			if inputErr != nil {
				diagnostics = append(diagnostics, semanticLoss(
					fmt.Sprintf("hosted_history[%d].arguments", index),
					"mcp_arguments_unrepresentable",
					inputErr.Error(),
				))
				continue
			}
			call := map[string]any{
				"type":        "mcp_tool_use",
				"id":          id,
				"name":        history.Name,
				"server_name": history.ServerName,
				"input":       input,
			}
			if len(history.Caller) > 0 {
				var caller any
				if err := kitutil.Unmarshal(history.Caller, &caller); err != nil {
					return nil, fmt.Errorf("hosted_history[%d].caller: %w", index, err)
				}
				call["caller"] = caller
			}
			blocks = append(blocks, call)
			if rawJSONPresent(mcpResult) {
				content, resultErr := claudeMCPContentFromResponsesString(mcpResult)
				if resultErr != nil {
					diagnostics = append(diagnostics, semanticLoss(
						fmt.Sprintf("hosted_history[%d].output", index),
						"mcp_result_unrepresentable",
						resultErr.Error(),
					))
					continue
				}
				result := map[string]any{"type": "mcp_tool_result", "tool_use_id": id, "content": content}
				if mcpFailed {
					result["is_error"] = true
				}
				blocks = append(blocks, result)
			} else if history.Status == "completed" || mcpFailed {
				diagnostics = append(diagnostics, semanticLoss(
					fmt.Sprintf("hosted_history[%d]", index),
					"mcp_result_missing",
					fmt.Sprintf("Responses MCP history has status %q but no output or error", history.Status),
				))
			}
			blocksBySourceIndex[history.MessageIndex] = append(blocksBySourceIndex[history.MessageIndex], blocks...)
			continue
		}
		name := claudeNameFromHostedKind(history.Kind)
		if name == "" {
			diagnostics = append(diagnostics, semanticLoss(
				fmt.Sprintf("hosted_history[%d]", index),
				"hosted_tool_history_unsupported",
				fmt.Sprintf("%s cannot preserve hosted-tool continuation item %q", types.RelayFormatClaude, history.NativeType),
			))
			continue
		}
		id := history.ID
		if id == "" {
			id = history.CallID
		}
		var input any = map[string]any{}
		if history.Kind == KindWebSearch {
			webInput, inputErr := claudeWebSearchInputFromResponses(history.Action)
			if inputErr != nil {
				diagnostics = append(diagnostics, semanticLoss(
					fmt.Sprintf("hosted_history[%d].action", index),
					"web_search_action_unrepresentable",
					inputErr.Error(),
				))
				continue
			}
			input = webInput
		} else if len(history.Action) > 0 {
			if err := kitutil.Unmarshal(history.Action, &input); err != nil {
				return nil, fmt.Errorf("hosted_history[%d].action: %w", index, err)
			}
		}
		call := map[string]any{
			"type":  "server_tool_use",
			"id":    id,
			"name":  name,
			"input": input,
		}
		if len(history.Caller) > 0 {
			var caller any
			if err := kitutil.Unmarshal(history.Caller, &caller); err != nil {
				return nil, fmt.Errorf("hosted_history[%d].caller: %w", index, err)
			}
			call["caller"] = caller
		}
		blocks = append(blocks, call)
		if len(history.Results) > 0 && !(set.Source == types.RelayFormatOpenAIResponses && history.Kind == KindWebSearch) {
			var content any
			if err := kitutil.Unmarshal(history.Results, &content); err != nil {
				return nil, fmt.Errorf("hosted_history[%d].results: %w", index, err)
			}
			blocks = append(blocks, map[string]any{
				"type":        claudeResultTypeFromHostedKind(history.Kind),
				"tool_use_id": id,
				"content":     content,
			})
		} else if len(history.Results) > 0 && history.Kind == KindWebSearch {
			diagnostics = append(diagnostics, presentationLoss(
				fmt.Sprintf("hosted_history[%d].results", index),
				"web_search_result_omitted",
				"Responses web-search source metadata cannot reconstruct Claude's encrypted web_search_tool_result",
			))
		}
		blocksBySourceIndex[history.MessageIndex] = append(blocksBySourceIndex[history.MessageIndex], blocks...)
	}
	if len(blocksBySourceIndex) == 0 {
		if len(diagnostics) > 0 {
			return diagnostics, nil
		}
		return unsupportedHostedHistoryDiagnostics(types.RelayFormatClaude, set.History), nil
	}
	messages := make([]dto.ClaudeMessage, 0, len(target.Messages)+len(blocksBySourceIndex))
	messageIndex := 0
	maxSourceIndex := 0
	for _, history := range set.History {
		if history.MessageIndex > maxSourceIndex {
			maxSourceIndex = history.MessageIndex
		}
	}
	for sourceIndex := 0; sourceIndex <= maxSourceIndex || messageIndex < len(target.Messages); sourceIndex++ {
		if blocks := blocksBySourceIndex[sourceIndex]; len(blocks) > 0 {
			role := "assistant"
			for _, history := range set.History {
				if history.MessageIndex == sourceIndex && history.Role != "" {
					role = history.Role
					break
				}
			}
			if !mixedBySourceIndex[sourceIndex] {
				messages = append(messages, dto.ClaudeMessage{Role: role, Content: blocks})
				continue
			}
			messages = append(messages, dto.ClaudeMessage{Role: role, Content: blocks})
			if messageIndex < len(target.Messages) {
				messages = append(messages, target.Messages[messageIndex])
				messageIndex++
			}
			diagnostics = append(diagnostics, semanticLoss(
				fmt.Sprintf("hosted_history[%d]", sourceIndex),
				"hosted_tool_order_unrepresentable",
				"hosted and ordinary content cannot be merged after the intermediate converter coalesced source blocks",
			))
			continue
		}
		if messageIndex < len(target.Messages) {
			messages = append(messages, target.Messages[messageIndex])
			messageIndex++
		}
	}
	target.Messages = messages
	diagnostics = append(diagnostics, presentationLoss(
		"messages",
		"hosted_tool_history_approximated",
		"hosted-tool continuation state is preserved, but provider-specific item fields may differ",
	))
	return diagnostics, nil
}

func responsesInputItems(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	switch kitutil.GetJsonType(raw) {
	case "array":
		var input []map[string]any
		if err := kitutil.Unmarshal(raw, &input); err != nil {
			return nil, fmt.Errorf("invalid Responses input: %w", err)
		}
		return input, nil
	case "string":
		var text string
		if err := kitutil.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("invalid Responses input: %w", err)
		}
		return []map[string]any{{"role": "user", "content": text}}, nil
	default:
		return nil, fmt.Errorf("cannot append hosted-tool history to Responses input type %q", kitutil.GetJsonType(raw))
	}
}

func unsupportedHostedHistoryDiagnostics(format types.RelayFormat, history []HostedHistoryItem) []types.ConversionDiagnostic {
	if len(history) == 0 {
		return nil
	}
	diagnostics := make([]types.ConversionDiagnostic, 0, len(history))
	for index, item := range history {
		diagnostics = append(diagnostics, semanticLoss(
			fmt.Sprintf("hosted_history[%d]", index),
			"hosted_tool_history_unsupported",
			fmt.Sprintf("%s cannot preserve hosted-tool continuation item %q", format, item.NativeType),
		))
	}
	return diagnostics
}

func openAIChatWebSearchDiagnostics(index int, search *WebSearch) []types.ConversionDiagnostic {
	if search == nil {
		return nil
	}
	path := fmt.Sprintf("tools[%d]", index)
	var diagnostics []types.ConversionDiagnostic
	if len(search.AllowedDomains) > 0 || len(search.BlockedDomains) > 0 {
		diagnostics = append(diagnostics, semanticLoss(path+".domains", "unsupported_domain_filter", "Chat Completions web_search_options cannot preserve domain access constraints"))
	}
	if search.MaxUses != nil {
		diagnostics = append(diagnostics, semanticLoss(path+".max_uses", "unsupported_search_limit", "Chat Completions cannot preserve Claude max_uses"))
	}
	if len(search.AllowedCallers) > 0 || search.ExternalWebAccess != nil {
		diagnostics = append(diagnostics, semanticLoss(path, "unsupported_search_controls", "Chat Completions cannot preserve caller or external-access constraints"))
	}
	if search.ResponseInclusion != "" || len(search.ReturnTokenBudget) > 0 {
		diagnostics = append(diagnostics, presentationLoss(path, "unsupported_search_tuning", "Chat Completions cannot preserve response-inclusion or return-token tuning"))
	}
	return diagnostics
}

func openAIResponsesWebSearchDiagnostics(index int, search *WebSearch) []types.ConversionDiagnostic {
	if search == nil {
		return nil
	}
	path := fmt.Sprintf("tools[%d]", index)
	var diagnostics []types.ConversionDiagnostic
	if search.MaxUses != nil {
		diagnostics = append(diagnostics, semanticLoss(path+".max_uses", "unsupported_search_limit", "OpenAI Responses cannot preserve Claude max_uses"))
	}
	if len(search.BlockedDomains) > 0 {
		diagnostics = append(diagnostics, semanticLoss(path+".blocked_domains", "unsupported_blocked_domains", "OpenAI Responses web search supports allow filters but not Claude blocked_domains"))
	}
	if len(search.AllowedCallers) > 0 {
		diagnostics = append(diagnostics, semanticLoss(path, "unsupported_search_controls", "OpenAI Responses cannot preserve Claude caller constraints"))
	}
	if search.ResponseInclusion != "" {
		diagnostics = append(diagnostics, presentationLoss(path+".response_inclusion", "unsupported_search_tuning", "OpenAI Responses cannot preserve Claude response-inclusion tuning"))
	}
	return diagnostics
}

func claudeWebSearchDiagnostics(index int, search *WebSearch) []types.ConversionDiagnostic {
	if search == nil {
		return nil
	}
	path := fmt.Sprintf("tools[%d]", index)
	var diagnostics []types.ConversionDiagnostic
	if search.SearchContextSize != "" {
		diagnostics = append(diagnostics, presentationLoss(path+".search_context_size", "unsupported_search_context_size", "Claude has no equivalent for OpenAI search_context_size; max_uses is deliberately not inferred"))
	}
	if search.ExternalWebAccess != nil {
		diagnostics = append(diagnostics, semanticLoss(path+".external_web_access", "unsupported_search_controls", "Claude cannot preserve OpenAI external-web access constraints"))
	}
	if len(search.ReturnTokenBudget) > 0 {
		diagnostics = append(diagnostics, presentationLoss(path+".return_token_budget", "unsupported_search_tuning", "Claude cannot preserve OpenAI return-token tuning"))
	}
	return diagnostics
}

func geminiWebSearchDiagnostics(index int, search *WebSearch) []types.ConversionDiagnostic {
	if search == nil {
		return nil
	}
	path := fmt.Sprintf("tools[%d]", index)
	if search.Location == nil && len(search.AllowedDomains) == 0 && len(search.BlockedDomains) == 0 && search.SearchContextSize == "" && search.MaxUses == nil && len(search.AllowedCallers) == 0 && search.ResponseInclusion == "" && search.ExternalWebAccess == nil && len(search.ReturnTokenBudget) == 0 {
		return nil
	}
	var diagnostics []types.ConversionDiagnostic
	if len(search.AllowedDomains) > 0 || len(search.BlockedDomains) > 0 || search.ExternalWebAccess != nil || search.MaxUses != nil || len(search.AllowedCallers) > 0 {
		diagnostics = append(diagnostics, semanticLoss(path, "unsupported_search_constraints", "Gemini Google Search cannot preserve source web-search access or execution constraints"))
	}
	if search.Location != nil || search.SearchContextSize != "" || search.ResponseInclusion != "" || len(search.ReturnTokenBudget) > 0 {
		diagnostics = append(diagnostics, presentationLoss(path, "unsupported_search_tuning", "Gemini Google Search cannot preserve source web-search location or result tuning"))
	}
	return diagnostics
}

func geminiNativeWebSearchDiagnostics(index int, definition Definition, target types.RelayFormat) []types.ConversionDiagnostic {
	path := fmt.Sprintf("tools[%d]", index)
	switch definition.NativeType {
	case "googleSearch":
		if !geminiNativeToolHasConfiguration(definition) {
			return nil
		}
		return []types.ConversionDiagnostic{semanticLoss(
			path+".googleSearch",
			"unsupported_native_search_config",
			fmt.Sprintf("%s cannot preserve Gemini googleSearch configuration", target),
		)}
	case "googleSearchRetrieval":
		return []types.ConversionDiagnostic{semanticLoss(
			path+".googleSearchRetrieval",
			"legacy_search_semantics_unrepresentable",
			fmt.Sprintf("%s cannot preserve Gemini legacy dynamic-retrieval semantics", target),
		)}
	case "enterpriseWebSearch":
		return []types.ConversionDiagnostic{semanticLoss(
			path+".enterpriseWebSearch",
			"enterprise_search_semantics_unrepresentable",
			fmt.Sprintf("%s cannot replace Gemini enterprise search with public web search without changing its data source", target),
		)}
	default:
		return []types.ConversionDiagnostic{semanticLoss(
			path,
			"unverified_search_mapping",
			fmt.Sprintf("%s has no verified mapping for Gemini search tool %q", target, definition.NativeType),
		)}
	}
}

func geminiNativeToolHasConfiguration(definition Definition) bool {
	if len(definition.Raw) == 0 {
		return false
	}
	var wrapper map[string]json.RawMessage
	if kitutil.Unmarshal(definition.Raw, &wrapper) != nil {
		return true
	}
	payload := wrapper[definition.NativeType]
	if !rawJSONPresent(payload) {
		return false
	}
	if kitutil.GetJsonType(payload) != "object" {
		return true
	}
	var fields map[string]json.RawMessage
	return kitutil.Unmarshal(payload, &fields) != nil || len(fields) > 0
}

func narrowAllowedFunctionChoice(choice *Choice, target types.RelayFormat) (*Choice, []types.ConversionDiagnostic) {
	if choice == nil || len(choice.AllowedNames) == 0 {
		return choice, nil
	}
	normalized := *choice
	normalized.AllowedNames = nil
	if len(choice.AllowedNames) == 1 {
		normalized.Mode = ChoiceNamed
		normalized.Kind = KindFunction
		normalized.Name = choice.AllowedNames[0]
		return &normalized, nil
	}
	return &normalized, []types.ConversionDiagnostic{semanticLoss(
		"tool_choice.allowed_function_names",
		"allowed_function_subset_unrepresentable",
		fmt.Sprintf("%s cannot restrict a required tool call to Gemini's %d-name function subset", target, len(choice.AllowedNames)),
	)}
}

func encodeOpenAIChatChoice(choice *Choice) (any, []types.ConversionDiagnostic) {
	if choice == nil {
		return nil, nil
	}
	switch choice.Mode {
	case ChoiceAuto:
		return "auto", nil
	case ChoiceNone:
		return "none", nil
	case ChoiceRequired:
		return "required", nil
	case ChoiceNamed:
		if choice.Kind == KindFunction {
			return map[string]any{"type": "function", "function": map[string]any{"name": choice.Name}}, nil
		}
		if choice.Kind == KindWebSearch {
			return nil, []types.ConversionDiagnostic{presentationLoss("tool_choice", "implicit_search_choice", "Chat Completions expresses hosted search through web_search_options instead of tool_choice")}
		}
	}
	return nil, []types.ConversionDiagnostic{semanticLoss("tool_choice", "unsupported_tool_choice", "Chat Completions cannot represent the source hosted tool choice")}
}

func encodeOpenAIResponsesChoice(choice *Choice, source types.RelayFormat) (json.RawMessage, []types.ConversionDiagnostic, error) {
	if choice == nil {
		return nil, nil, nil
	}
	var value any
	switch choice.Mode {
	case ChoiceAuto, ChoiceNone, ChoiceRequired:
		value = string(choice.Mode)
	case ChoiceNamed:
		if len(choice.Raw) > 0 && choice.NativeType != "" {
			return append(json.RawMessage(nil), choice.Raw...), nil, nil
		}
		switch choice.Kind {
		case KindFunction:
			value = map[string]any{"type": "function", "name": choice.Name}
		case KindWebSearch:
			value = map[string]any{"type": "web_search"}
		default:
			return nil, []types.ConversionDiagnostic{semanticLoss("tool_choice", "unsupported_tool_choice", "OpenAI Responses has no verified hosted tool-choice mapping")}, nil
		}
	case ChoiceOpaque:
		if source == types.RelayFormatOpenAIResponses && len(choice.Raw) > 0 {
			return append(json.RawMessage(nil), choice.Raw...), nil, nil
		}
		return nil, []types.ConversionDiagnostic{semanticLoss("tool_choice", "unsupported_tool_choice", "OpenAI Responses cannot reconstruct the source complex tool choice")}, nil
	}
	encoded, err := kitutil.Marshal(value)
	return encoded, nil, err
}

func encodeClaudeChoice(choice *Choice, parallelAllowed *bool, source types.RelayFormat) (any, []types.ConversionDiagnostic) {
	if choice == nil && parallelAllowed == nil {
		return nil, nil
	}
	if source == types.RelayFormatClaude && choice != nil && len(choice.Raw) > 0 {
		var value any
		if err := kitutil.Unmarshal(choice.Raw, &value); err != nil {
			return nil, []types.ConversionDiagnostic{semanticLoss("tool_choice", "invalid_native_tool_choice", "Claude tool_choice could not be restored from its native payload")}
		}
		return value, nil
	}
	value := map[string]any{}
	if choice != nil {
		switch choice.Mode {
		case ChoiceAuto:
			value["type"] = "auto"
		case ChoiceNone:
			value["type"] = "none"
		case ChoiceRequired:
			value["type"] = "any"
		case ChoiceNamed:
			if choice.Kind != KindFunction && choice.Kind != KindWebSearch {
				return nil, []types.ConversionDiagnostic{semanticLoss("tool_choice", "unsupported_tool_choice", "Claude has no verified hosted tool-choice mapping")}
			}
			value["type"] = "tool"
			value["name"] = choice.Name
		case ChoiceOpaque:
			return nil, []types.ConversionDiagnostic{semanticLoss("tool_choice", "unsupported_tool_choice", "Claude cannot represent the source complex tool-choice policy")}
		}
	}
	if value["type"] == nil && parallelAllowed != nil {
		value["type"] = "auto"
	}
	if parallelAllowed != nil && value["type"] != "none" {
		value["disable_parallel_tool_use"] = !*parallelAllowed
	} else if choice != nil && choice.DisableParallelToolUse != nil && value["type"] != "none" {
		value["disable_parallel_tool_use"] = *choice.DisableParallelToolUse
	}
	return value, nil
}

func encodeGeminiChoice(choice *Choice) (*dto.ToolConfig, []types.ConversionDiagnostic) {
	if choice == nil {
		return nil, nil
	}
	config := &dto.ToolConfig{FunctionCallingConfig: &dto.FunctionCallingConfig{}}
	if len(choice.AllowedNames) > 0 {
		config.FunctionCallingConfig.Mode = "ANY"
		config.FunctionCallingConfig.AllowedFunctionNames = append([]string(nil), choice.AllowedNames...)
		return config, nil
	}
	switch choice.Mode {
	case ChoiceAuto:
		config.FunctionCallingConfig.Mode = "AUTO"
	case ChoiceNone:
		config.FunctionCallingConfig.Mode = "NONE"
	case ChoiceRequired:
		config.FunctionCallingConfig.Mode = "ANY"
	case ChoiceNamed:
		if choice.Kind != KindFunction {
			return nil, []types.ConversionDiagnostic{semanticLoss("tool_choice", "unsupported_tool_choice", "Gemini generateContent does not expose an equivalent hosted-tool choice")}
		}
		config.FunctionCallingConfig.Mode = "ANY"
		config.FunctionCallingConfig.AllowedFunctionNames = []string{choice.Name}
	case ChoiceOpaque:
		return nil, []types.ConversionDiagnostic{semanticLoss("tool_choice", "unsupported_tool_choice", "Gemini cannot represent the source complex tool-choice policy")}
	}
	return config, nil
}

func semanticLoss(path string, code string, message string) types.ConversionDiagnostic {
	return types.ConversionDiagnostic{Code: code, Path: path, Message: message, Severity: types.ConversionDiagnosticError}
}

func presentationLoss(path string, code string, message string) types.ConversionDiagnostic {
	return types.ConversionDiagnostic{Code: code, Path: path, Message: message, Severity: types.ConversionDiagnosticWarning}
}

func locationMap(location *ApproximateLocation) map[string]any {
	value := map[string]any{
		"city":     location.City,
		"region":   location.Region,
		"country":  location.Country,
		"timezone": location.Timezone,
	}
	deleteEmptyStrings(value)
	return value
}

func deleteEmptyStrings(value map[string]any) {
	for key, item := range value {
		if text, ok := item.(string); ok && text == "" {
			delete(value, key)
		}
	}
}

func functionParametersMap(parameters any) (map[string]any, error) {
	if parameters == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}, nil
	}
	converted, err := kitutil.Any2Type[map[string]any](parameters)
	if err != nil {
		return nil, err
	}
	if converted["type"] == nil {
		converted["type"] = "object"
	}
	if converted["properties"] == nil {
		converted["properties"] = map[string]any{}
	}
	return converted, nil
}
