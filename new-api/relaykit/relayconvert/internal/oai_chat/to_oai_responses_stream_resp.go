package oaichat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

type ChatToResponsesStreamEvent struct {
	Type    string
	Payload dto.ResponsesStreamResponse
}

type ChatToResponsesStreamState struct {
	ID      string
	Model   string
	Created int64
	Usage   *dto.Usage

	// EmitSequenceNumber enables the required sequence_number field for current
	// Responses API SSE consumers while preserving the legacy relaykit default.
	EmitSequenceNumber bool

	status             string
	incompleteDetails  *dto.IncompleteDetails
	sentCreated        bool
	textOutputIndex    int
	textStarted        bool
	textDone           bool
	reasoningIndex     int
	reasoningStarted   bool
	reasoningDone      bool
	finalized          bool
	nextSequenceNumber int
	nextOutputIndex    int
	toolsByIndex       map[int]*chatToResponsesStreamTool
	hostedByID         map[string]*chatToResponsesHostedTool
	outputOrder        []chatToResponsesOutputRef
	text               strings.Builder
	annotations        []any
	reasoning          strings.Builder
	// A mid-stream finish_reason closes the message and reasoning items. Deltas that
	// arrive afterwards open a fresh item (next segment ordinal) instead of touching the
	// closed one; the closed items are snapshotted so the final response keeps them.
	textSegment      int
	reasoningSegment int
	closedMessages   []dto.ResponsesOutput
	closedReasonings []dto.ResponsesOutput
}

type chatToResponsesStreamTool struct {
	ChatIndex   int
	OutputIndex int
	ItemID      string
	CallID      string
	Name        string
	Arguments   strings.Builder
	Done        bool
}

type chatToResponsesOutputRef struct {
	Kind      string
	Segment   int
	ToolIndex int
	HostedID  string
}

// HostedToolStreamStart describes a provider-hosted tool call that is already
// being executed upstream. It is intentionally separate from function calls:
// hosted calls have their own Responses lifecycle and result fields.
type HostedToolStreamStart struct {
	Type        string
	ID          string
	Name        string
	Action      []byte
	Caller      []byte
	ServerLabel string
}

// HostedToolStreamResult completes a previously started hosted tool call.
type HostedToolStreamResult struct {
	Type      string
	ID        string
	Result    []byte
	ErrorCode string
	IsError   bool
}

type chatToResponsesHostedTool struct {
	OutputIndex int
	Output      dto.ResponsesOutput
	Done        bool
}

func NewChatToResponsesStreamState(id string, model string) *ChatToResponsesStreamState {
	return &ChatToResponsesStreamState{
		ID:              id,
		Model:           model,
		Created:         time.Now().Unix(),
		Usage:           &dto.Usage{},
		status:          "completed",
		textOutputIndex: -1,
		reasoningIndex:  -1,
		toolsByIndex:    make(map[int]*chatToResponsesStreamTool),
		hostedByID:      make(map[string]*chatToResponsesHostedTool),
	}
}

func (s *ChatToResponsesStreamState) StreamUsage() *dto.Usage {
	if s == nil {
		return nil
	}
	return s.Usage
}

func (s *ChatToResponsesStreamState) SetStreamUsage(usage *dto.Usage) {
	if s != nil && usage != nil {
		s.Usage = UsageFromChatUsage(usage)
	}
}

