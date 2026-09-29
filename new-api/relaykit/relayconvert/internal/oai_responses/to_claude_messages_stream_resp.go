package oairesponses

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	sharedclaude "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/claude"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

const responsesEventOutputTextDone = "response.output_text.done"

type ResponsesToClaudeStreamState struct {
	ID    string
	Model string
	Usage *dto.Usage

	sentMessageStart bool
	done             bool
	sawToolCall      bool
	nextBlockIndex   int
	blocks           []*responsesClaudeStreamBlock
	byOutputIndex    map[int]*responsesClaudeStreamBlock
	byItemID         map[string]*responsesClaudeStreamBlock
	lastByKind       map[string]*responsesClaudeStreamBlock
	usageText        strings.Builder
}

type responsesClaudeStreamBlock struct {
	Index               int
	Kind                string
	ItemID              string
	CallID              string
	Name                string
	Started             bool
	Stopped             bool
	Value               strings.Builder
	SentBytes           int
	AnnotationCount     int
	NeedsReasoningBreak bool
}

func NewResponsesToClaudeStreamState(id string, model string) *ResponsesToClaudeStreamState {
	return &ResponsesToClaudeStreamState{
		ID:            strings.TrimSpace(id),
		Model:         strings.TrimSpace(model),
		byOutputIndex: make(map[int]*responsesClaudeStreamBlock),
		byItemID:      make(map[string]*responsesClaudeStreamBlock),
		lastByKind:    make(map[string]*responsesClaudeStreamBlock),
	}
}

func (s *ResponsesToClaudeStreamState) UsageText() string {
	if s == nil {
		return ""
	}
	return s.usageText.String()
}

func (s *ResponsesToClaudeStreamState) Done() bool {
	return s != nil && s.done
}

func (s *ResponsesToClaudeStreamState) SetUsage(usage *dto.Usage) {
	if s != nil && usage != nil {
		s.Usage = usage
	}
}

func (s *ResponsesToClaudeStreamState) StreamUsage() *dto.Usage {
	if s == nil {
		return nil
	}
	return s.Usage
}

func (s *ResponsesToClaudeStreamState) SetStreamUsage(usage *dto.Usage) {
	s.SetUsage(usage)
}

func (s *ResponsesToClaudeStreamState) ConvertChunk(event *dto.ResponsesStreamResponse, estimatedInputTokens int) ([]*dto.ClaudeResponse, *dto.Usage, error) {
	if s == nil {
		return nil, nil, nil
	}
	if event == nil || s.done {
		return nil, s.Usage, nil
	}

	s.applyResponseMetadata(event.Response)
	switch event.Type {
	case responsesEventCreated:
		return s.ensureMessageStart(estimatedInputTokens), s.Usage, nil
	case responsesEventReasoningSummaryDelta, responsesEventReasoningTextDelta:
		block, err := s.ensureBlock(event, "thinking")
		if err != nil {
			return nil, s.Usage, err
		}
		delta := event.Delta
		if block.NeedsReasoningBreak && delta != "" {
			delta = separatedResponsesDelta(delta)
			block.NeedsReasoningBreak = false
		}
		return s.appendDelta(block, delta, estimatedInputTokens), s.Usage, nil
	case responsesEventReasoningSummaryDone, responsesEventReasoningTextDone:
		block, err := s.ensureBlock(event, "thinking")
		if err != nil {
			return nil, s.Usage, err
		}
		var responses []*dto.ClaudeResponse
		if event.Text != nil {
			responses = append(responses, s.mergeFinalValue(block, *event.Text, estimatedInputTokens)...)
		}
		if block.Value.Len() > 0 {
			block.NeedsReasoningBreak = true
		}
		return responses, s.Usage, nil
	case responsesEventOutputTextDelta:
		block, err := s.ensureBlock(event, "text")
		if err != nil {
			return nil, s.Usage, err
		}
		return s.appendDelta(block, event.Delta, estimatedInputTokens), s.Usage, nil
	case responsesEventOutputTextDone:
		block, err := s.ensureBlock(event, "text")
		if err != nil {
			return nil, s.Usage, err
		}
		if event.Text == nil {
			return nil, s.Usage, nil
		}
		return s.mergeFinalValue(block, *event.Text, estimatedInputTokens), s.Usage, nil
	case responsesEventOutputTextAnnotationAdded:
		block, err := s.ensureBlock(event, "text")
		if err != nil {
			return nil, s.Usage, err
		}
		var annotation any
		if err := kitutil.Unmarshal(event.Annotation, &annotation); err != nil {
			return nil, s.Usage, fmt.Errorf("invalid Responses stream annotation: %w", err)
		}
		return s.appendAnnotations(block, []any{annotation}, estimatedInputTokens, false), s.Usage, nil
	case responsesEventOutputItemAdded, responsesEventOutputItemDone:
		responses, err := s.applyOutputItem(event, estimatedInputTokens, event.Type == responsesEventOutputItemDone)
		return responses, s.Usage, err
	case responsesEventFunctionArgsDelta, responsesEventCustomToolInputDelta:
		block, err := s.ensureBlock(event, "tool_use")
		if err != nil {
			return nil, s.Usage, err
		}
		return s.appendDelta(block, event.Delta, estimatedInputTokens), s.Usage, nil
	case responsesEventFunctionArgsDone, responsesEventCustomToolInputDone:
		block, err := s.ensureBlock(event, "tool_use")
		if err != nil {
			return nil, s.Usage, err
		}
		if event.Arguments == nil {
			return nil, s.Usage, nil
		}
		return s.mergeFinalValue(block, *event.Arguments, estimatedInputTokens), s.Usage, nil
	case responsesEventCompleted, responsesEventDone, responsesEventIncomplete:
		responses, err := s.finish(event.Response, estimatedInputTokens)
		return responses, s.Usage, err
	case responsesEventFailed, responsesEventError:
		message := strings.TrimSpace(event.Message)
		if message == "" {
			message = event.Type
		}
		return nil, s.Usage, fmt.Errorf("responses stream error: %s", message)
	default:
		return nil, s.Usage, nil
	}
}

