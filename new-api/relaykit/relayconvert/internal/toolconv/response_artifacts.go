package toolconv

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	geminichat "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/gemini_chat"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type HostedResponseItem struct {
	Kind              Kind
	NativeType        string
	ID                string
	CallID            string
	Name              string
	Status            string
	Position          int
	Action            json.RawMessage
	Results           json.RawMessage
	Sources           json.RawMessage
	Caller            json.RawMessage
	Arguments         json.RawMessage
	Output            json.RawMessage
	Error             json.RawMessage
	ServerName        string
	IsError           *bool
	ApprovalRequestID string
	Tools             json.RawMessage
	ErrorCode         string
	Raw               json.RawMessage
}

type HostedResponseSet struct {
	Source           types.RelayFormat
	Items            []HostedResponseItem
	SourceLength     int
	RegularPositions []int
}

type positionedResponsesOutput struct {
	position int
	output   dto.ResponsesOutput
}

type positionedClaudeBlocks struct {
	position int
	blocks   []dto.ClaudeMediaMessage
}

func (s HostedResponseSet) Empty() bool {
	return len(s.Items) == 0
}

// ExtractHostedResponse removes server-executed tool artifacts before a
// message converter sees them. The artifacts travel beside multi-step routes,
// just like request tool definitions, so a lossy Chat pivot cannot reclassify
// or discard them.
func ExtractHostedResponse(format types.RelayFormat, response any) (any, HostedResponseSet, error) {
	switch format {
	case types.RelayFormatClaude:
		return extractClaudeHostedResponse(response)
	case types.RelayFormatOpenAIResponses:
		return extractOpenAIHostedResponse(response)
	case types.RelayFormatGemini:
		return extractGeminiHostedResponse(response)
	default:
		return response, HostedResponseSet{Source: format}, nil
	}
}

func AttachHostedResponse(format types.RelayFormat, response any, set HostedResponseSet, options *convmeta.Options) (any, []types.ConversionDiagnostic, error) {
	if set.Empty() {
		return response, nil, nil
	}
	var (
		value       any
		diagnostics []types.ConversionDiagnostic
		err         error
	)
	switch format {
	case types.RelayFormatOpenAIResponses:
		value, diagnostics, err = attachOpenAIHostedResponse(response, set)
	case types.RelayFormatClaude:
		value, diagnostics, err = attachClaudeHostedResponse(response, set)
	default:
		value = response
		for index, item := range set.Items {
			diagnostics = append(diagnostics, responsePresentationLoss(
				fmt.Sprintf("hosted_tools[%d]", index),
				"hosted_tool_event_omitted",
				fmt.Sprintf("%s cannot represent hosted-tool response %q", format, item.NativeType),
			))
		}
	}
	if err != nil {
		return nil, diagnostics, err
	}
	for index := range diagnostics {
		diagnostics[index].From = set.Source
		diagnostics[index].To = format
	}
	return value, diagnostics, nil
}