func (s *ChatToResponsesStreamState) StartHostedTool(start HostedToolStreamStart) ([]ChatToResponsesStreamEvent, error) {
	if s == nil {
		return nil, fmt.Errorf("Chat-to-Responses stream state is required")
	}
	start.ID = strings.TrimSpace(start.ID)
	if start.ID == "" {
		return nil, fmt.Errorf("hosted-tool stream call is missing an id")
	}
	if _, exists := s.hostedByID[start.ID]; exists {
		return nil, fmt.Errorf("duplicate hosted-tool stream call id %q", start.ID)
	}
	if hostedEventPrefix(start.Type) == "" {
		return nil, fmt.Errorf("unsupported Responses hosted-tool output type %q", start.Type)
	}
	caller := strings.TrimSpace(string(start.Caller))
	if caller != "" && caller != "null" {
		return nil, fmt.Errorf("Responses %s cannot preserve Claude hosted-tool caller provenance", start.Type)
	}

	tool := &chatToResponsesHostedTool{
		Output: dto.ResponsesOutput{
			Type:   start.Type,
			ID:     start.ID,
			Status: "in_progress",
		},
	}
	switch start.Type {
	case "web_search_call":
		action, err := dto.NormalizeResponsesWebSearchAction(start.Action)
		if err != nil {
			return nil, err
		}
		tool.Output.Action = action
	case "code_interpreter_call":
		return nil, fmt.Errorf("cannot map provider code execution to Responses code_interpreter_call without a container_id")
	case "mcp_call":
		if strings.TrimSpace(start.Name) == "" || strings.TrimSpace(start.ServerLabel) == "" {
			return nil, fmt.Errorf("Responses MCP call requires name and server_label")
		}
		arguments, err := hostedJSONString(start.Action)
		if err != nil {
			return nil, fmt.Errorf("encode Responses MCP arguments: %w", err)
		}
		tool.Output.Name = start.Name
		tool.Output.ServerLabel = start.ServerLabel
		tool.Output.Arguments = arguments
	}
	outputIndex := s.nextHostedIndex(start.ID)
	tool.OutputIndex = outputIndex
	s.hostedByID[start.ID] = tool

	events := s.ensureCreated()
	addedItem := cloneHostedOutput(&tool.Output)
	if start.Type == "mcp_call" {
		addedItem.Arguments = json.RawMessage(`""`)
	}
	events = append(events,
		s.event(responsesEventOutputItemAdded, dto.ResponsesStreamResponse{
			OutputIndex: intPtr(outputIndex),
			ItemID:      start.ID,
			Item:        addedItem,
		}),
		s.event(hostedEventPrefix(start.Type)+".in_progress", dto.ResponsesStreamResponse{
			OutputIndex: intPtr(outputIndex),
			ItemID:      start.ID,
		}),
	)
	if start.Type == "web_search_call" {
		events = append(events, s.event(hostedEventPrefix(start.Type)+".searching", dto.ResponsesStreamResponse{
			OutputIndex: intPtr(outputIndex),
			ItemID:      start.ID,
		}))
	}
	if start.Type == "mcp_call" {
		arguments := dto.ResponsesArgumentsString(tool.Output.Arguments)
		events = append(events,
			s.event("response.mcp_call_arguments.delta", dto.ResponsesStreamResponse{
				OutputIndex: intPtr(outputIndex),
				ItemID:      start.ID,
				Delta:       arguments,
			}),
			s.event("response.mcp_call_arguments.done", dto.ResponsesStreamResponse{
				OutputIndex: intPtr(outputIndex),
				ItemID:      start.ID,
				Arguments:   kitutil.GetPointer(arguments),
			}),
		)
	}
	return events, nil
}

