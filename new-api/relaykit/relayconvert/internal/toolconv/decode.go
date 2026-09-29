package toolconv

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

const maxClaudeWebSearchUses = 1000

func ExtractRequest(format types.RelayFormat, request any) (any, Set, error) {
	switch format {
	case types.RelayFormatOpenAI:
		return extractOpenAIChatRequest(request)
	case types.RelayFormatOpenAIResponses:
		return extractOpenAIResponsesRequest(request)
	case types.RelayFormatClaude:
		return extractClaudeRequest(request)
	case types.RelayFormatGemini:
		return extractGeminiRequest(request)
	default:
		return request, Set{Source: format}, nil
	}
}

func extractOpenAIChatRequest(request any) (any, Set, error) {
	source, ok := request.(*dto.GeneralOpenAIRequest)
	if !ok {
		value, valueOK := request.(dto.GeneralOpenAIRequest)
		if !valueOK {
			return nil, Set{}, fmt.Errorf("expected OpenAI chat completions request, got %T", request)
		}
		source = &value
	}

	set := Set{Source: types.RelayFormatOpenAI}
	set.ParallelAllowed = source.ParallelTooCalls
	if len(source.Functions) > 0 {
		var functions []dto.FunctionRequest
		if err := kitutil.Unmarshal(source.Functions, &functions); err != nil {
			return nil, Set{}, fmt.Errorf("invalid legacy functions: %w", err)
		}
		for _, function := range functions {
			set.Definitions = append(set.Definitions, Definition{
				Kind:      KindFunction,
				Execution: ExecutionClient,
				Function:  &Function{Name: function.Name, Description: function.Description, Parameters: function.Parameters, Strict: function.Strict},
			})
		}
	}
	for index, tool := range source.Tools {
		if tool.Type == "function" || tool.Type == "" {
			if definition, ok := decodeOpenAIChatPseudoHostedTool(tool.Function.Name); ok {
				set.Definitions = append(set.Definitions, definition)
				continue
			}
			set.Definitions = append(set.Definitions, Definition{
				Kind:      KindFunction,
				Execution: ExecutionClient,
				Function: &Function{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					Parameters:  tool.Function.Parameters,
					Strict:      tool.Function.Strict,
				},
			})
			continue
		}
		if len(tool.Custom) == 0 {
			return nil, Set{}, fmt.Errorf("tools[%d] has unsupported type %q without a native payload", index, tool.Type)
		}
		definition, err := decodeOpenAIResponsesDefinition(tool.Custom)
		if err != nil {
			return nil, Set{}, fmt.Errorf("tools[%d]: %w", index, err)
		}
		set.Definitions = append(set.Definitions, definition)
	}

	if source.WebSearchOptions != nil {
		webSearch := &WebSearch{
			SearchContextSize: source.WebSearchOptions.SearchContextSize,
		}
		location, err := decodeOpenAIChatLocation(source.WebSearchOptions.UserLocation)
		if err != nil {
			return nil, Set{}, err
		}
		webSearch.Location = location
		set.Definitions = append(set.Definitions, Definition{
			Kind:       KindWebSearch,
			Execution:  ExecutionServer,
			NativeType: "web_search_options",
			WebSearch:  webSearch,
		})
	}

	if choice, err := decodeOpenAIChatChoice(source.ToolChoice); err != nil {
		return nil, Set{}, err
	} else if choice != nil {
		set.Choice = choice
	}
	if len(source.FunctionCall) > 0 {
		legacyChoice, err := decodeLegacyOpenAIFunctionChoice(source.FunctionCall)
		if err != nil {
			return nil, Set{}, err
		}
		if set.Choice != nil && legacyChoice != nil {
			return nil, Set{}, fmt.Errorf("tool_choice and legacy function_call cannot both be converted")
		}
		set.Choice = legacyChoice
	}

	clone := *source
	clone.Tools = nil
	clone.ToolChoice = nil
	clone.WebSearchOptions = nil
	clone.Functions = nil
	clone.FunctionCall = nil
	clone.ParallelTooCalls = nil
	return &clone, set, nil
}