func extractClaudeHostedResponse(response any) (any, HostedResponseSet, error) {
	var source *dto.ClaudeResponse
	switch value := response.(type) {
	case *dto.ClaudeResponse:
		source = value
	case dto.ClaudeResponse:
		source = &value
	default:
		return nil, HostedResponseSet{}, fmt.Errorf("expected Claude response, got %T", response)
	}
	clone := *source
	clone.Content = make([]dto.ClaudeMediaMessage, 0, len(source.Content))
	set := HostedResponseSet{Source: types.RelayFormatClaude, SourceLength: len(source.Content)}
	for position := range source.Content {
		block := source.Content[position]
		blockType := strings.TrimSpace(block.Type)
		switch {
		case blockType == "server_tool_use" || blockType == "mcp_tool_use":
			rawBlock, err := kitutil.Marshal(block)
			if err != nil {
				return nil, set, fmt.Errorf("content[%d]: %w", position, err)
			}
			action, err := kitutil.Marshal(block.Input)
			if err != nil {
				return nil, set, fmt.Errorf("content[%d].input: %w", position, err)
			}
			item := HostedResponseItem{
				Kind:       hostedKindFromClaudeCall(blockType, block.Name),
				NativeType: blockType,
				ID:         block.Id,
				CallID:     block.Id,
				Name:       block.Name,
				Status:     "in_progress",
				Position:   position,
				Action:     action,
				Caller:     append(json.RawMessage(nil), block.Caller...),
				ServerName: block.ServerName,
				Raw:        rawBlock,
			}
			set.Items = append(set.Items, item)
		case isClaudeHostedToolBlock(blockType):
			rawBlock, err := kitutil.Marshal(block)
			if err != nil {
				return nil, set, fmt.Errorf("content[%d]: %w", position, err)
			}
			results, err := kitutil.Marshal(block.Content)
			if err != nil {
				return nil, set, fmt.Errorf("content[%d].content: %w", position, err)
			}
			failed, errorCode := claudeHostedResultFailure(blockType, results, block.IsError, block.ErrorCode)
			status := "completed"
			isError := block.IsError
			if failed {
				status = "failed"
				if isError == nil {
					value := true
					isError = &value
				}
			}
			set.Items = append(set.Items, HostedResponseItem{
				Kind:       hostedKindFromClaudeResult(blockType),
				NativeType: blockType,
				ID:         block.ToolUseId,
				CallID:     block.ToolUseId,
				Status:     status,
				Position:   position,
				Results:    results,
				ErrorCode:  errorCode,
				IsError:    isError,
				Raw:        rawBlock,
			})
		default:
			clone.Content = append(clone.Content, block)
			set.RegularPositions = append(set.RegularPositions, position)
		}
	}
	return &clone, set, nil
}

func extractOpenAIHostedResponse(response any) (any, HostedResponseSet, error) {
	var source *dto.OpenAIResponsesResponse
	switch value := response.(type) {
	case *dto.OpenAIResponsesResponse:
		source = value
	case dto.OpenAIResponsesResponse:
		source = &value
	default:
		return nil, HostedResponseSet{}, fmt.Errorf("expected OpenAI Responses response, got %T", response)
	}
	clone := *source
	clone.Output = make([]dto.ResponsesOutput, 0, len(source.Output))
	set := HostedResponseSet{Source: types.RelayFormatOpenAIResponses, SourceLength: len(source.Output)}
	for position := range source.Output {
		output := source.Output[position]
		if !isResponsesHostedOutput(output.Type) {
			clone.Output = append(clone.Output, output)
			set.RegularPositions = append(set.RegularPositions, position)
			continue
		}
		rawOutput, err := kitutil.Marshal(output)
		if err != nil {
			return nil, set, fmt.Errorf("output[%d]: %w", position, err)
		}
		set.Items = append(set.Items, HostedResponseItem{
			Kind:              hostedKindFromResponsesType(output.Type),
			NativeType:        output.Type,
			ID:                output.ID,
			CallID:            output.CallId,
			Name:              output.Name,
			Status:            output.Status,
			Position:          position,
			Action:            append(json.RawMessage(nil), output.Action...),
			Results:           append(json.RawMessage(nil), output.Results...),
			Sources:           append(json.RawMessage(nil), output.Sources...),
			Caller:            append(json.RawMessage(nil), output.Caller...),
			Arguments:         append(json.RawMessage(nil), output.Arguments...),
			Output:            append(json.RawMessage(nil), output.Output...),
			Error:             append(json.RawMessage(nil), output.ItemError...),
			ServerName:        output.ServerLabel,
			ApprovalRequestID: output.ApprovalRequestID,
			Tools:             append(json.RawMessage(nil), output.MCPTools...),
			Raw:               rawOutput,
		})
	}
	return &clone, set, nil
}