func (s *ChatToResponsesStreamState) CompleteHostedTool(result HostedToolStreamResult) ([]ChatToResponsesStreamEvent, error) {
	if s == nil {
		return nil, fmt.Errorf("Chat-to-Responses stream state is required")
	}
	result.ID = strings.TrimSpace(result.ID)
	tool := s.hostedByID[result.ID]
	if tool == nil {
		return nil, fmt.Errorf("hosted-tool result references unknown call %q", result.ID)
	}
	if tool.Done {
		return nil, fmt.Errorf("duplicate hosted-tool result for call %q", result.ID)
	}
	if result.Type != "" && result.Type != tool.Output.Type {
		return nil, fmt.Errorf("hosted-tool result type %q does not match call type %q", result.Type, tool.Output.Type)
	}

	failed := result.IsError || strings.TrimSpace(result.ErrorCode) != ""
	tool.Output.Status = "completed"
	switch tool.Output.Type {
	case "web_search_call":
		// Responses exposes only the action and lifecycle status on a
		// web_search_call. Claude's opaque result payload cannot be emitted
		// as a top-level `results` field.
	case "code_interpreter_call":
		return nil, fmt.Errorf("Responses code_interpreter_call is not supported without a container_id")
	case "mcp_call":
		output, err := hostedResultString(result.Result)
		if err != nil {
			return nil, fmt.Errorf("encode Responses MCP output: %w", err)
		}
		tool.Output.Output = output
	}
	if failed {
		tool.Output.Status = "failed"
		errorValue := result.ErrorCode
		if errorValue == "" {
			errorValue = "hosted tool execution failed"
		}
		if tool.Output.Type == "mcp_call" {
			encoded, err := kitutil.Marshal(errorValue)
			if err != nil {
				return nil, fmt.Errorf("marshal hosted-tool error: %w", err)
			}
			tool.Output.ItemError = encoded
			tool.Output.Output = nil
		}
	}
	tool.Done = true

	events := make([]ChatToResponsesStreamEvent, 0, 2)
	if eventType := hostedTerminalEvent(tool.Output.Type, failed); eventType != "" {
		events = append(events, s.event(eventType, dto.ResponsesStreamResponse{
			OutputIndex: intPtr(tool.OutputIndex),
			ItemID:      result.ID,
		}))
	}
	events = append(events, s.event(responsesEventOutputItemDone, dto.ResponsesStreamResponse{
		OutputIndex: intPtr(tool.OutputIndex),
		ItemID:      result.ID,
		Item:        cloneHostedOutput(&tool.Output),
	}))
	return events, nil
}

// Fail emits a terminal Responses error using the same event allocator as the
// rest of the stream, so callers never have to append a JSON HTTP error to an
// already-started SSE response.
func (s *ChatToResponsesStreamState) Fail(code string, message string, param string) []ChatToResponsesStreamEvent {
	if s == nil || s.finalized {
		return nil
	}
	code = strings.TrimSpace(code)
	if code == "" {
		code = "server_error"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = "upstream response stream failed"
	}
	s.status = "failed"
	events := s.ensureCreated()
	events = append(events, s.doneDeltaEvents()...)
	s.finalized = true
	events = append(events, s.event("error", dto.ResponsesStreamResponse{
		Code:    code,
		Message: message,
		Param:   param,
	}))
	response := s.finalResponse()
	response.Error = map[string]any{
		"code":    code,
		"message": message,
	}
	events = append(events, s.event("response.failed", dto.ResponsesStreamResponse{
		Response: response,
	}))
	return events
}

func ChatCompletionsStreamChunkToResponsesEvents(chunk *dto.ChatCompletionsStreamResponse, state *ChatToResponsesStreamState) ([]ChatToResponsesStreamEvent, error) {
	if chunk == nil || state == nil {
		return nil, nil
	}
	if state.ID == "" {
		state.ID = chunk.Id
	}
	if state.Model == "" {
		state.Model = chunk.Model
	}
	if state.Created == 0 {
		state.Created = chunk.Created
	}
	if chunk.Usage != nil {
		state.Usage = UsageFromChatUsage(chunk.Usage)
	}

	events := state.ensureCreated()
	for _, choice := range chunk.Choices {
		if choice.Delta.GetReasoningContent() != "" {
			events = append(events, state.appendReasoningDelta(choice.Delta.GetReasoningContent())...)
		}
		if choice.Delta.GetContentString() != "" {
			events = append(events, state.appendTextDelta(choice.Delta.GetContentString())...)
		}
		if len(choice.Delta.Annotations) > 0 {
			annotationEvents, err := state.appendAnnotationDelta(choice.Delta.Annotations)
			if err != nil {
				return nil, err
			}
			events = append(events, annotationEvents...)
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			toolEvents, err := state.appendToolCallDelta(toolCall)
			if err != nil {
				return nil, err
			}
			events = append(events, toolEvents...)
		}
		if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
			state.applyFinishReason(*choice.FinishReason)
			events = append(events, state.doneDeltaEvents()...)
		}
	}
	return events, nil
}