func extractOpenAIResponsesRequest(request any) (any, Set, error) {
	source, ok := request.(*dto.OpenAIResponsesRequest)
	if !ok {
		value, valueOK := request.(dto.OpenAIResponsesRequest)
		if !valueOK {
			return nil, Set{}, fmt.Errorf("expected OpenAI Responses request, got %T", request)
		}
		source = &value
	}

	set := Set{Source: types.RelayFormatOpenAIResponses}
	set.ParallelAllowed = rawBoolPointer(source.ParallelToolCalls)
	if len(source.Tools) > 0 {
		var rawTools []json.RawMessage
		if err := kitutil.Unmarshal(source.Tools, &rawTools); err != nil {
			return nil, Set{}, fmt.Errorf("invalid Responses tools: %w", err)
		}
		for index, rawTool := range rawTools {
			definition, err := decodeOpenAIResponsesDefinition(rawTool)
			if err != nil {
				return nil, Set{}, fmt.Errorf("tools[%d]: %w", index, err)
			}
			set.Definitions = append(set.Definitions, definition)
		}
	}
	choice, err := decodeOpenAIResponsesChoice(source.ToolChoice)
	if err != nil {
		return nil, Set{}, err
	}
	set.Choice = choice

	clone := *source
	clone.Tools = nil
	clone.ToolChoice = nil
	clone.ParallelToolCalls = nil
	sanitizedInput, history, err := extractOpenAIResponsesHostedHistory(source.Input)
	if err != nil {
		return nil, Set{}, err
	}
	clone.Input = sanitizedInput
	set.History = history
	return &clone, set, nil
}

func extractClaudeRequest(request any) (any, Set, error) {
	source, ok := request.(*dto.ClaudeRequest)
	if !ok {
		value, valueOK := request.(dto.ClaudeRequest)
		if !valueOK {
			return nil, Set{}, fmt.Errorf("expected Claude Messages request, got %T", request)
		}
		source = &value
	}

	set := Set{Source: types.RelayFormatClaude}
	if source.ToolChoice != nil {
		rawChoice, rawErr := rawJSON(source.ToolChoice)
		if rawErr == nil {
			var choiceMap map[string]any
			if kitutil.Unmarshal(rawChoice, &choiceMap) == nil {
				if disabled, ok := choiceMap["disable_parallel_tool_use"].(bool); ok {
					allowed := !disabled
					set.ParallelAllowed = &allowed
				}
			}
		}
	}
	if source.Tools != nil {
		rawTools, err := rawJSON(source.Tools)
		if err != nil {
			return nil, Set{}, fmt.Errorf("invalid Claude tools: %w", err)
		}
		var tools []json.RawMessage
		if err := kitutil.Unmarshal(rawTools, &tools); err != nil {
			return nil, Set{}, fmt.Errorf("invalid Claude tools: %w", err)
		}
		for index, rawTool := range tools {
			definition, err := decodeClaudeDefinition(rawTool)
			if err != nil {
				return nil, Set{}, fmt.Errorf("tools[%d]: %w", index, err)
			}
			set.Definitions = append(set.Definitions, definition)
		}
	}
	choice, err := decodeClaudeChoice(source.ToolChoice, set.Definitions)
	if err != nil {
		return nil, Set{}, err
	}
	set.Choice = choice

	clone := *source
	clone.Tools = nil
	clone.ToolChoice = nil
	clone.Messages, set.History, err = extractClaudeHostedHistory(source.Messages)
	if err != nil {
		return nil, Set{}, err
	}
	return &clone, set, nil
}

func extractGeminiRequest(request any) (any, Set, error) {
	source, ok := request.(*dto.GeminiChatRequest)
	if !ok {
		value, valueOK := request.(dto.GeminiChatRequest)
		if !valueOK {
			return nil, Set{}, fmt.Errorf("expected Gemini generateContent request, got %T", request)
		}
		source = &value
	}

	set := Set{Source: types.RelayFormatGemini}
	if source.ToolConfig != nil {
		set.NativeToolConfig, _ = rawJSON(source.ToolConfig)
	}
	if len(source.Tools) > 0 {
		var tools []json.RawMessage
		if err := kitutil.Unmarshal(source.Tools, &tools); err != nil {
			return nil, Set{}, fmt.Errorf("invalid Gemini tools: %w", err)
		}
		for index, rawTool := range tools {
			definitions, err := decodeGeminiDefinitions(rawTool)
			if err != nil {
				return nil, Set{}, fmt.Errorf("tools[%d]: %w", index, err)
			}
			for definitionIndex := range definitions {
				definitions[definitionIndex].Group = index
			}
			set.Definitions = append(set.Definitions, definitions...)
		}
	}
	set.Choice = decodeGeminiChoice(source.ToolConfig)

	clone := *source
	clone.Tools = nil
	clone.ToolConfig = nil
	return &clone, set, nil
}

