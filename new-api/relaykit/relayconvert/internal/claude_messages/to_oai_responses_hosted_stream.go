package claudemessages

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	oaichat "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/oai_chat"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

// ClaudeHostedStreamBridge keeps Anthropic server-executed tool blocks out of
// the Chat Completions pivot. Anthropic streams a hosted call's input after the
// content_block_start event, so the bridge owns those input_json_delta events
// until content_block_stop and only then starts the Responses output item.
type ClaudeHostedStreamBridge struct {
	pending map[int]*claudeHostedStreamCall
}

type claudeHostedStreamCall struct {
	blockType  string
	id         string
	name       string
	serverName string
	caller     []byte
	startInput []byte
	input      strings.Builder
}

func NewClaudeHostedStreamBridge() *ClaudeHostedStreamBridge {
	return &ClaudeHostedStreamBridge{pending: make(map[int]*claudeHostedStreamCall)}
}

// Convert consumes provider-hosted stream frames and reports whether the frame
// must be skipped by the ordinary Claude-to-Chat converter.
func (b *ClaudeHostedStreamBridge) Convert(response *dto.ClaudeResponse, state *oaichat.ChatToResponsesStreamState) ([]oaichat.ChatToResponsesStreamEvent, bool, error) {
	if response == nil || state == nil {
		return nil, false, nil
	}
	if b == nil {
		return nil, false, fmt.Errorf("Claude hosted stream bridge is required")
	}
	if b.pending == nil {
		b.pending = make(map[int]*claudeHostedStreamCall)
	}
	index := response.GetIndex()

	switch response.Type {
	case "content_block_start":
		if response.ContentBlock == nil {
			return nil, false, nil
		}
		block := response.ContentBlock
		blockType := strings.TrimSpace(block.Type)
		switch blockType {
		case "server_tool_use", "mcp_tool_use":
			if _, exists := b.pending[index]; exists {
				return nil, true, fmt.Errorf("duplicate Claude hosted-tool content block index %d", index)
			}
			if blockType == "mcp_tool_use" && (strings.TrimSpace(block.Name) == "" || strings.TrimSpace(block.ServerName) == "") {
				return nil, true, fmt.Errorf("Claude MCP tool use must include name and server_name")
			}
			if _, err := claudeHostedCallOutputType(blockType, block.Name); err != nil {
				return nil, true, err
			}
			pending := &claudeHostedStreamCall{
				blockType:  blockType,
				id:         block.Id,
				name:       block.Name,
				serverName: block.ServerName,
				caller:     append([]byte(nil), block.Caller...),
			}
			// Non-stream-shaped gateways occasionally include the complete input
			// on the start frame. Preserve it as a fallback, while streamed deltas
			// replace the placeholder at block completion.
			if block.Input != nil {
				input, err := kitutil.Marshal(block.Input)
				if err != nil {
					return nil, true, fmt.Errorf("marshal Claude hosted-tool input: %w", err)
				}
				if string(input) != "{}" && string(input) != "null" {
					pending.startInput = input
				}
			}
			b.pending[index] = pending
			return nil, true, nil
		case "web_search_tool_result", "mcp_tool_result":
			outputType, err := claudeHostedResultOutputType(blockType)
			if err != nil {
				return nil, true, err
			}
			var result []byte
			if outputType != "web_search_call" {
				result, err = kitutil.Marshal(block.Content)
				if err != nil {
					return nil, true, fmt.Errorf("marshal Claude hosted-tool result: %w", err)
				}
			}
			events, err := state.CompleteHostedTool(oaichat.HostedToolStreamResult{
				Type:      outputType,
				ID:        block.ToolUseId,
				Result:    result,
				ErrorCode: claudeHostedResultErrorCode(block.Content, block.ErrorCode),
				IsError:   block.IsError != nil && *block.IsError,
			})
			return events, true, err
		default:
			return nil, false, nil
		}
	case "content_block_delta":
		pending := b.pending[index]
		if pending == nil {
			return nil, false, nil
		}
		if response.Delta != nil && response.Delta.Type == "input_json_delta" && response.Delta.PartialJson != nil {
			pending.input.WriteString(*response.Delta.PartialJson)
		}
		return nil, true, nil
	case "content_block_stop":
		pending := b.pending[index]
		if pending == nil {
			return nil, false, nil
		}
		delete(b.pending, index)
		action := []byte(pending.input.String())
		if len(action) == 0 {
			action = pending.startInput
		}
		if len(action) == 0 {
			action = []byte("{}")
		}
		outputType, err := claudeHostedCallOutputType(pending.blockType, pending.name)
		if err != nil {
			return nil, true, err
		}
		events, err := state.StartHostedTool(oaichat.HostedToolStreamStart{
			Type:        outputType,
			ID:          pending.id,
			Name:        pending.name,
			Action:      action,
			Caller:      pending.caller,
			ServerLabel: pending.serverName,
		})
		return events, true, err
	default:
		return nil, false, nil
	}
}

func claudeHostedCallOutputType(blockType string, name string) (string, error) {
	if blockType == "mcp_tool_use" {
		return "mcp_call", nil
	}
	switch strings.TrimSpace(name) {
	case "web_search":
		return "web_search_call", nil
	case "code_execution":
		return "", fmt.Errorf("Claude code_execution has no valid OpenAI Responses mapping without a container_id")
	case "web_fetch":
		return "", fmt.Errorf("Claude web_fetch has no valid OpenAI Responses hosted-tool mapping")
	default:
		return "", fmt.Errorf("unknown Claude server tool %q cannot be represented as an OpenAI Responses hosted tool", name)
	}
}

func claudeHostedResultOutputType(blockType string) (string, error) {
	switch blockType {
	case "web_search_tool_result":
		return "web_search_call", nil
	case "mcp_tool_result":
		return "mcp_call", nil
	case "code_execution_tool_result":
		return "", fmt.Errorf("Claude code_execution result has no valid OpenAI Responses mapping without a container_id")
	case "web_fetch_tool_result":
		return "", fmt.Errorf("Claude web_fetch result has no valid OpenAI Responses hosted-tool mapping")
	default:
		return "", fmt.Errorf("unknown Claude hosted-tool result %q", blockType)
	}
}

func claudeHostedResultErrorCode(content any, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	value, ok := content.(map[string]any)
	if !ok {
		return ""
	}
	contentType := strings.TrimSpace(kitutil.Interface2String(value["type"]))
	if !strings.HasSuffix(contentType, "_error") {
		return ""
	}
	return strings.TrimSpace(kitutil.Interface2String(value["error_code"]))
}