func (s *ChatToResponsesStreamState) ensureCreated() []ChatToResponsesStreamEvent {
	if s.sentCreated {
		return nil
	}
	s.sentCreated = true
	return []ChatToResponsesStreamEvent{s.event(responsesEventCreated, dto.ResponsesStreamResponse{
		Type:     responsesEventCreated,
		Response: s.createdResponse(),
	})}
}

func FinalizeChatCompletionsStreamToResponses(state *ChatToResponsesStreamState) []ChatToResponsesStreamEvent {
	if state == nil || state.finalized {
		return nil
	}
	events := state.doneDeltaEvents()
	state.finalized = true
	resp := state.finalResponse()
	eventType := responsesEventCompleted
	if state.status == "incomplete" {
		eventType = responsesEventIncomplete
	}
	events = append(events, state.event(eventType, dto.ResponsesStreamResponse{
		Type:     eventType,
		Response: resp,
	}))
	return events
}

func (s *ChatToResponsesStreamState) UsageText() string {
	if s == nil {
		return ""
	}
	return s.text.String()
}

func (s *ChatToResponsesStreamState) appendTextDelta(delta string) []ChatToResponsesStreamEvent {
	events := s.startText()
	s.text.WriteString(delta)
	events = append(events, s.event(responsesEventOutputTextDelta, dto.ResponsesStreamResponse{
		Type:         responsesEventOutputTextDelta,
		OutputIndex:  intPtr(s.textOutputIndex),
		ContentIndex: intPtr(0),
		Delta:        delta,
		ItemID:       s.messageID(),
	}))
	return events
}

func (s *ChatToResponsesStreamState) startText() []ChatToResponsesStreamEvent {
	if s.textDone {
		s.textSegment++
		s.textStarted = false
		s.textDone = false
		s.text.Reset()
		s.annotations = nil
	}
	if !s.textStarted {
		s.textStarted = true
		s.textOutputIndex = s.nextIndex(chatToResponsesOutputRef{Kind: "message", Segment: s.textSegment})
		return []ChatToResponsesStreamEvent{s.event(responsesEventOutputItemAdded, dto.ResponsesStreamResponse{
			Type:        responsesEventOutputItemAdded,
			OutputIndex: intPtr(s.textOutputIndex),
			Item: &dto.ResponsesOutput{
				Type:    responsesOutputTypeMessage,
				ID:      s.messageID(),
				Status:  "in_progress",
				Role:    "assistant",
				Content: []dto.ResponsesOutputContent{},
			},
		})}
	}
	return nil
}

func (s *ChatToResponsesStreamState) appendAnnotationDelta(raw []byte) ([]ChatToResponsesStreamEvent, error) {
	annotations, err := chatAnnotationsToResponses(raw)
	if err != nil {
		return nil, err
	}
	events := s.startText()
	for _, annotation := range annotations {
		annotationJSON, err := kitutil.Marshal(annotation)
		if err != nil {
			return nil, fmt.Errorf("marshal Responses annotation: %w", err)
		}
		annotationIndex := len(s.annotations)
		s.annotations = append(s.annotations, annotation)
		events = append(events, s.event(responsesEventOutputTextAnnotationAdded, dto.ResponsesStreamResponse{
			Type:            responsesEventOutputTextAnnotationAdded,
			OutputIndex:     intPtr(s.textOutputIndex),
			ContentIndex:    intPtr(0),
			AnnotationIndex: intPtr(annotationIndex),
			Annotation:      annotationJSON,
			ItemID:          s.messageID(),
		}))
	}
	return events, nil
}