func decodeOpenAIResponsesDefinition(raw json.RawMessage) (Definition, error) {
	var tool map[string]any
	if err := kitutil.Unmarshal(raw, &tool); err != nil {
		return Definition{}, err
	}
	toolType := strings.TrimSpace(kitutil.Interface2String(tool["type"]))
	if toolType == "function" {
		return Definition{
			Kind:      KindFunction,
			Execution: ExecutionClient,
			Name:      strings.TrimSpace(kitutil.Interface2String(tool["name"])),
			Raw:       cloneRaw(raw),
			Function: &Function{
				Name:        strings.TrimSpace(kitutil.Interface2String(tool["name"])),
				Description: kitutil.Interface2String(tool["description"]),
				Parameters:  tool["parameters"],
				Strict:      boolPointer(tool, "strict"),
			},
		}, nil
	}
	if isOpenAIResponsesWebSearchType(toolType) {
		webSearch := &WebSearch{
			SearchContextSize: strings.TrimSpace(kitutil.Interface2String(tool["search_context_size"])),
			ExternalWebAccess: boolPointer(tool, "external_web_access"),
		}
		if value, exists := tool["return_token_budget"]; exists {
			encoded, err := rawJSON(value)
			if err != nil {
				return Definition{}, err
			}
			webSearch.ReturnTokenBudget = encoded
		}
		if filters, ok := tool["filters"].(map[string]any); ok {
			webSearch.AllowedDomains = stringSlice(filters["allowed_domains"])
		}
		if location, ok := tool["user_location"].(map[string]any); ok {
			webSearch.Location = locationFromMap(location)
		}
		return Definition{
			Kind:       KindWebSearch,
			Execution:  ExecutionServer,
			NativeType: toolType,
			WebSearch:  webSearch,
			Raw:        cloneRaw(raw),
		}, nil
	}
	return Definition{
		Kind:       kindFromNativeType(toolType),
		Execution:  executionFromNativeType(toolType),
		NativeType: toolType,
		Raw:        cloneRaw(raw),
	}, nil
}

func decodeClaudeDefinition(raw json.RawMessage) (Definition, error) {
	var tool map[string]any
	if err := kitutil.Unmarshal(raw, &tool); err != nil {
		return Definition{}, err
	}
	toolType := strings.TrimSpace(kitutil.Interface2String(tool["type"]))
	if strings.HasPrefix(toolType, "web_search") {
		if !isVersionedClaudeWebSearchType(toolType) {
			return Definition{}, fmt.Errorf("invalid Claude web-search tool version %q", toolType)
		}
		if !isKnownClaudeWebSearchType(toolType) {
			return Definition{
				Kind:       KindNative,
				Execution:  ExecutionServer,
				NativeType: toolType,
				Name:       strings.TrimSpace(kitutil.Interface2String(tool["name"])),
				Raw:        cloneRaw(raw),
			}, nil
		}
		toolName := strings.TrimSpace(kitutil.Interface2String(tool["name"]))
		if toolName != "web_search" {
			return Definition{}, fmt.Errorf("Claude web-search tool name must be %q", "web_search")
		}
		webSearch := &WebSearch{
			AllowedDomains:    stringSlice(tool["allowed_domains"]),
			BlockedDomains:    stringSlice(tool["blocked_domains"]),
			AllowedCallers:    stringSlice(tool["allowed_callers"]),
			ResponseInclusion: strings.TrimSpace(kitutil.Interface2String(tool["response_inclusion"])),
		}
		if _, exists := tool["max_uses"]; exists {
			var fields struct {
				MaxUses *int `json:"max_uses"`
			}
			if err := kitutil.Unmarshal(raw, &fields); err != nil || fields.MaxUses == nil {
				return Definition{}, fmt.Errorf("max_uses must be a JSON integer")
			}
			if *fields.MaxUses <= 0 || *fields.MaxUses > maxClaudeWebSearchUses {
				return Definition{}, fmt.Errorf("max_uses must be between 1 and %d", maxClaudeWebSearchUses)
			}
			webSearch.MaxUses = fields.MaxUses
		}
		if len(webSearch.AllowedDomains) > 0 && len(webSearch.BlockedDomains) > 0 {
			return Definition{}, fmt.Errorf("allowed_domains and blocked_domains are mutually exclusive")
		}
		if webSearch.ResponseInclusion != "" && !claudeWebSearchSupportsResponseInclusion(toolType) {
			return Definition{}, fmt.Errorf("response_inclusion requires Claude web_search_20260318")
		}
		if location, ok := tool["user_location"].(map[string]any); ok {
			webSearch.Location = locationFromMap(location)
		}
		return Definition{
			Kind:       KindWebSearch,
			Execution:  ExecutionServer,
			NativeType: toolType,
			Name:       toolName,
			WebSearch:  webSearch,
			Raw:        cloneRaw(raw),
		}, nil
	}
	if toolType == "" {
		return Definition{
			Kind:      KindFunction,
			Execution: ExecutionClient,
			Name:      strings.TrimSpace(kitutil.Interface2String(tool["name"])),
			Raw:       cloneRaw(raw),
			Function: &Function{
				Name:        strings.TrimSpace(kitutil.Interface2String(tool["name"])),
				Description: kitutil.Interface2String(tool["description"]),
				Parameters:  tool["input_schema"],
				Strict:      boolPointer(tool, "strict"),
			},
		}, nil
	}
	return Definition{
		Kind:       kindFromNativeType(toolType),
		Execution:  executionFromNativeType(toolType),
		NativeType: toolType,
		Name:       strings.TrimSpace(kitutil.Interface2String(tool["name"])),
		Raw:        cloneRaw(raw),
	}, nil
}