func extractGeminiHostedResponse(response any) (any, HostedResponseSet, error) {
	var source *dto.GeminiChatResponse
	switch value := response.(type) {
	case *dto.GeminiChatResponse:
		source = value
	case dto.GeminiChatResponse:
		source = &value
	default:
		return nil, HostedResponseSet{}, fmt.Errorf("expected Gemini response, got %T", response)
	}
	queries := geminichat.GroundingWebSearchQueries(source)
	if len(queries) == 0 {
		return source, HostedResponseSet{Source: types.RelayFormatGemini}, nil
	}
	action, err := kitutil.Marshal(map[string]any{
		"type":    "search",
		"queries": queries,
	})
	if err != nil {
		return nil, HostedResponseSet{}, fmt.Errorf("marshal Gemini web-search action: %w", err)
	}
	// The Chat pivot emits the answer as the regular Responses output. Place
	// the hosted call after that output, matching the stream bridge which only
	// learns Gemini's queries once grounding metadata arrives near stream end.
	set := HostedResponseSet{
		Source:           types.RelayFormatGemini,
		SourceLength:     2,
		RegularPositions: []int{0},
		Items: []HostedResponseItem{{
			Kind:       KindWebSearch,
			NativeType: "googleSearch",
			ID:         fmt.Sprintf("ws_%s", kitutil.GetUUID()),
			Status:     "completed",
			Position:   1,
			Action:     action,
		}},
	}
	return source, set, nil
}

func attachOpenAIHostedResponse(response any, set HostedResponseSet) (any, []types.ConversionDiagnostic, error) {
	target, ok := response.(*dto.OpenAIResponsesResponse)
	if !ok || target == nil {
		return nil, nil, fmt.Errorf("expected OpenAI Responses response, got %T", response)
	}
	var diagnostics []types.ConversionDiagnostic
	hostedOutput := make([]positionedResponsesOutput, 0, len(set.Items))
	convertedByID := make(map[string]int, len(set.Items)*2)
	for index, item := range set.Items {
		if set.Source == types.RelayFormatOpenAIResponses && len(item.Raw) > 0 {
			var output dto.ResponsesOutput
			if err := kitutil.Unmarshal(item.Raw, &output); err != nil {
				return nil, diagnostics, fmt.Errorf("hosted_tools[%d]: %w", index, err)
			}
			hostedOutput = append(hostedOutput, positionedResponsesOutput{position: item.Position, output: output})
			continue
		}
		outputType := responsesTypeFromHostedKind(item.Kind)
		if outputType == "" {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("hosted_tools[%d]", index),
				"hosted_tool_unrepresentable",
				fmt.Sprintf("OpenAI Responses has no lossless response mapping for %q", item.NativeType),
			))
			continue
		}
		if isClaudeHostedResult(item.NativeType) {
			outputIndex, exists := convertedByID[item.CallID]
			if !exists {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("hosted_tools[%d].tool_use_id", index),
					"hosted_tool_result_orphaned",
					fmt.Sprintf("hosted-tool result references unknown call %q", item.CallID),
				))
				continue
			}
			output := &hostedOutput[outputIndex].output
			output.Status = hostedCompletionStatus(item)
			if item.Kind == KindMCP {
				failed := hostedItemFailed(item)
				var (
					encoded    json.RawMessage
					normalized bool
					err        error
				)
				if failed {
					encoded, normalized, err = responsesMCPErrorFromClaudeContent(item.Results, item.ErrorCode)
				} else {
					encoded, normalized, err = responsesMCPStringFromClaudeContent(item.Results)
				}
				if err != nil {
					diagnostics = append(diagnostics, responseSemanticLoss(
						fmt.Sprintf("hosted_tools[%d].content", index),
						"mcp_result_unrepresentable",
						err.Error(),
					))
					continue
				}
				if failed {
					output.Output = nil
					output.ItemError = encoded
				} else {
					output.Output = encoded
					output.ItemError = nil
				}
				if normalized {
					diagnostics = append(diagnostics, responsePresentationLoss(
						fmt.Sprintf("hosted_tools[%d].content", index),
						"mcp_text_result_normalized",
						"Claude's single MCP text block was normalized to a Responses output string",
					))
				}
			} else if item.Kind == KindWebSearch && rawJSONPresent(item.Results) {
				diagnostics = append(diagnostics, responsePresentationLoss(
					fmt.Sprintf("hosted_tools[%d].content", index),
					"web_search_result_omitted",
					"Claude web-search result content is provider-private and has no field on an OpenAI Responses web_search_call; completion status and citations remain available",
				))
			}
			continue
		}
		output := dto.ResponsesOutput{
			Type:   outputType,
			ID:     firstNonEmpty(item.ID, item.CallID),
			Status: hostedCompletionStatus(item),
		}
		switch item.Kind {
		case KindWebSearch:
			action, err := dto.NormalizeResponsesWebSearchAction(item.Action)
			if err != nil {
				return nil, diagnostics, fmt.Errorf("hosted_tools[%d].action: %w", index, err)
			}
			output.Action = action
		case KindMCP:
			if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.ServerName) == "" {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("hosted_tools[%d]", index),
					"mcp_identity_missing",
					"Claude MCP output requires both name and server_name for Responses mapping",
				))
				continue
			}
			output.CallId = item.CallID
			output.Name = item.Name
			output.Caller = append(json.RawMessage(nil), item.Caller...)
			output.ServerLabel = item.ServerName
			output.ApprovalRequestID = item.ApprovalRequestID
			output.MCPTools = append(json.RawMessage(nil), item.Tools...)
			arguments := item.Arguments
			if len(arguments) == 0 {
				arguments = item.Action
			}
			encodedArguments, argumentErr := responsesMCPArgumentsFromClaude(arguments)
			if argumentErr != nil {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("hosted_tools[%d].input", index),
					"mcp_arguments_unrepresentable",
					argumentErr.Error(),
				))
				continue
			}
			output.Arguments = encodedArguments
		}
		outputIndex := len(hostedOutput)
		for _, key := range []string{item.ID, item.CallID} {
			if key != "" {
				convertedByID[key] = outputIndex
			}
		}
		hostedOutput = append(hostedOutput, positionedResponsesOutput{position: item.Position, output: output})
		if set.Source != types.RelayFormatOpenAIResponses {
			diagnostics = append(diagnostics, responsePresentationLoss(
				fmt.Sprintf("hosted_tools[%d]", index),
				"hosted_tool_result_approximated",
				"hosted-tool execution is preserved, but provider-specific result fields may differ",
			))
		}
	}
	merged, orderingDiagnostics := mergeResponsesOutput(target.Output, hostedOutput, set)
	diagnostics = append(diagnostics, orderingDiagnostics...)
	target.Output = merged
	return target, diagnostics, nil
}