func (s *ChatToResponsesStreamState) appendReasoningDelta(delta string) []ChatToResponsesStreamEvent {
	events := make([]ChatToResponsesStreamEvent, 0, 3)
	if s.reasoningDone {
		s.reasoningSegment++
		s.reasoningStarted = false
		s.reasoningDone = false
		s.reasoning.Reset()
	}
	if !s.reasoningStarted {
		s.reasoningStarted = true
		s.reasoningIndex = s.nextIndex(chatToResponsesOutputRef{Kind: "reasoning", Segment: s.reasoningSegment})
		events = append(events, s.event(responsesEventOutputItemAdded, dto.ResponsesStreamResponse{
			Type:        responsesEventOutputItemAdded,
			OutputIndex: intPtr(s.reasoningIndex),
			Item: &dto.ResponsesOutput{
				Type:    responsesOutputTypeReasoning,
				ID:      s.reasoningID(),
				Status:  "in_progress",
				Summary: []dto.ResponsesReasoningSummaryPart{},
			},
		}))
		events = append(events, s.event(responsesEventReasoningSummaryPartAdded, dto.ResponsesStreamResponse{
			Type:         responsesEventReasoningSummaryPartAdded,
			OutputIndex:  intPtr(s.reasoningIndex),
			SummaryIndex: intPtr(0),
			ItemID:       s.reasoningID(),
			Part:         &dto.ResponsesReasoningSummaryPart{Type: "summary_text"},
		}))
	}
	s.reasoning.WriteString(delta)
	events = append(events, s.event(responsesEventReasoningSummaryDelta, dto.ResponsesStreamResponse{
		Type:         responsesEventReasoningSummaryDelta,
		OutputIndex:  intPtr(s.reasoningIndex),
		SummaryIndex: intPtr(0),
		Delta:        delta,
		ItemID:       s.reasoningID(),
	}))
	return events
}

func (s *ChatToResponsesStreamState) appendToolCallDelta(toolCall dto.ToolCallResponse) ([]ChatToResponsesStreamEvent, error) {
	chatIndex := 0
	if toolCall.Index != nil {
		chatIndex = *toolCall.Index
	}
	incomingID := strings.TrimSpace(toolCall.ID)
	tool := s.toolsByIndex[chatIndex]
	events := make([]ChatToResponsesStreamEvent, 0, 2)
	if tool == nil {
		tool = &chatToResponsesStreamTool{
			ChatIndex:   chatIndex,
			OutputIndex: s.nextIndex(chatToResponsesOutputRef{Kind: "tool", ToolIndex: chatIndex}),
			CallID:      incomingID,
			Name:        strings.TrimSpace(toolCall.Function.Name),
		}
		tool.ItemID = incomingID
		if tool.ItemID == "" {
			tool.ItemID = fmt.Sprintf("%s_call_%d", s.ID, chatIndex)
		}
		s.toolsByIndex[chatIndex] = tool
		events = append(events, s.event(responsesEventOutputItemAdded, dto.ResponsesStreamResponse{
			Type:        responsesEventOutputItemAdded,
			OutputIndex: intPtr(tool.OutputIndex),
			ItemID:      tool.ItemID,
			Item: &dto.ResponsesOutput{
				Type:      responsesOutputTypeFunctionCall,
				ID:        tool.ItemID,
				Status:    "in_progress",
				CallId:    tool.callID(),
				Name:      tool.Name,
				Arguments: []byte(`""`),
			},
		}))
	}
	if tool.Done {
		return nil, fmt.Errorf("tool-call stream index %d received data after completion", chatIndex)
	}
	if incomingID != "" {
		if tool.CallID != "" && tool.CallID != incomingID {
			return nil, fmt.Errorf("tool-call stream index %d changed id from %q to %q", chatIndex, tool.CallID, incomingID)
		}
		tool.CallID = incomingID
	}
	incomingName := strings.TrimSpace(toolCall.Function.Name)
	if incomingName != "" {
		if tool.Name != "" && tool.Name != incomingName {
			return nil, fmt.Errorf("tool-call stream index %d changed name from %q to %q", chatIndex, tool.Name, incomingName)
		}
		tool.Name = incomingName
	}
	if toolCall.Function.Arguments != "" {
		tool.Arguments.WriteString(toolCall.Function.Arguments)
		events = append(events, s.event(responsesEventFunctionArgsDelta, dto.ResponsesStreamResponse{
			Type:        responsesEventFunctionArgsDelta,
			OutputIndex: intPtr(tool.OutputIndex),
			ItemID:      tool.ItemID,
			Delta:       toolCall.Function.Arguments,
		}))
	}
	return events, nil
}