func decodeGeminiDefinitions(raw json.RawMessage) ([]Definition, error) {
	var tool map[string]any
	if err := kitutil.Unmarshal(raw, &tool); err != nil {
		return nil, err
	}
	definitions := make([]Definition, 0)
	if functions, ok := tool["functionDeclarations"].([]any); ok {
		for _, value := range functions {
			function, ok := value.(map[string]any)
			if !ok {
				continue
			}
			parameters := function["parameters"]
			parametersJSONSchema, hasParametersJSONSchema := function["parametersJsonSchema"]
			if parameters != nil && hasParametersJSONSchema && parametersJSONSchema != nil {
				return nil, fmt.Errorf("function %q declares both parameters and parametersJsonSchema", strings.TrimSpace(kitutil.Interface2String(function["name"])))
			}
			if parameters == nil && hasParametersJSONSchema {
				parameters = parametersJSONSchema
			}
			functionRaw, err := rawJSON(map[string]any{"functionDeclarations": []any{value}})
			if err != nil {
				return nil, err
			}
			definitions = append(definitions, Definition{
				Kind:      KindFunction,
				Execution: ExecutionClient,
				Name:      strings.TrimSpace(kitutil.Interface2String(function["name"])),
				Raw:       functionRaw,
				Function: &Function{
					Name:        strings.TrimSpace(kitutil.Interface2String(function["name"])),
					Description: kitutil.Interface2String(function["description"]),
					Parameters:  parameters,
				},
			})
		}
	}
	for key := range tool {
		var kind Kind
		var nativeType string
		switch key {
		case "functionDeclarations":
			continue
		case "googleSearch":
			kind, nativeType = KindWebSearch, "googleSearch"
		case "googleSearchRetrieval":
			kind, nativeType = KindWebSearch, "googleSearchRetrieval"
		case "enterpriseWebSearch":
			kind, nativeType = KindWebSearch, "enterpriseWebSearch"
		case "googleMaps":
			kind, nativeType = KindNative, "googleMaps"
		case "codeExecution":
			kind, nativeType = KindCodeExecution, "codeExecution"
		case "urlContext":
			kind, nativeType = KindURLContext, "urlContext"
		case "fileSearch":
			kind, nativeType = KindFileSearch, "fileSearch"
		case "computerUse":
			kind, nativeType = KindComputerUse, "computerUse"
		case "retrieval":
			kind, nativeType = KindFileSearch, "retrieval"
		default:
			kind, nativeType = KindNative, key
		}
		keyRaw, err := rawJSON(map[string]any{key: tool[key]})
		if err != nil {
			return nil, err
		}
		definition := Definition{
			Kind:       kind,
			Execution:  ExecutionServer,
			NativeType: nativeType,
			Name:       strings.TrimSpace(kitutil.Interface2String(tool["name"])),
			Raw:        keyRaw,
		}
		if kind == KindWebSearch {
			definition.WebSearch = &WebSearch{}
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func decodeLegacyOpenAIFunctionChoice(raw json.RawMessage) (*Choice, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if kitutil.GetJsonType(raw) == "string" {
		var value string
		if err := kitutil.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		return choiceFromString(value), nil
	}
	var value map[string]any
	if err := kitutil.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("invalid legacy function_call: %w", err)
	}
	name := strings.TrimSpace(kitutil.Interface2String(value["name"]))
	if name == "" {
		return nil, fmt.Errorf("legacy function_call requires name")
	}
	return &Choice{Mode: ChoiceNamed, Kind: KindFunction, Name: name}, nil
}

func rawBoolPointer(raw json.RawMessage) *bool {
	if len(raw) == 0 || kitutil.GetJsonType(raw) != "boolean" {
		return nil
	}
	var value bool
	if kitutil.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

// decodeOpenAIChatPseudoHostedTool recognizes the OpenAI Chat dialect that
// declares Gemini hosted tools as function definitions named googleSearch,
// codeExecution, or urlContext. The names are the historical public contract;
// recognition lives here so every target format goes through the same hosted
// ToolDefinition pipeline.
func decodeOpenAIChatPseudoHostedTool(name string) (Definition, bool) {
	switch name {
	case "googleSearch":
		return Definition{
			Kind:       KindWebSearch,
			Execution:  ExecutionServer,
			NativeType: "googleSearch",
			Name:       "googleSearch",
			WebSearch:  &WebSearch{},
		}, true
	case "codeExecution":
		return Definition{
			Kind:       KindCodeExecution,
			Execution:  ExecutionServer,
			NativeType: "codeExecution",
			Name:       "codeExecution",
		}, true
	case "urlContext":
		return Definition{
			Kind:       KindURLContext,
			Execution:  ExecutionServer,
			NativeType: "urlContext",
			Name:       "urlContext",
		}, true
	default:
		return Definition{}, false
	}
}

func decodeOpenAIChatLocation(raw json.RawMessage) (*ApproximateLocation, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var wrapper map[string]any
	if err := kitutil.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("invalid web_search_options.user_location: %w", err)
	}
	location, ok := wrapper["approximate"].(map[string]any)
	if !ok {
		return nil, nil
	}
	return locationFromMap(location), nil
}

func decodeOpenAIChatChoice(value any) (*Choice, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		return choiceFromString(text), nil
	}
	raw, err := rawJSON(value)
	if err != nil {
		return nil, fmt.Errorf("invalid Chat tool_choice: %w", err)
	}
	var choice map[string]any
	if err := kitutil.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("invalid Chat tool_choice: %w", err)
	}
	if strings.TrimSpace(kitutil.Interface2String(choice["type"])) != "function" {
		return &Choice{Mode: ChoiceOpaque, Raw: cloneRaw(raw)}, nil
	}
	function, _ := choice["function"].(map[string]any)
	name := strings.TrimSpace(kitutil.Interface2String(function["name"]))
	if name == "" {
		return nil, fmt.Errorf("Chat function tool_choice requires function.name")
	}
	return &Choice{Mode: ChoiceNamed, Kind: KindFunction, Name: name}, nil
}