func attachClaudeHostedResponse(response any, set HostedResponseSet) (any, []types.ConversionDiagnostic, error) {
	target, ok := response.(*dto.ClaudeResponse)
	if !ok || target == nil {
		return nil, nil, fmt.Errorf("expected Claude response, got %T", response)
	}
	var diagnostics []types.ConversionDiagnostic
	hostedContent := make([]positionedClaudeBlocks, 0, len(set.Items))
	for index, item := range set.Items {
		if set.Source == types.RelayFormatClaude && len(item.Raw) > 0 {
			var block dto.ClaudeMediaMessage
			if err := kitutil.Unmarshal(item.Raw, &block); err != nil {
				return nil, diagnostics, fmt.Errorf("hosted_tools[%d]: %w", index, err)
			}
			hostedContent = append(hostedContent, positionedClaudeBlocks{position: item.Position, blocks: []dto.ClaudeMediaMessage{block}})
			continue
		}
		if set.Source == types.RelayFormatOpenAIResponses && item.Kind == KindWebSearch {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("hosted_tools[%d]", index),
				"web_search_response_unrepresentable",
				"Responses web-search execution cannot reconstruct Claude's required encrypted web_search_tool_result continuation state",
			))
			continue
		}
		name := claudeNameFromHostedKind(item.Kind)
		if item.Kind == KindMCP {
			name = item.Name
		}
		if name == "" {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("hosted_tools[%d]", index),
				"hosted_tool_unrepresentable",
				fmt.Sprintf("Claude has no lossless response mapping for %q", item.NativeType),
			))
			continue
		}
		var input any = map[string]any{}
		if item.Kind == KindWebSearch {
			webInput, inputErr := claudeWebSearchInputFromResponses(item.Action)
			if inputErr != nil {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("hosted_tools[%d].action", index),
					"web_search_action_unrepresentable",
					inputErr.Error(),
				))
				continue
			}
			input = webInput
		} else if item.Kind == KindMCP {
			mcpInput, inputErr := claudeMCPInputFromResponses(item.Arguments)
			if inputErr != nil {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("hosted_tools[%d].arguments", index),
					"mcp_arguments_unrepresentable",
					inputErr.Error(),
				))
				continue
			}
			input = mcpInput
		} else if len(item.Action) > 0 {
			if err := kitutil.Unmarshal(item.Action, &input); err != nil {
				return nil, diagnostics, fmt.Errorf("hosted_tools[%d].action: %w", index, err)
			}
		}
		callType := "server_tool_use"
		if item.Kind == KindMCP {
			callType = "mcp_tool_use"
		}
		blocks := []dto.ClaudeMediaMessage{{
			Type:       callType,
			Id:         item.ID,
			Name:       name,
			Input:      input,
			Caller:     append(json.RawMessage(nil), item.Caller...),
			ServerName: item.ServerName,
		}}
		result := item.Results
		if item.Kind == KindMCP {
			result = item.Output
			if hostedItemFailed(item) && rawJSONPresent(item.Error) {
				result = item.Error
			}
		} else if len(result) == 0 {
			result = item.Sources
		}
		if len(result) > 0 && !(set.Source == types.RelayFormatOpenAIResponses && item.Kind == KindWebSearch) {
			var content any
			if item.Kind == KindMCP {
				decoded, resultErr := claudeMCPContentFromResponsesString(result)
				if resultErr != nil {
					diagnostics = append(diagnostics, responseSemanticLoss(
						fmt.Sprintf("hosted_tools[%d].output", index),
						"mcp_result_unrepresentable",
						resultErr.Error(),
					))
					continue
				}
				content = decoded
			} else if err := kitutil.Unmarshal(result, &content); err != nil {
				return nil, diagnostics, fmt.Errorf("hosted_tools[%d].results: %w", index, err)
			}
			isError := hostedItemFailed(item)
			blocks = append(blocks, dto.ClaudeMediaMessage{
				Type:      claudeResultTypeFromHostedKind(item.Kind),
				ToolUseId: item.ID,
				Content:   content,
				IsError:   &isError,
				ErrorCode: item.ErrorCode,
			})
		} else if len(result) > 0 && item.Kind == KindWebSearch {
			diagnostics = append(diagnostics, responsePresentationLoss(
				fmt.Sprintf("hosted_tools[%d].results", index),
				"web_search_result_omitted",
				"Responses web-search source metadata cannot reconstruct Claude's encrypted web_search_tool_result",
			))
		} else if item.Kind == KindMCP && (item.Status == "completed" || item.Status == "failed") {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("hosted_tools[%d]", index),
				"mcp_result_missing",
				fmt.Sprintf("Responses MCP output has status %q but no output or error", item.Status),
			))
		}
		hostedContent = append(hostedContent, positionedClaudeBlocks{position: item.Position, blocks: blocks})
		if set.Source != types.RelayFormatClaude {
			diagnostics = append(diagnostics, responsePresentationLoss(
				fmt.Sprintf("hosted_tools[%d]", index),
				"hosted_tool_result_approximated",
				"hosted-tool execution is preserved, but provider-specific result fields may differ",
			))
		}
	}
	merged, orderingDiagnostics := mergeClaudeContent(target.Content, hostedContent, set)
	diagnostics = append(diagnostics, orderingDiagnostics...)
	target.Content = merged
	return target, diagnostics, nil
}