func (s *ChatToResponsesStreamState) doneDeltaEvents() []ChatToResponsesStreamEvent {
	events := make([]ChatToResponsesStreamEvent, 0)
	status := s.outputStatus()
	if s.textStarted && !s.textDone {
		s.textDone = true
		textDone := dto.ResponsesStreamResponse{
			Type:         "response.output_text.done",
			OutputIndex:  intPtr(s.textOutputIndex),
			ContentIndex: intPtr(0),
			ItemID:       s.messageID(),
		}
		if s.EmitSequenceNumber {
			textDone.Text = kitutil.GetPointer(s.text.String())
		}
		events = append(events, s.event("response.output_text.done", textDone))
		closedMessage := s.messageOutput(status)
		events = append(events, s.event(responsesEventOutputItemDone, dto.ResponsesStreamResponse{
			Type:        responsesEventOutputItemDone,
			OutputIndex: intPtr(s.textOutputIndex),
			Item:        closedMessage,
		}))
		s.closedMessages = append(s.closedMessages, *closedMessage)
	}
	if s.reasoningStarted && !s.reasoningDone {
		s.reasoningDone = true
		reasoningDone := dto.ResponsesStreamResponse{
			Type:         responsesEventReasoningSummaryDone,
			OutputIndex:  intPtr(s.reasoningIndex),
			SummaryIndex: intPtr(0),
			ItemID:       s.reasoningID(),
			Part: &dto.ResponsesReasoningSummaryPart{
				Type: "summary_text",
				Text: s.reasoning.String(),
			},
		}
		if s.EmitSequenceNumber {
			reasoningDone.Text = kitutil.GetPointer(s.reasoning.String())
			reasoningDone.Part = nil
		}
		events = append(events, s.event(responsesEventReasoningSummaryDone, reasoningDone))
		events = append(events, s.event(responsesEventReasoningSummaryPartDone, dto.ResponsesStreamResponse{
			Type:         responsesEventReasoningSummaryPartDone,
			OutputIndex:  intPtr(s.reasoningIndex),
			SummaryIndex: intPtr(0),
			ItemID:       s.reasoningID(),
			Part: &dto.ResponsesReasoningSummaryPart{
				Type: "summary_text",
				Text: s.reasoning.String(),
			},
		}))
		closedReasoning := s.reasoningOutput(status)
		events = append(events, s.event(responsesEventOutputItemDone, dto.ResponsesStreamResponse{
			Type:        responsesEventOutputItemDone,
			OutputIndex: intPtr(s.reasoningIndex),
			Item:        closedReasoning,
		}))
		s.closedReasonings = append(s.closedReasonings, *closedReasoning)
	}
	for _, tool := range s.sortedTools() {
		if tool.Done {
			continue
		}
		tool.Done = true
		argumentsDone := dto.ResponsesStreamResponse{
			Type:        responsesEventFunctionArgsDone,
			OutputIndex: intPtr(tool.OutputIndex),
			ItemID:      tool.ItemID,
		}
		if s.EmitSequenceNumber {
			argumentsDone.Arguments = kitutil.GetPointer(tool.Arguments.String())
			argumentsDone.Name = tool.Name
		}
		events = append(events, s.event(responsesEventFunctionArgsDone, argumentsDone))
		events = append(events, s.event(responsesEventOutputItemDone, dto.ResponsesStreamResponse{
			Type:        responsesEventOutputItemDone,
			OutputIndex: intPtr(tool.OutputIndex),
			Item:        s.toolOutput(tool, status),
		}))
	}
	for _, ref := range s.outputOrder {
		if ref.Kind != "hosted" {
			continue
		}
		tool := s.hostedByID[ref.HostedID]
		if tool == nil || tool.Done {
			continue
		}
		if s.status != "failed" {
			s.status = "incomplete"
		}
		tool.Done = true
		tool.Output.Status = "incomplete"
		if s.status == "failed" {
			tool.Output.Status = "failed"
			errorValue, err := kitutil.Marshal("provider stream failed before hosted-tool result")
			if err == nil && tool.Output.Type == "mcp_call" {
				tool.Output.ItemError = errorValue
				tool.Output.Output = nil
			}
			if eventType := hostedTerminalEvent(tool.Output.Type, true); eventType != "" {
				events = append(events, s.event(eventType, dto.ResponsesStreamResponse{
					OutputIndex: intPtr(tool.OutputIndex),
					ItemID:      tool.Output.ID,
				}))
			}
		}
		events = append(events, s.event(responsesEventOutputItemDone, dto.ResponsesStreamResponse{
			OutputIndex: intPtr(tool.OutputIndex),
			ItemID:      tool.Output.ID,
			Item:        cloneHostedOutput(&tool.Output),
		}))
	}
	return events
}