func decodeOpenAIResponsesChoice(raw json.RawMessage) (*Choice, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if kitutil.GetJsonType(raw) == "string" {
		var text string
		if err := kitutil.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		return choiceFromString(text), nil
	}
	var value map[string]any
	if err := kitutil.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("invalid Responses tool_choice: %w", err)
	}
	toolType := strings.TrimSpace(kitutil.Interface2String(value["type"]))
	if toolType == "function" {
		name := strings.TrimSpace(kitutil.Interface2String(value["name"]))
		if name == "" {
			return nil, fmt.Errorf("Responses function tool_choice requires name")
		}
		return &Choice{Mode: ChoiceNamed, Kind: KindFunction, Name: name}, nil
	}
	if isOpenAIResponsesWebSearchType(toolType) {
		return &Choice{Mode: ChoiceNamed, Kind: KindWebSearch, Name: "web_search", NativeType: toolType, Raw: cloneRaw(raw)}, nil
	}
	return &Choice{Mode: ChoiceOpaque, Kind: kindFromNativeType(toolType), NativeType: toolType, Raw: cloneRaw(raw)}, nil
}

func decodeClaudeChoice(value any, definitions []Definition) (*Choice, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := rawJSON(value)
	if err != nil {
		return nil, fmt.Errorf("invalid Claude tool_choice: %w", err)
	}
	var choice map[string]any
	if err := kitutil.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("invalid Claude tool_choice: %w", err)
	}
	choiceType := strings.TrimSpace(kitutil.Interface2String(choice["type"]))
	var decoded *Choice
	switch choiceType {
	case "auto":
		decoded = &Choice{Mode: ChoiceAuto}
	case "none":
		decoded = &Choice{Mode: ChoiceNone}
	case "any":
		decoded = &Choice{Mode: ChoiceRequired}
	case "tool":
		name := strings.TrimSpace(kitutil.Interface2String(choice["name"]))
		kind := KindNative
		matches := 0
		for _, definition := range definitions {
			definitionName := definition.Name
			if definition.Kind == KindFunction && definition.Function != nil {
				definitionName = definition.Function.Name
			}
			if definitionName != name {
				continue
			}
			matches++
			kind = definition.Kind
		}
		if matches > 1 {
			return nil, fmt.Errorf("Claude tool_choice name %q is ambiguous across %d definitions", name, matches)
		}
		decoded = &Choice{Mode: ChoiceNamed, Kind: kind, Name: name}
	default:
		decoded = &Choice{Mode: ChoiceOpaque}
	}
	if disabled, ok := choice["disable_parallel_tool_use"].(bool); ok {
		decoded.DisableParallelToolUse = &disabled
	}
	decoded.Raw = cloneRaw(raw)
	return decoded, nil
}

