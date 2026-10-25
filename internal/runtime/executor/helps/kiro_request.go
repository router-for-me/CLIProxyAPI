package helps

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type kiroTurn struct {
	role    string
	text    []string
	images  []any
	uses    []any
	results []any
}

// BuildKiroRequest converts the existing Claude request representation to Kiro's wire payload.
func BuildKiroRequest(body []byte, model, profileARN string) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("kiro: invalid request JSON")
	}
	root := gjson.ParseBytes(body)
	if choice := root.Get("tool_choice.type").String(); choice != "" && choice != "auto" {
		return nil, fmt.Errorf("kiro: tool_choice %q is not supported by the upstream", choice)
	}
	turns := make([]kiroTurn, 0)
	for _, message := range root.Get("messages").Array() {
		role := message.Get("role").String()
		if role != "user" && role != "assistant" {
			return nil, fmt.Errorf("kiro: unsupported message role %q", role)
		}
		turn := kiroTurn{role: role}
		content := message.Get("content")
		blocks := content.Array()
		if content.Type == gjson.String {
			turn.text = append(turn.text, content.String())
			blocks = nil
		}
		for _, block := range blocks {
			switch block.Get("type").String() {
			case "text":
				turn.text = append(turn.text, block.Get("text").String())
			case "thinking", "redacted_thinking":
				continue
			case "image":
				if role != "user" {
					return nil, fmt.Errorf("kiro: images require a user message")
				}
				image, errImage := kiroImage(block)
				if errImage != nil {
					return nil, errImage
				}
				turn.images = append(turn.images, image)
			case "tool_use":
				if role != "assistant" || block.Get("id").String() == "" || block.Get("name").String() == "" || !block.Get("input").IsObject() {
					return nil, fmt.Errorf("kiro: invalid tool use")
				}
				turn.uses = append(turn.uses, map[string]any{"toolUseId": block.Get("id").String(), "name": block.Get("name").String(), "input": json.RawMessage(block.Get("input").Raw)})
			case "tool_result":
				if role != "user" || block.Get("tool_use_id").String() == "" {
					return nil, fmt.Errorf("kiro: invalid tool result")
				}
				status := "success"
				if block.Get("is_error").Bool() {
					status = "error"
				}
				content := block.Get("content")
				if content.Type != gjson.Null && content.Type != gjson.String && !content.IsArray() {
					return nil, fmt.Errorf("kiro: tool result content must be text or an array")
				}
				if content.IsArray() {
					for _, resultBlock := range content.Array() {
						switch resultBlock.Get("type").String() {
						case "text":
						case "image":
							image, errImage := kiroImage(resultBlock)
							if errImage != nil {
								return nil, errImage
							}
							turn.images = append(turn.images, image)
						default:
							return nil, fmt.Errorf("kiro: unsupported tool result block %q", resultBlock.Get("type").String())
						}
					}
				}
				turn.results = append(turn.results, map[string]any{"toolUseId": block.Get("tool_use_id").String(), "status": status, "content": []any{map[string]any{"text": kiroText(content)}}})
			default:
				return nil, fmt.Errorf("kiro: unsupported content block %q", block.Get("type").String())
			}
		}
		if len(turns) > 0 && turns[len(turns)-1].role == role {
			previous := &turns[len(turns)-1]
			previous.text = append(previous.text, turn.text...)
			previous.images = append(previous.images, turn.images...)
			previous.uses = append(previous.uses, turn.uses...)
			previous.results = append(previous.results, turn.results...)
		} else {
			turns = append(turns, turn)
		}
	}
	if len(turns) == 0 {
		return nil, fmt.Errorf("kiro: at least one user message is required")
	}
	if turns[0].role != "user" {
		return nil, fmt.Errorf("kiro: conversation must start with a user message")
	}
	if turns[len(turns)-1].role != "user" {
		return nil, fmt.Errorf("kiro: assistant prefill is not supported")
	}
	if system := kiroText(root.Get("system")); system != "" {
		turns[0].text = append([]string{system}, turns[0].text...)
	}
	tools := make([]any, 0)
	for _, tool := range root.Get("tools").Array() {
		name := tool.Get("name").String()
		if name == "" {
			return nil, fmt.Errorf("kiro: server-side tools are not supported")
		}
		schema := tool.Get("input_schema")
		if !schema.IsObject() {
			return nil, fmt.Errorf("kiro: tool %q requires an object input schema", name)
		}
		description := tool.Get("description").String()
		if description == "" {
			description = name
		}
		tools = append(tools, map[string]any{"toolSpecification": map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"json": json.RawMessage(schema.Raw)}}})
	}
	history := make([]any, 0, len(turns)-1)
	var current map[string]any
	for i, turn := range turns {
		content := strings.Join(turn.text, "\n\n")
		if turn.role == "assistant" {
			message := map[string]any{"content": content}
			if len(turn.uses) > 0 {
				message["toolUses"] = turn.uses
			}
			history = append(history, map[string]any{"assistantResponseMessage": message})
			continue
		}
		message := map[string]any{"content": content, "modelId": model, "origin": "AI_EDITOR"}
		if content == "" {
			message["content"] = "Continue."
		}
		if len(turn.images) > 0 {
			message["images"] = turn.images
		}
		messageContext := map[string]any{}
		if len(turn.results) > 0 {
			messageContext["toolResults"] = turn.results
		}
		if i == len(turns)-1 && len(tools) > 0 {
			messageContext["tools"] = tools
		}
		if len(messageContext) > 0 {
			message["userInputMessageContext"] = messageContext
		}
		if i == len(turns)-1 {
			current = message
		} else {
			history = append(history, map[string]any{"userInputMessage": message})
		}
	}
	state := map[string]any{"conversationId": uuid.NewString(), "chatTriggerType": "MANUAL", "currentMessage": map[string]any{"userInputMessage": current}}
	if len(history) > 0 {
		state["history"] = history
	}
	payload := map[string]any{"conversationState": state}
	if profileARN != "" {
		payload["profileArn"] = profileARN
	}
	return json.Marshal(payload)
}

func kiroImage(block gjson.Result) (map[string]any, error) {
	if block.Get("source.type").String() != "base64" {
		return nil, fmt.Errorf("kiro: images must use inline base64 data")
	}
	format := strings.TrimPrefix(block.Get("source.media_type").String(), "image/")
	if format == "jpg" {
		format = "jpeg"
	}
	switch format {
	case "png", "jpeg", "gif", "webp":
	default:
		return nil, fmt.Errorf("kiro: unsupported image format %q", format)
	}
	data := block.Get("source.data").String()
	if decoded, err := base64.StdEncoding.DecodeString(data); err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("kiro: invalid image data")
	}
	return map[string]any{"format": format, "source": map[string]any{"bytes": data}}, nil
}

func kiroText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	parts := make([]string, 0)
	for _, block := range content.Array() {
		if block.Get("type").String() == "text" {
			parts = append(parts, block.Get("text").String())
		}
	}
	return strings.Join(parts, "\n")
}