func mergeResponsesOutput(regular []dto.ResponsesOutput, hosted []positionedResponsesOutput, set HostedResponseSet) ([]dto.ResponsesOutput, []types.ConversionDiagnostic) {
	if len(hosted) == 0 {
		return regular, nil
	}
	if len(regular) == len(set.RegularPositions) {
		byPosition := make(map[int][]dto.ResponsesOutput, len(hosted))
		for _, item := range hosted {
			byPosition[item.position] = append(byPosition[item.position], item.output)
		}
		regularByPosition := make(map[int]dto.ResponsesOutput, len(regular))
		for index, position := range set.RegularPositions {
			regularByPosition[position] = regular[index]
		}
		merged := make([]dto.ResponsesOutput, 0, len(regular)+len(hosted))
		for position := 0; position < set.SourceLength; position++ {
			merged = append(merged, byPosition[position]...)
			if output, exists := regularByPosition[position]; exists {
				merged = append(merged, output)
			}
		}
		return merged, nil
	}
	before, after, exact := hostedOutsideRegularRange(hostedPositions(hosted), set.RegularPositions)
	if exact {
		merged := make([]dto.ResponsesOutput, 0, len(regular)+len(hosted))
		for _, item := range before {
			merged = append(merged, hosted[item].output)
		}
		merged = append(merged, regular...)
		for _, item := range after {
			merged = append(merged, hosted[item].output)
		}
		return merged, nil
	}
	merged := make([]dto.ResponsesOutput, 0, len(regular)+len(hosted))
	for _, item := range hosted {
		merged = append(merged, item.output)
	}
	merged = append(merged, regular...)
	return merged, []types.ConversionDiagnostic{responseSemanticLoss(
		"output",
		"hosted_tool_order_unrepresentable",
		"hosted-tool items were interleaved with content that the target converter coalesced, so their original order cannot be reconstructed",
	)}
}