func decodeGeminiChoice(config *dto.ToolConfig) *Choice {
	if config == nil || config.FunctionCallingConfig == nil {
		return nil
	}
	functionConfig := config.FunctionCallingConfig
	switch strings.ToUpper(strings.TrimSpace(string(functionConfig.Mode))) {
	case "NONE":
		return &Choice{Mode: ChoiceNone}
	case "ANY":
		if len(functionConfig.AllowedFunctionNames) == 1 {
			return &Choice{Mode: ChoiceNamed, Kind: KindFunction, Name: functionConfig.AllowedFunctionNames[0]}
		}
		return &Choice{
			Mode:         ChoiceRequired,
			Kind:         KindFunction,
			AllowedNames: append([]string(nil), functionConfig.AllowedFunctionNames...),
		}
	case "", "AUTO":
		return &Choice{Mode: ChoiceAuto}
	default:
		raw, _ := rawJSON(functionConfig)
		return &Choice{Mode: ChoiceOpaque, Raw: raw}
	}
}

func choiceFromString(value string) *Choice {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none":
		return &Choice{Mode: ChoiceNone}
	case "required", "any":
		return &Choice{Mode: ChoiceRequired}
	case "auto":
		return &Choice{Mode: ChoiceAuto}
	default:
		raw, _ := rawJSON(value)
		return &Choice{Mode: ChoiceOpaque, Raw: raw}
	}
}

func isOpenAIResponsesWebSearchType(toolType string) bool {
	switch toolType {
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		return true
	default:
		return false
	}
}

func claudeWebSearchSupportsResponseInclusion(toolType string) bool {
	return toolType == "web_search_20260318"
}

func isKnownClaudeWebSearchType(toolType string) bool {
	switch toolType {
	case "web_search_20250305", "web_search_20260209", "web_search_20260318":
		return true
	default:
		return false
	}
}

func isVersionedClaudeWebSearchType(toolType string) bool {
	const prefix = "web_search_"
	version := strings.TrimPrefix(toolType, prefix)
	if !strings.HasPrefix(toolType, prefix) || len(version) != 8 {
		return false
	}
	_, err := strconv.ParseUint(version, 10, 32)
	return err == nil
}