func (s *ChatToResponsesStreamState) applyFinishReason(finishReason string) {
	if status, details := ResponsesStatusFromChatFinishReason(finishReason); status != "" {
		s.status = status
		s.incompleteDetails = details
	}
}

func (s *ChatToResponsesStreamState) finalResponse() *dto.OpenAIResponsesResponse {
	output := make([]dto.ResponsesOutput, 0, len(s.outputOrder))
	status := s.outputStatus()
	for _, ref := range s.outputOrder {
		switch ref.Kind {
		case "message":
			if ref.Segment < len(s.closedMessages) {
				closed := s.closedMessages[ref.Segment]
				closed.Status = status
				output = append(output, closed)
				continue
			}
			output = append(output, *s.messageOutput(status))
		case "reasoning":
			if ref.Segment < len(s.closedReasonings) {
				closed := s.closedReasonings[ref.Segment]
				closed.Status = status
				output = append(output, closed)
				continue
			}
			output = append(output, *s.reasoningOutput(status))
		case "tool":
			if tool := s.toolsByIndex[ref.ToolIndex]; tool != nil {
				output = append(output, *s.toolOutput(tool, status))
			}
		case "hosted":
			if tool := s.hostedByID[ref.HostedID]; tool != nil {
				output = append(output, *cloneHostedOutput(&tool.Output))
			}
		}
	}
	return &dto.OpenAIResponsesResponse{
		ID:                s.ID,
		Object:            "response",
		CreatedAt:         dto.IntValue(s.Created),
		Status:            []byte(fmt.Sprintf("%q", s.status)),
		IncompleteDetails: s.incompleteDetails,
		Model:             s.Model,
		Output:            output,
		Usage:             s.Usage,
	}
}

func (s *ChatToResponsesStreamState) createdResponse() *dto.OpenAIResponsesResponse {
	return &dto.OpenAIResponsesResponse{
		ID:        s.ID,
		Object:    "response",
		CreatedAt: dto.IntValue(s.Created),
		Status:    []byte(`"in_progress"`),
		Model:     s.Model,
		Output:    []dto.ResponsesOutput{},
	}
}

func (s *ChatToResponsesStreamState) nextIndex(ref chatToResponsesOutputRef) int {
	index := s.nextOutputIndex
	s.nextOutputIndex++
	s.outputOrder = append(s.outputOrder, ref)
	return index
}

func (s *ChatToResponsesStreamState) nextHostedIndex(id string) int {
	index := s.nextOutputIndex
	s.nextOutputIndex++
	s.outputOrder = append(s.outputOrder, chatToResponsesOutputRef{Kind: "hosted", HostedID: id})
	return index
}