func (s *ResponsesToClaudeStreamState) Finalize(estimatedInputTokens int) ([]*dto.ClaudeResponse, error) {
	if s == nil || s.done {
		return nil, nil
	}
	return s.finish(nil, estimatedInputTokens)
}

func (s *ResponsesToClaudeStreamState) applyResponseMetadata(response *dto.OpenAIResponsesResponse) {
	if s == nil || response == nil {
		return
	}
	if response.ID != "" {
		s.ID = response.ID
	}
	if response.Model != "" {
		s.Model = response.Model
	}
	if response.Usage != nil {
		s.Usage = dto.MergeUsageNonZero(s.Usage, UsageFromResponsesUsage(response.Usage))
	}
}

func (s *ResponsesToClaudeStreamState) ensureMessageStart(estimatedInputTokens int) []*dto.ClaudeResponse {
	if s.sentMessageStart {
		return nil
	}
	s.sentMessageStart = true
	inputTokens := estimatedInputTokens
	if s.Usage != nil {
		if usage := sharedclaude.UsageFromOpenAI(s.Usage); usage != nil {
			inputTokens = usage.InputTokens
		}
	}
	message := &dto.ClaudeMediaMessage{
		Id:    s.ID,
		Type:  "message",
		Role:  "assistant",
		Model: s.Model,
		Usage: &dto.ClaudeUsage{InputTokens: inputTokens},
	}
	message.SetContent(make([]any, 0))
	return []*dto.ClaudeResponse{{Type: "message_start", Message: message}}
}

func (s *ResponsesToClaudeStreamState) ensureBlock(event *dto.ResponsesStreamResponse, kind string) (*responsesClaudeStreamBlock, error) {
	block := s.findBlock(event)
	if block == nil {
		if last := s.lastByKind[kind]; last != nil && !last.Stopped && event.OutputIndex == nil && responseStreamEventItemID(event) == "" {
			block = last
		}
	}
	if block == nil {
		block = &responsesClaudeStreamBlock{Index: s.nextBlockIndex, Kind: kind}
		s.nextBlockIndex++
		s.blocks = append(s.blocks, block)
	}
	if block.Kind == "" {
		block.Kind = kind
	}
	if block.Kind != kind {
		return nil, fmt.Errorf("Responses output item changed from %s to %s", block.Kind, kind)
	}
	s.applyBlockMetadata(block, event)
	s.lastByKind[kind] = block
	return block, nil
}

func (s *ResponsesToClaudeStreamState) findBlock(event *dto.ResponsesStreamResponse) *responsesClaudeStreamBlock {
	if event == nil {
		return nil
	}
	if event.OutputIndex != nil {
		if block := s.byOutputIndex[*event.OutputIndex]; block != nil {
			return block
		}
	}
	if itemID := responseStreamEventItemID(event); itemID != "" {
		return s.byItemID[itemID]
	}
	return nil
}