func mergeClaudeContent(regular []dto.ClaudeMediaMessage, hosted []positionedClaudeBlocks, set HostedResponseSet) ([]dto.ClaudeMediaMessage, []types.ConversionDiagnostic) {
	if len(hosted) == 0 {
		return regular, nil
	}
	if len(regular) == len(set.RegularPositions) {
		byPosition := make(map[int][]dto.ClaudeMediaMessage, len(hosted))
		for _, item := range hosted {
			byPosition[item.position] = append(byPosition[item.position], item.blocks...)
		}
		regularByPosition := make(map[int]dto.ClaudeMediaMessage, len(regular))
		for index, position := range set.RegularPositions {
			regularByPosition[position] = regular[index]
		}
		merged := make([]dto.ClaudeMediaMessage, 0, len(regular)+len(hosted)*2)
		for position := 0; position < set.SourceLength; position++ {
			merged = append(merged, byPosition[position]...)
			if block, exists := regularByPosition[position]; exists {
				merged = append(merged, block)
			}
		}
		return merged, nil
	}
	before, after, exact := hostedOutsideRegularRange(claudeHostedPositions(hosted), set.RegularPositions)
	if exact {
		merged := make([]dto.ClaudeMediaMessage, 0, len(regular)+len(hosted)*2)
		for _, item := range before {
			merged = append(merged, hosted[item].blocks...)
		}
		merged = append(merged, regular...)
		for _, item := range after {
			merged = append(merged, hosted[item].blocks...)
		}
		return merged, nil
	}
	merged := make([]dto.ClaudeMediaMessage, 0, len(regular)+len(hosted)*2)
	for _, item := range hosted {
		merged = append(merged, item.blocks...)
	}
	merged = append(merged, regular...)
	return merged, []types.ConversionDiagnostic{responseSemanticLoss(
		"content",
		"hosted_tool_order_unrepresentable",
		"hosted-tool blocks were interleaved with content that the target converter coalesced, so their original order cannot be reconstructed",
	)}
}

func hostedPositions(items []positionedResponsesOutput) []int {
	positions := make([]int, len(items))
	for index := range items {
		positions[index] = items[index].position
	}
	return positions
}

