package helps

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/cursorproto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const cursorMCPProvider = "cliproxy"

type cursorOpenAIRequest struct {
	Model      string                `json:"model"`
	Messages   []cursorOpenAIMessage `json:"messages"`
	Tools      []cursorOpenAITool    `json:"tools"`
	ToolChoice json.RawMessage       `json:"tool_choice"`
}

type cursorOpenAIMessage struct {
	Role             string                 `json:"role"`
	Content          json.RawMessage        `json:"content"`
	ReasoningContent string                 `json:"reasoning_content,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	ToolCalls        []cursorOpenAIToolCall `json:"tool_calls,omitempty"`
}

type cursorOpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type cursorOpenAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type cursorImage struct {
	MIME string
	Data []byte
}

type cursorTurnStep struct {
	Kind       string
	Text       string
	ToolCallID string
	ToolName   string
	Arguments  map[string]any
	Result     *cursorToolResult
}

type cursorToolResult struct {
	Content string
	IsError bool
}

type cursorTurn struct {
	UserText string
	Images   []cursorImage
	Steps    []cursorTurnStep
}

// CursorRunPayload is the first message and content-addressed blob store for one Run stream.
type CursorRunPayload struct {
	Message        []byte
	Blobs          map[string][]byte
	Tools          []*cursorproto.McpToolDefinition
	ConversationID string
}

// BuildCursorRunPayload builds a stateless Cursor conversation from an OpenAI Chat request.
func BuildCursorRunPayload(payload []byte, modelID string) (*CursorRunPayload, error) {
	var request cursorOpenAIRequest
	if errJSON := json.Unmarshal(payload, &request); errJSON != nil {
		return nil, fmt.Errorf("cursor request: decode OpenAI payload: %w", errJSON)
	}
	parsed, errParse := parseCursorMessages(request.Messages)
	if errParse != nil {
		return nil, errParse
	}
	selectedTools := filterCursorTools(request.Tools, request.ToolChoice)
	tools, errTools := buildCursorMCPTools(selectedTools)
	if errTools != nil {
		return nil, errTools
	}

	blobs := make(map[string][]byte)
	systemJSON, errSystem := json.Marshal(map[string]string{"role": "system", "content": parsed.SystemPrompt})
	if errSystem != nil {
		return nil, fmt.Errorf("cursor request: encode system prompt: %w", errSystem)
	}
	systemBlobID := storeCursorBlob(blobs, systemJSON)
	selectedContextBlob := storeCursorBlob(blobs, encodeCursorSelectedContext([][]byte{systemBlobID}, cursorMCPProvider))

	turnIDs := make([][]byte, 0, len(parsed.Turns))
	for _, turn := range parsed.Turns {
		turnID, errTurn := storeCursorTurn(blobs, turn, selectedContextBlob)
		if errTurn != nil {
			return nil, errTurn
		}
		turnIDs = append(turnIDs, turnID)
	}
	mode := int32(1)
	state := &cursorproto.ConversationStateStructure{
		RootPromptMessagesJson: [][]byte{systemBlobID},
		Turns:                  turnIDs,
		Mode:                   &mode,
		ClientName:             cursorMCPProvider,
	}

	var action *cursorproto.ConversationAction
	if parsed.Resume {
		action = &cursorproto.ConversationAction{Action: &cursorproto.ConversationAction_ResumeAction{
			ResumeAction: &cursorproto.ResumeAction{RequestContext: &cursorproto.RequestContext{Tools: tools}},
		}}
	} else {
		userMessage := buildCursorUserMessage(parsed.UserText, parsed.UserImages, selectedContextBlob)
		action = &cursorproto.ConversationAction{Action: &cursorproto.ConversationAction_UserMessageAction{
			UserMessageAction: &cursorproto.UserMessageAction{UserMessage: userMessage},
		}}
	}
	conversationID := uuid.NewString()
	run := &cursorproto.AgentRunRequest{
		ConversationState: state,
		Action:            action,
		ModelDetails: &cursorproto.ModelDetails{
			ModelId:        modelID,
			DisplayModelId: modelID,
			DisplayName:    modelID,
		},
		McpTools:       &cursorproto.McpTools{McpTools: tools},
		ConversationId: proto.String(conversationID),
	}
	message, errMarshal := proto.Marshal(&cursorproto.AgentClientMessage{
		Message: &cursorproto.AgentClientMessage_RunRequest{RunRequest: run},
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("cursor request: encode run message: %w", errMarshal)
	}
	return &CursorRunPayload{
		Message:        message,
		Blobs:          blobs,
		Tools:          tools,
		ConversationID: conversationID,
	}, nil
}

func filterCursorTools(tools []cursorOpenAITool, rawChoice json.RawMessage) []cursorOpenAITool {
	if len(rawChoice) == 0 || string(rawChoice) == "null" {
		return tools
	}
	var choice string
	if errString := json.Unmarshal(rawChoice, &choice); errString == nil {
		if strings.EqualFold(strings.TrimSpace(choice), "none") {
			return nil
		}
		return tools
	}
	var selected struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if errJSON := json.Unmarshal(rawChoice, &selected); errJSON != nil || strings.TrimSpace(selected.Function.Name) == "" {
		return tools
	}
	name := strings.TrimSpace(selected.Function.Name)
	filtered := make([]cursorOpenAITool, 0, 1)
	for _, tool := range tools {
		if tool.Function.Name == name {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

type parsedCursorMessages struct {
	SystemPrompt string
	Turns        []cursorTurn
	UserText     string
	UserImages   []cursorImage
	Resume       bool
}

func parseCursorMessages(messages []cursorOpenAIMessage) (*parsedCursorMessages, error) {
	systemParts := make([]string, 0)
	turns := make([]cursorTurn, 0)
	var current *cursorTurn
	toolSteps := make(map[string]int)
	lastRole := ""
	finalize := func() {
		if current != nil {
			turns = append(turns, *current)
		}
		current = nil
		toolSteps = make(map[string]int)
	}

	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "system" || role == "developer" {
			text, _, errContent := parseCursorContent(message.Content)
			if errContent != nil {
				return nil, errContent
			}
			if text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		lastRole = role
		switch role {
		case "user":
			finalize()
			text, images, errContent := parseCursorContent(message.Content)
			if errContent != nil {
				return nil, errContent
			}
			current = &cursorTurn{UserText: text, Images: images}
		case "assistant":
			if current == nil {
				return nil, fmt.Errorf("cursor request: assistant message has no preceding user message")
			}
			if reasoning := strings.TrimSpace(message.ReasoningContent); reasoning != "" {
				current.Steps = append(current.Steps, cursorTurnStep{Kind: "thinking", Text: reasoning})
			}
			text, _, errContent := parseCursorContent(message.Content)
			if errContent != nil {
				return nil, errContent
			}
			if text != "" {
				current.Steps = append(current.Steps, cursorTurnStep{Kind: "assistant", Text: text})
			}
			for _, call := range message.ToolCalls {
				arguments := parseCursorToolArguments(call.Function.Arguments)
				step := cursorTurnStep{
					Kind:       "tool",
					ToolCallID: strings.TrimSpace(call.ID),
					ToolName:   strings.TrimSpace(call.Function.Name),
					Arguments:  arguments,
				}
				if step.ToolCallID == "" {
					step.ToolCallID = uuid.NewString()
				}
				current.Steps = append(current.Steps, step)
				toolSteps[step.ToolCallID] = len(current.Steps) - 1
			}
		case "tool":
			if current == nil {
				return nil, fmt.Errorf("cursor request: tool result has no preceding user message")
			}
			text, _, errContent := parseCursorContent(message.Content)
			if errContent != nil {
				return nil, errContent
			}
			callID := strings.TrimSpace(message.ToolCallID)
			stepIndex, okStep := toolSteps[callID]
			if !okStep || stepIndex < 0 || stepIndex >= len(current.Steps) {
				return nil, fmt.Errorf("cursor request: tool result references unknown call %q", callID)
			}
			current.Steps[stepIndex].Result = &cursorToolResult{Content: text}
		default:
			return nil, fmt.Errorf("cursor request: unsupported message role %q", role)
		}
	}

	result := &parsedCursorMessages{SystemPrompt: strings.Join(systemParts, "\n")}
	if result.SystemPrompt == "" {
		result.SystemPrompt = "You are a helpful assistant."
	}
	switch lastRole {
	case "user":
		if current == nil {
			return nil, fmt.Errorf("cursor request: missing final user message")
		}
		result.Turns = turns
		result.UserText = current.UserText
		result.UserImages = current.Images
	case "tool":
		finalize()
		result.Turns = turns
		result.Resume = true
	default:
		return nil, fmt.Errorf("cursor request: conversation must end with a user or tool message")
	}
	return result, nil
}

func parseCursorContent(raw json.RawMessage) (string, []cursorImage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil, nil
	}
	var plain string
	if errString := json.Unmarshal(raw, &plain); errString == nil {
		return plain, nil, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if errParts := json.Unmarshal(raw, &parts); errParts != nil {
		return "", nil, fmt.Errorf("cursor request: invalid message content: %w", errParts)
	}
	texts := make([]string, 0, len(parts))
	images := make([]cursorImage, 0)
	for _, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text", "input_text", "output_text":
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		case "image_url", "input_image":
			image, errImage := parseCursorDataImage(part.ImageURL.URL)
			if errImage != nil {
				return "", nil, errImage
			}
			images = append(images, image)
		}
	}
	return strings.Join(texts, "\n"), images, nil
}

func parseCursorDataImage(value string) (cursorImage, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "data:image/") {
		return cursorImage{}, fmt.Errorf("cursor request: remote image URLs are not supported; use a base64 data:image URL")
	}
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return cursorImage{}, fmt.Errorf("cursor request: malformed image data URL")
	}
	metadata := value[:comma]
	if !strings.HasSuffix(strings.ToLower(metadata), ";base64") {
		return cursorImage{}, fmt.Errorf("cursor request: image data URL must use base64 encoding")
	}
	mime := strings.TrimPrefix(strings.Split(metadata, ";")[0], "data:")
	decoded, errDecode := base64.StdEncoding.DecodeString(value[comma+1:])
	if errDecode != nil || len(decoded) == 0 {
		return cursorImage{}, fmt.Errorf("cursor request: invalid base64 image data")
	}
	return cursorImage{MIME: mime, Data: decoded}, nil
}

func buildCursorMCPTools(tools []cursorOpenAITool) ([]*cursorproto.McpToolDefinition, error) {
	result := make([]*cursorproto.McpToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if strings.ToLower(strings.TrimSpace(tool.Type)) != "function" {
			continue
		}
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			return nil, fmt.Errorf("cursor request: function tool name is empty")
		}
		schema := any(map[string]any{"type": "object", "properties": map[string]any{}})
		if len(tool.Function.Parameters) > 0 && string(tool.Function.Parameters) != "null" {
			if errJSON := json.Unmarshal(tool.Function.Parameters, &schema); errJSON != nil {
				return nil, fmt.Errorf("cursor request: invalid schema for tool %s: %w", name, errJSON)
			}
		}
		value, errValue := structpb.NewValue(schema)
		if errValue != nil {
			return nil, fmt.Errorf("cursor request: encode schema for tool %s: %w", name, errValue)
		}
		encoded, errMarshal := proto.Marshal(value)
		if errMarshal != nil {
			return nil, fmt.Errorf("cursor request: marshal schema for tool %s: %w", name, errMarshal)
		}
		result = append(result, &cursorproto.McpToolDefinition{
			Name:               name,
			Description:        tool.Function.Description,
			InputSchema:        encoded,
			ProviderIdentifier: cursorMCPProvider,
			ToolName:           name,
		})
	}
	return result, nil
}

func buildCursorUserMessage(text string, images []cursorImage, selectedContextBlob []byte) *cursorproto.UserMessage {
	messageID := uuid.NewString()
	selectedImages := make([]*cursorproto.SelectedImage, 0, len(images))
	for _, image := range images {
		selectedImages = append(selectedImages, &cursorproto.SelectedImage{
			Uuid:         uuid.NewString(),
			Path:         "",
			MimeType:     image.MIME,
			DataOrBlobId: &cursorproto.SelectedImage_Data{Data: image.Data},
		})
	}
	return &cursorproto.UserMessage{
		Text:                text,
		MessageId:           messageID,
		SelectedContext:     &cursorproto.SelectedContext{SelectedImages: selectedImages},
		Mode:                1,
		SelectedContextBlob: selectedContextBlob,
		CorrelationId:       messageID,
	}
}

func storeCursorTurn(blobs map[string][]byte, turn cursorTurn, selectedContextBlob []byte) ([]byte, error) {
	userBytes, errUser := proto.Marshal(buildCursorUserMessage(turn.UserText, turn.Images, selectedContextBlob))
	if errUser != nil {
		return nil, fmt.Errorf("cursor request: encode historical user message: %w", errUser)
	}
	userID := storeCursorBlob(blobs, userBytes)
	stepIDs := make([][]byte, 0, len(turn.Steps))
	for _, step := range turn.Steps {
		stepBytes, errStep := encodeCursorTurnStep(step)
		if errStep != nil {
			return nil, errStep
		}
		stepIDs = append(stepIDs, storeCursorBlob(blobs, stepBytes))
	}
	requestID := uuid.NewString()
	turnBytes, errTurn := proto.Marshal(&cursorproto.ConversationTurnStructure{
		Turn: &cursorproto.ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &cursorproto.AgentConversationTurnStructure{
				UserMessage: userID,
				Steps:       stepIDs,
				RequestId:   &requestID,
			},
		},
	})
	if errTurn != nil {
		return nil, fmt.Errorf("cursor request: encode historical turn: %w", errTurn)
	}
	return storeCursorBlob(blobs, turnBytes), nil
}

func encodeCursorTurnStep(step cursorTurnStep) ([]byte, error) {
	message := &cursorproto.ConversationStep{}
	switch step.Kind {
	case "assistant":
		message.Message = &cursorproto.ConversationStep_AssistantMessage{AssistantMessage: &cursorproto.AssistantMessage{Text: step.Text}}
	case "thinking":
		message.Message = &cursorproto.ConversationStep_ThinkingMessage{ThinkingMessage: &cursorproto.ThinkingMessage{Text: step.Text}}
	case "tool":
		args := make(map[string][]byte, len(step.Arguments))
		for key, item := range step.Arguments {
			value, errValue := structpb.NewValue(item)
			if errValue != nil {
				return nil, fmt.Errorf("cursor request: encode tool argument %s: %w", key, errValue)
			}
			encoded, errMarshal := proto.Marshal(value)
			if errMarshal != nil {
				return nil, fmt.Errorf("cursor request: marshal tool argument %s: %w", key, errMarshal)
			}
			args[key] = encoded
		}
		mcpCall := &cursorproto.McpToolCall{Args: &cursorproto.McpArgs{
			Name:               step.ToolName,
			Args:               args,
			ToolCallId:         step.ToolCallID,
			ProviderIdentifier: cursorMCPProvider,
			ToolName:           step.ToolName,
		}}
		if step.Result != nil {
			if step.Result.IsError {
				mcpCall.Result = &cursorproto.McpToolResult{Result: &cursorproto.McpToolResult_Error{Error: &cursorproto.McpToolError{Error: step.Result.Content}}}
			} else {
				mcpCall.Result = &cursorproto.McpToolResult{Result: &cursorproto.McpToolResult_Success{Success: &cursorproto.McpSuccess{
					Content: []*cursorproto.McpToolResultContentItem{{Content: &cursorproto.McpToolResultContentItem_Text{Text: &cursorproto.McpTextContent{Text: step.Result.Content}}}},
				}}}
			}
		}
		message.Message = &cursorproto.ConversationStep_ToolCall{ToolCall: &cursorproto.ToolCall{
			Tool: &cursorproto.ToolCall_McpToolCall{McpToolCall: mcpCall},
		}}
	default:
		return nil, fmt.Errorf("cursor request: unsupported historical step %q", step.Kind)
	}
	encoded, errMarshal := proto.Marshal(message)
	if errMarshal != nil {
		return nil, fmt.Errorf("cursor request: encode historical step: %w", errMarshal)
	}
	return encoded, nil
}

func parseCursorToolArguments(raw string) map[string]any {
	result := make(map[string]any)
	if errJSON := json.Unmarshal([]byte(raw), &result); errJSON == nil {
		return result
	}
	if strings.TrimSpace(raw) != "" {
		result["__raw"] = raw
	}
	return result
}

func storeCursorBlob(blobs map[string][]byte, data []byte) []byte {
	digest := sha256.Sum256(data)
	id := append([]byte(nil), digest[:]...)
	blobs[hex.EncodeToString(id)] = append([]byte(nil), data...)
	return id
}

func encodeCursorSelectedContext(rootPromptBlobIDs [][]byte, clientName string) []byte {
	result := make([]byte, 0, len(rootPromptBlobIDs)*34+len(clientName)+4)
	for _, blobID := range rootPromptBlobIDs {
		result = append(result, 0x0a, byte(len(blobID)))
		result = append(result, blobID...)
	}
	client := []byte(clientName)
	result = append(result, 0xb2, 0x01, byte(len(client)))
	result = append(result, client...)
	return result
}