func (s *ResponsesToClaudeStreamState) applyBlockMetadata(block *responsesClaudeStreamBlock, event *dto.ResponsesStreamResponse) {
	if block == nil || event == nil {
		return
	}
	if event.OutputIndex != nil {
		s.byOutputIndex[*event.OutputIndex] = block
	}
	if itemID := responseStreamEventItemID(event); itemID != "" {
		block.ItemID = itemID
		s.byItemID[itemID] = block
	}
	if event.Item == nil {
		return
	}
	if callID := strings.TrimSpace(event.Item.CallId); callID != "" {
		block.CallID = callID
	} else if block.CallID == "" {
		block.CallID = strings.TrimSpace(event.Item.ID)
	}
	if name := strings.TrimSpace(event.Item.Name); name != "" {
		block.Name = name
	}
}

func (s *ResponsesToClaudeStreamState) startBlock(block *responsesClaudeStreamBlock, estimatedInputTokens int) []*dto.ClaudeResponse {
	if block == nil || block.Started || block.Stopped {
		return nil
	}
	var content dto.ClaudeMediaMessage
	switch block.Kind {
	case "text":
		content = dto.ClaudeMediaMessage{Type: "text", Text: kitutil.GetPointer("")}
	case "thinking":
		content = dto.ClaudeMediaMessage{Type: "thinking", Thinking: kitutil.GetPointer("")}
	case "tool_use":
		if block.Name == "" {
			return nil
		}
		callID := block.CallID
		if callID == "" {
			callID = block.ItemID
		}
		content = dto.ClaudeMediaMessage{Type: "tool_use", Id: callID, Name: block.Name, Input: map[string]any{}}
		s.sawToolCall = true
	default:
		return nil
	}
	block.Started = true
	responses := s.ensureMessageStart(estimatedInputTokens)
	index := block.Index
	responses = append(responses, &dto.ClaudeResponse{Type: "content_block_start", Index: &index, ContentBlock: &content})
	return responses
}

func (s *ResponsesToClaudeStreamState) appendDelta(block *responsesClaudeStreamBlock, delta string, estimatedInputTokens int) []*dto.ClaudeResponse {
	if block == nil || block.Stopped || delta == "" {
		return nil
	}
	block.Value.WriteString(delta)
	return s.flushBlock(block, estimatedInputTokens)
}

func (s *ResponsesToClaudeStreamState) mergeFinalValue(block *responsesClaudeStreamBlock, finalValue string, estimatedInputTokens int) []*dto.ClaudeResponse {
	if block == nil || block.Stopped {
		return nil
	}
	current := block.Value.String()
	if current == "" {
		block.Value.WriteString(finalValue)
	} else if strings.HasPrefix(finalValue, current) {
		block.Value.WriteString(finalValue[len(current):])
	}
	return s.flushBlock(block, estimatedInputTokens)
}

func (s *ResponsesToClaudeStreamState) flushBlock(block *responsesClaudeStreamBlock, estimatedInputTokens int) []*dto.ClaudeResponse {
	if block == nil || block.Stopped {
		return nil
	}
	responses := s.startBlock(block, estimatedInputTokens)
	if !block.Started {
		return responses
	}
	value := block.Value.String()
	if block.SentBytes >= len(value) {
		return responses
	}
	delta := value[block.SentBytes:]
	block.SentBytes = len(value)
	s.usageText.WriteString(delta)
	index := block.Index
	media := &dto.ClaudeMediaMessage{}
	switch block.Kind {
	case "text":
		media.Type = "text_delta"
		media.Text = &delta
	case "thinking":
		media.Type = "thinking_delta"
		media.Thinking = &delta
	case "tool_use":
		media.Type = "input_json_delta"
		media.PartialJson = &delta
	}
	responses = append(responses, &dto.ClaudeResponse{Type: "content_block_delta", Index: &index, Delta: media})
	return responses
}

func (s *ResponsesToClaudeStreamState) stopBlock(block *responsesClaudeStreamBlock, estimatedInputTokens int) []*dto.ClaudeResponse {
	if block == nil || block.Stopped {
		return nil
	}
	responses := s.flushBlock(block, estimatedInputTokens)
	responses = append(responses, s.startBlock(block, estimatedInputTokens)...)
	if !block.Started {
		return responses
	}
	block.Stopped = true
	index := block.Index
	return append(responses, &dto.ClaudeResponse{Type: "content_block_stop", Index: &index})
}