func claudeHostedPositions(items []positionedClaudeBlocks) []int {
	positions := make([]int, len(items))
	for index := range items {
		positions[index] = items[index].position
	}
	return positions
}

func hostedOutsideRegularRange(hosted []int, regular []int) (before []int, after []int, exact bool) {
	if len(regular) == 0 {
		indices := make([]int, len(hosted))
		for index := range hosted {
			indices[index] = index
		}
		return indices, nil, true
	}
	firstRegular, lastRegular := regular[0], regular[len(regular)-1]
	for index, position := range hosted {
		switch {
		case position < firstRegular:
			before = append(before, index)
		case position > lastRegular:
			after = append(after, index)
		default:
			return nil, nil, false
		}
	}
	return before, after, true
}

func hostedKindFromClaudeCall(blockType string, name string) Kind {
	if blockType == "mcp_tool_use" {
		return KindMCP
	}
	switch strings.TrimSpace(name) {
	case "web_search":
		return KindWebSearch
	case "web_fetch":
		return KindWebFetch
	case "code_execution":
		return KindCodeExecution
	default:
		return KindNative
	}
}

func hostedKindFromClaudeResult(blockType string) Kind {
	switch strings.TrimSuffix(blockType, "_tool_result") {
	case "web_search":
		return KindWebSearch
	case "web_fetch":
		return KindWebFetch
	case "code_execution":
		return KindCodeExecution
	case "mcp":
		return KindMCP
	default:
		return KindNative
	}
}

func hostedKindFromResponsesType(outputType string) Kind {
	normalized := strings.TrimSpace(outputType)
	normalized = strings.TrimSuffix(normalized, "_output")
	normalized = strings.TrimSuffix(normalized, "_call")
	switch normalized {
	case "web_search":
		return KindWebSearch
	case "file_search":
		return KindFileSearch
	case "code_interpreter", "local_shell":
		return KindCodeExecution
	case "computer":
		return KindComputerUse
	case "image_generation":
		return KindImage
	case "mcp":
		return KindMCP
	default:
		return KindNative
	}
}

func responsesTypeFromHostedKind(kind Kind) string {
	switch kind {
	case KindWebSearch:
		return "web_search_call"
	case KindMCP:
		return "mcp_call"
	default:
		return ""
	}
}

func claudeNameFromHostedKind(kind Kind) string {
	switch kind {
	case KindWebSearch:
		return "web_search"
	case KindWebFetch:
		return "web_fetch"
	case KindCodeExecution:
		return ""
	case KindMCP:
		return "mcp"
	default:
		return ""
	}
}

func isClaudeHostedResult(nativeType string) bool {
	return nativeType == "mcp_tool_result" || strings.HasSuffix(nativeType, "_tool_result")
}

func hostedCompletionStatus(item HostedResponseItem) string {
	if hostedItemFailed(item) {
		return "failed"
	}
	if item.Status != "" && item.Status != "in_progress" || len(item.Results) > 0 || len(item.Output) > 0 {
		return "completed"
	}
	return "in_progress"
}

func hostedItemFailed(item HostedResponseItem) bool {
	return item.Status == "failed" || item.ErrorCode != "" || rawJSONPresent(item.Error) || item.IsError != nil && *item.IsError
}

func hostedErrorValue(item HostedResponseItem) json.RawMessage {
	if len(item.Error) > 0 {
		return append(json.RawMessage(nil), item.Error...)
	}
	if item.ErrorCode == "" {
		return nil
	}
	encoded, _ := kitutil.Marshal(item.ErrorCode)
	return encoded
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func rawJSONPresent(value json.RawMessage) bool {
	normalized := strings.TrimSpace(string(value))
	return normalized != "" && normalized != "null"
}

func claudeResultTypeFromHostedKind(kind Kind) string {
	name := claudeNameFromHostedKind(kind)
	if name == "mcp" {
		return "mcp_tool_result"
	}
	return name + "_tool_result"
}