func locationFromMap(value map[string]any) *ApproximateLocation {
	if len(value) == 0 {
		return nil
	}
	location := &ApproximateLocation{
		City:     strings.TrimSpace(kitutil.Interface2String(value["city"])),
		Region:   strings.TrimSpace(kitutil.Interface2String(value["region"])),
		Country:  strings.TrimSpace(kitutil.Interface2String(value["country"])),
		Timezone: strings.TrimSpace(kitutil.Interface2String(value["timezone"])),
	}
	if location.City == "" && location.Region == "" && location.Country == "" && location.Timezone == "" {
		return nil
	}
	return location
}

func boolPointer(value map[string]any, key string) *bool {
	raw, exists := value[key]
	if !exists {
		return nil
	}
	parsed, ok := raw.(bool)
	if !ok {
		return nil
	}
	return &parsed
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, stringsOK := value.([]string); stringsOK {
			return append([]string(nil), strings...)
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, text)
		}
	}
	return result
}

func rawJSON(value any) (json.RawMessage, error) {
	switch raw := value.(type) {
	case json.RawMessage:
		return cloneRaw(raw), nil
	case []byte:
		return cloneRaw(raw), nil
	default:
		encoded, err := kitutil.Marshal(value)
		return json.RawMessage(encoded), err
	}
}

func cloneRaw(raw []byte) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func kindFromNativeType(toolType string) Kind {
	switch {
	case toolType == "file_search":
		return KindFileSearch
	case strings.HasPrefix(toolType, "web_fetch"):
		return KindWebFetch
	case toolType == "code_interpreter", strings.HasPrefix(toolType, "code_execution"):
		return KindCodeExecution
	case strings.Contains(toolType, "computer"):
		return KindComputerUse
	case toolType == "url_context":
		return KindURLContext
	case toolType == "mcp", toolType == "mcp_toolset":
		return KindMCP
	case toolType == "image_generation":
		return KindImage
	default:
		return KindNative
	}
}

func executionFromNativeType(toolType string) Execution {
	if strings.HasPrefix(toolType, "computer_") || strings.HasPrefix(toolType, "bash_") || strings.HasPrefix(toolType, "text_editor_") || strings.HasPrefix(toolType, "memory_") {
		return ExecutionClient
	}
	return ExecutionServer
}

func extractOpenAIResponsesHostedHistory(input json.RawMessage) (json.RawMessage, []HostedHistoryItem, error) {
	if len(input) == 0 || kitutil.GetJsonType(input) != "array" {
		return input, nil, nil
	}
	var rawItems []json.RawMessage
	if err := kitutil.Unmarshal(input, &rawItems); err != nil {
		return nil, nil, fmt.Errorf("invalid Responses input: %w", err)
	}
	filtered := make([]json.RawMessage, 0, len(rawItems))
	var history []HostedHistoryItem
	for index, rawItem := range rawItems {
		var item map[string]any
		if err := kitutil.Unmarshal(rawItem, &item); err != nil {
			return nil, nil, fmt.Errorf("input[%d]: %w", index, err)
		}
		itemType := strings.TrimSpace(kitutil.Interface2String(item["type"]))
		if !isResponsesHostedHistoryType(itemType) {
			filtered = append(filtered, rawItem)
			continue
		}
		status := strings.TrimSpace(kitutil.Interface2String(item["status"]))
		action := rawMapValue(item, "action")
		results := firstRawMapValue(item, "results", "sources", "output")
		if itemType == "mcp_call" {
			action = rawMapValue(item, "arguments")
			output := rawMapValue(item, "output")
			itemError := rawMapValue(item, "error")
			results = output
			if rawJSONPresent(itemError) {
				results = itemError
				status = "failed"
			}
		}
		history = append(history, HostedHistoryItem{
			Kind:         hostedKindFromResponsesType(itemType),
			NativeType:   itemType,
			Role:         strings.TrimSpace(kitutil.Interface2String(item["role"])),
			MessageIndex: index,
			Sequence:     index,
			ID:           strings.TrimSpace(kitutil.Interface2String(item["id"])),
			CallID:       strings.TrimSpace(kitutil.Interface2String(item["call_id"])),
			Name:         strings.TrimSpace(kitutil.Interface2String(item["name"])),
			ServerName:   strings.TrimSpace(kitutil.Interface2String(item["server_label"])),
			Status:       status,
			Action:       action,
			Results:      results,
			Caller:       rawMapValue(item, "caller"),
			Raw:          cloneRaw(rawItem),
		})
	}
	if len(history) == 0 {
		return input, nil, nil
	}
	encoded, err := kitutil.Marshal(filtered)
	if err != nil {
		return nil, nil, err
	}
	return encoded, history, nil
}