func (s *ResponsesToClaudeStreamState) applyOutputItem(event *dto.ResponsesStreamResponse, estimatedInputTokens int, stop bool) ([]*dto.ClaudeResponse, error) {
	if event == nil || event.Item == nil {
		return nil, nil
	}
	item := event.Item
	var kind string
	switch item.Type {
	case responsesOutputTypeReasoning:
		kind = "thinking"
	case responsesOutputTypeMessage:
		if item.Role != "" && item.Role != "assistant" {
			return nil, nil
		}
		kind = "text"
	case responsesOutputTypeFunctionCall, responsesOutputTypeCustomToolCall:
		kind = "tool_use"
	default:
		return nil, nil
	}
	block, err := s.ensureBlock(event, kind)
	if err != nil {
		return nil, err
	}
	var responses []*dto.ClaudeResponse
	switch kind {
	case "thinking":
		responses = append(responses, s.mergeFinalValue(block, reasoningOutputText(item), estimatedInputTokens)...)
	case "text":
		var text strings.Builder
		var annotations []any
		for _, content := range item.Content {
			if content.Type != "output_text" {
				continue
			}
			text.WriteString(content.Text)
			annotations = append(annotations, content.Annotations...)
		}
		responses = append(responses, s.mergeFinalValue(block, text.String(), estimatedInputTokens)...)
		responses = append(responses, s.appendAnnotations(block, annotations, estimatedInputTokens, true)...)
	case "tool_use":
		responses = append(responses, s.mergeFinalValue(block, item.ArgumentsString(), estimatedInputTokens)...)
	}
	if stop {
		responses = append(responses, s.stopBlock(block, estimatedInputTokens)...)
	}
	return responses, nil
}

func (s *ResponsesToClaudeStreamState) appendAnnotations(block *responsesClaudeStreamBlock, annotations []any, estimatedInputTokens int, snapshot bool) []*dto.ClaudeResponse {
	if block == nil || block.Kind != "text" || block.Stopped || len(annotations) == 0 {
		return nil
	}
	remaining := annotations
	if snapshot {
		if len(annotations) <= block.AnnotationCount {
			return nil
		}
		remaining = annotations[block.AnnotationCount:]
		block.AnnotationCount = len(annotations)
	} else {
		block.AnnotationCount += len(annotations)
	}
	citations := responsesAnnotationsToClaude(remaining, block.Value.String())
	if len(citations) == 0 {
		return nil
	}
	responses := s.startBlock(block, estimatedInputTokens)
	index := block.Index
	for _, citation := range citations {
		responses = append(responses, &dto.ClaudeResponse{
			Type:  "content_block_delta",
			Index: &index,
			Delta: &dto.ClaudeMediaMessage{Type: "citations_delta", Citation: citation},
		})
	}
	return responses
}

func (s *ResponsesToClaudeStreamState) finish(response *dto.OpenAIResponsesResponse, estimatedInputTokens int) ([]*dto.ClaudeResponse, error) {
	if s.done {
		return nil, nil
	}
	s.applyResponseMetadata(response)
	responses := make([]*dto.ClaudeResponse, 0)
	if response != nil {
		for outputIndex := range response.Output {
			index := outputIndex
			item := response.Output[outputIndex]
			event := &dto.ResponsesStreamResponse{OutputIndex: &index, ItemID: item.ID, Item: &item}
			itemResponses, err := s.applyOutputItem(event, estimatedInputTokens, true)
			if err != nil {
				return nil, err
			}
			responses = append(responses, itemResponses...)
		}
	}
	for _, block := range s.blocks {
		responses = append(responses, s.stopBlock(block, estimatedInputTokens)...)
	}
	responses = append(responses, s.ensureMessageStart(estimatedInputTokens)...)
	stopReason := responsesClaudeStopReason(response, s.sawToolCall)
	usage := sharedclaude.UsageFromOpenAI(s.Usage)
	responses = append(responses,
		&dto.ClaudeResponse{
			Type:  "message_delta",
			Usage: usage,
			Delta: &dto.ClaudeMediaMessage{StopReason: &stopReason},
		},
		&dto.ClaudeResponse{Type: "message_stop"},
	)
	s.done = true
	return responses, nil
}

func separatedResponsesDelta(delta string) string {
	if strings.HasPrefix(delta, "\n\n") {
		return delta
	}
	if strings.HasPrefix(delta, "\n") {
		return "\n" + delta
	}
	return "\n\n" + delta
}
