package input

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/tidwall/gjson"
)

// Request is a lossless view of a Claude protocol request. It keeps the wire
// payload intact while exposing provider-independent Claude fields to direct
// pairwise adapters.
type Request struct {
	resolvedModel string
	raw           []byte
	root          gjson.Result
}

// Parse creates a lossless Claude request view.
func Parse(resolvedModel string, raw []byte) Request {
	return Request{
		resolvedModel: resolvedModel,
		raw:           raw,
		root:          gjson.ParseBytes(raw),
	}
}

// Raw returns the original request bytes without copying or rewriting them.
func (r Request) Raw() []byte {
	return r.raw
}

// Root returns the parsed Claude request object.
func (r Request) Root() gjson.Result {
	return r.root
}

// ResolvedModel returns the model selected by routing and alias resolution.
func (r Request) ResolvedModel() string {
	return r.resolvedModel
}

// SourceModel returns the model namespace supplied on the Claude wire request.
func (r Request) SourceModel() string {
	return r.root.Get("model").String()
}

// ModelOrSource prefers the routed model and falls back to the wire model.
func (r Request) ModelOrSource() string {
	if r.resolvedModel != "" {
		return r.resolvedModel
	}
	return r.SourceModel()
}

// System returns the top-level Claude system content.
func (r Request) System() gjson.Result {
	return r.root.Get("system")
}

// MessagesResult returns the lossless messages array.
func (r Request) MessagesResult() gjson.Result {
	return r.root.Get("messages")
}

// Messages returns the ordered Claude messages.
func (r Request) Messages() []gjson.Result {
	messages := r.MessagesResult()
	if !messages.IsArray() {
		return nil
	}
	return messages.Array()
}

// Tools returns the Claude tool declarations.
func (r Request) Tools() gjson.Result {
	return r.root.Get("tools")
}

// ToolChoice returns the Claude tool selection policy.
func (r Request) ToolChoice() gjson.Result {
	return r.root.Get("tool_choice")
}

// Thinking returns the Claude thinking configuration.
func (r Request) Thinking() gjson.Result {
	return r.root.Get("thinking")
}

// OutputConfig returns the Claude output configuration.
func (r Request) OutputConfig() gjson.Result {
	return r.root.Get("output_config")
}

// DecodeObject decodes the lossless Claude request into its JSON object while
// preserving JSON number spelling for pairwise validation.
func (r Request) DecodeObject() (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(r.raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("Claude request must be a valid JSON object: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("Claude request must contain exactly one JSON object")
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Claude request must be a valid JSON object")
	}
	return root, nil
}

// Validate checks provider-independent Claude request structure. Pairwise
// adapters remain responsible for counterpart restrictions and field mapping.
func (r Request) Validate() error {
	object, err := r.DecodeObject()
	if err != nil {
		return err
	}
	messagesValue, exists := object["messages"]
	if !exists {
		return nil
	}
	messages, ok := messagesValue.([]any)
	if !ok {
		return fmt.Errorf("Claude messages must be an array")
	}
	for messageIndex, messageValue := range messages {
		message, ok := messageValue.(map[string]any)
		if !ok {
			return fmt.Errorf("Claude message %d must be an object", messageIndex)
		}
		content, exists := message["content"]
		if !exists {
			continue
		}
		switch content := content.(type) {
		case string:
		case []any:
			for blockIndex, blockValue := range content {
				block, ok := blockValue.(map[string]any)
				if !ok {
					return fmt.Errorf("Claude message %d content block %d must be an object", messageIndex, blockIndex)
				}
				blockType, ok := block["type"].(string)
				if !ok || blockType == "" {
					return fmt.Errorf("Claude message %d content block %d requires a type", messageIndex, blockIndex)
				}
			}
		default:
			return fmt.Errorf("Claude message %d content must be text or an array", messageIndex)
		}
	}
	return nil
}

// MessageRole returns a Claude message role.
func MessageRole(message gjson.Result) string {
	return message.Get("role").String()
}

// Block is a provider-independent view of one Claude content block.
type Block struct {
	value gjson.Result
}

// MessageBlocks returns ordered content blocks for array-form Claude content.
func MessageBlocks(message gjson.Result) []Block {
	content := message.Get("content")
	if !content.IsArray() {
		return nil
	}
	values := content.Array()
	blocks := make([]Block, len(values))
	for i, value := range values {
		blocks[i] = Block{value: value}
	}
	return blocks
}

// Value returns the lossless parsed block.
func (b Block) Value() gjson.Result {
	return b.value
}

// Type returns the Claude content block type.
func (b Block) Type() string {
	return b.value.Get("type").String()
}

// ToolUseID returns the identity declared by a tool_use block.
func (b Block) ToolUseID() string {
	return b.value.Get("id").String()
}

// ToolResultID returns the tool_use identity referenced by a tool_result block.
func (b Block) ToolResultID() string {
	return b.value.Get("tool_use_id").String()
}