func isResponsesHostedHistoryType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "web_search_call", "file_search_call", "code_interpreter_call", "computer_call", "computer_call_output", "image_generation_call", "local_shell_call", "local_shell_call_output", "apply_patch_call", "apply_patch_call_output", "mcp_call", "mcp_list_tools", "mcp_approval_request", "mcp_approval_response":
		return true
	default:
		return false
	}
}

func extractClaudeHostedHistory(messages []dto.ClaudeMessage) ([]dto.ClaudeMessage, []HostedHistoryItem, error) {
	clonedMessages := make([]dto.ClaudeMessage, 0, len(messages))
	var history []HostedHistoryItem
	for messageIndex := range messages {
		message := messages[messageIndex]
		if message.IsStringContent() {
			clonedMessages = append(clonedMessages, message)
			continue
		}
		rawContent, err := rawJSON(message.Content)
		if err != nil {
			return nil, nil, fmt.Errorf("messages[%d].content: %w", messageIndex, err)
		}
		var blocks []json.RawMessage
		if err := kitutil.Unmarshal(rawContent, &blocks); err != nil {
			return nil, nil, fmt.Errorf("messages[%d].content: %w", messageIndex, err)
		}
		filtered := make([]any, 0, len(blocks))
		historyStart := len(history)
		for blockIndex, rawBlock := range blocks {
			var block map[string]any
			if err := kitutil.Unmarshal(rawBlock, &block); err != nil {
				return nil, nil, fmt.Errorf("messages[%d].content[%d]: %w", messageIndex, blockIndex, err)
			}
			blockType := strings.TrimSpace(kitutil.Interface2String(block["type"]))
			if blockType != "server_tool_use" && blockType != "mcp_tool_use" && !isClaudeHostedToolBlock(blockType) {
				filtered = append(filtered, block)
				continue
			}
			name := strings.TrimSpace(kitutil.Interface2String(block["name"]))
			kind := hostedKindFromClaudeCall(blockType, name)
			if strings.HasSuffix(blockType, "_tool_result") {
				kind = hostedKindFromClaudeResult(blockType)
			}
			results := rawMapValue(block, "content")
			status := "in_progress"
			if strings.HasSuffix(blockType, "_tool_result") {
				status = "completed"
				isError, _ := block["is_error"].(bool)
				failed, _ := claudeHostedResultFailure(
					blockType,
					results,
					&isError,
					strings.TrimSpace(kitutil.Interface2String(block["error_code"])),
				)
				if failed {
					status = "failed"
				}
			}
			history = append(history, HostedHistoryItem{
				Kind:         kind,
				NativeType:   blockType,
				Role:         message.Role,
				MessageIndex: messageIndex,
				BlockIndex:   blockIndex,
				Sequence:     len(history),
				ID:           strings.TrimSpace(kitutil.Interface2String(block["id"])),
				CallID:       strings.TrimSpace(kitutil.Interface2String(block["tool_use_id"])),
				Name:         name,
				ServerName:   strings.TrimSpace(kitutil.Interface2String(block["server_name"])),
				Status:       status,
				Action:       rawMapValue(block, "input"),
				Results:      results,
				Caller:       rawMapValue(block, "caller"),
				Raw:          cloneRaw(rawBlock),
			})
		}
		if len(filtered) > 0 {
			for index := historyStart; index < len(history); index++ {
				history[index].MessageHasRegular = true
			}
			message.Content = filtered
			clonedMessages = append(clonedMessages, message)
		}
	}
	return clonedMessages, history, nil
}

func rawMapValue(value map[string]any, key string) json.RawMessage {
	item, exists := value[key]
	if !exists {
		return nil
	}
	encoded, err := kitutil.Marshal(item)
	if err != nil {
		return nil
	}
	return encoded
}

func firstRawMapValue(value map[string]any, keys ...string) json.RawMessage {
	for _, key := range keys {
		if raw := rawMapValue(value, key); len(raw) > 0 {
			return raw
		}
	}
	return nil
}