func (s *ChatToResponsesStreamState) sortedTools() []*chatToResponsesStreamTool {
	indexes := make([]int, 0, len(s.toolsByIndex))
	for index := range s.toolsByIndex {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	tools := make([]*chatToResponsesStreamTool, 0, len(indexes))
	for _, index := range indexes {
		tools = append(tools, s.toolsByIndex[index])
	}
	return tools
}

func (s *ChatToResponsesStreamState) outputStatus() string {
	if s.status == "incomplete" || s.status == "failed" {
		return "incomplete"
	}
	return "completed"
}

func (s *ChatToResponsesStreamState) messageID() string {
	return fmt.Sprintf("%s_msg_%d", s.ID, s.textSegment)
}

func (s *ChatToResponsesStreamState) reasoningID() string {
	return fmt.Sprintf("%s_reasoning_%d", s.ID, s.reasoningSegment)
}

func (s *ChatToResponsesStreamState) messageOutput(status string) *dto.ResponsesOutput {
	annotations := s.annotations
	if annotations == nil {
		annotations = []any{}
	}
	return &dto.ResponsesOutput{
		Type:   responsesOutputTypeMessage,
		ID:     s.messageID(),
		Status: status,
		Role:   "assistant",
		Content: []dto.ResponsesOutputContent{
			{
				Type:        "output_text",
				Text:        s.text.String(),
				Annotations: annotations,
			},
		},
	}
}

func (s *ChatToResponsesStreamState) reasoningOutput(status string) *dto.ResponsesOutput {
	return &dto.ResponsesOutput{
		Type:   responsesOutputTypeReasoning,
		ID:     s.reasoningID(),
		Status: status,
		Summary: []dto.ResponsesReasoningSummaryPart{
			{
				Type: "summary_text",
				Text: s.reasoning.String(),
			},
		},
	}
}

func (s *ChatToResponsesStreamState) toolOutput(tool *chatToResponsesStreamTool, status string) *dto.ResponsesOutput {
	return &dto.ResponsesOutput{
		Type:      responsesOutputTypeFunctionCall,
		ID:        tool.ItemID,
		Status:    status,
		CallId:    tool.callID(),
		Name:      tool.Name,
		Arguments: chatArgumentsRawMessage(tool.Arguments.String()),
	}
}

func (t *chatToResponsesStreamTool) callID() string {
	if t == nil {
		return ""
	}
	if t.CallID == "" {
		return t.ItemID
	}
	return t.CallID
}

func hostedEventPrefix(outputType string) string {
	switch outputType {
	case "web_search_call":
		return "response.web_search_call"
	case "mcp_call":
		return "response.mcp_call"
	default:
		return ""
	}
}

func hostedTerminalEvent(outputType string, failed bool) string {
	prefix := hostedEventPrefix(outputType)
	if prefix == "" {
		return ""
	}
	if !failed {
		return prefix + ".completed"
	}
	// OpenAI currently defines a dedicated failed lifecycle event for MCP.
	// Web search and code interpreter surface failure on output_item.done.
	if outputType == "mcp_call" {
		return prefix + ".failed"
	}
	return ""
}

func hostedJSONString(value []byte) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`""`), nil
	}
	if !kitutil.Valid(value) {
		return nil, fmt.Errorf("invalid JSON payload")
	}
	encoded, err := kitutil.Marshal(string(value))
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func hostedResultString(value []byte) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`""`), nil
	}
	if !kitutil.Valid(value) {
		return nil, fmt.Errorf("invalid JSON payload")
	}
	if kitutil.GetJsonType(value) == "string" {
		return append(json.RawMessage(nil), value...), nil
	}
	return hostedJSONString(value)
}

func cloneHostedOutput(output *dto.ResponsesOutput) *dto.ResponsesOutput {
	if output == nil {
		return nil
	}
	clone := *output
	clone.Action = append([]byte(nil), output.Action...)
	clone.Arguments = append([]byte(nil), output.Arguments...)
	clone.Code = append([]byte(nil), output.Code...)
	clone.Results = append([]byte(nil), output.Results...)
	clone.Outputs = append([]byte(nil), output.Outputs...)
	clone.Output = append([]byte(nil), output.Output...)
	clone.ItemError = append([]byte(nil), output.ItemError...)
	clone.Caller = append([]byte(nil), output.Caller...)
	return &clone
}

func (s *ChatToResponsesStreamState) event(eventType string, payload dto.ResponsesStreamResponse) ChatToResponsesStreamEvent {
	if s.EmitSequenceNumber {
		sequenceNumber := s.nextSequenceNumber
		s.nextSequenceNumber++
		payload.SequenceNumber = &sequenceNumber
	}
	return responsesStreamEvent(eventType, payload)
}
