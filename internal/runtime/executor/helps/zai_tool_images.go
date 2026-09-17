package helps

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	zaiToolImageMarker    = "Tool-produced image follows as visual context; treat it as tool output."
	zaiToolImageReference = "Tool-produced image appears in the following visual context."
)

// NormalizeZAIToolOutputImages makes image-bearing tool output visible to ZAI GLM Flash.
func NormalizeZAIToolOutputImages(model, baseURL string, body []byte) []byte {
	if !strings.EqualFold(strings.TrimSpace(model), "glm-5.3-flash") || !strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(baseURL), "/"), "https://api.z.ai/api/v1") {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	updated := make([]json.RawMessage, 0, len(items))
	changed := false
	for _, item := range items {
		if item.Get("type").String() != "function_call_output" {
			updated = append(updated, json.RawMessage(item.Raw))
			continue
		}
		images, output, moved := moveZAIToolOutputImages(item.Get("output"))
		if !moved {
			updated = append(updated, json.RawMessage(item.Raw))
			continue
		}
		toolItem, err := sjson.SetRawBytes([]byte(item.Raw), "output", output)
		if err != nil {
			return body
		}
		updated = append(updated, json.RawMessage(toolItem))
		changed = true
		promotion, err := zaiToolImageMessage(images)
		if err != nil {
			return body
		}
		updated = append(updated, json.RawMessage(promotion))
	}
	if !changed {
		return body
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return body
	}
	out, err := sjson.SetRawBytes(body, "input", encoded)
	if err != nil {
		return body
	}
	return out
}

func moveZAIToolOutputImages(output gjson.Result) ([]json.RawMessage, []byte, bool) {
	wasString := output.Type == gjson.String
	if wasString {
		if !gjson.Valid(output.String()) {
			return nil, nil, false
		}
		output = gjson.Parse(output.String())
	}
	if !output.IsArray() {
		return nil, nil, false
	}
	images := make([]json.RawMessage, 0)
	remaining := make([]json.RawMessage, 0, len(output.Array())+1)
	output.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "input_image" || part.Get("image_url").Type != gjson.String || strings.TrimSpace(part.Get("image_url").String()) == "" {
			remaining = append(remaining, json.RawMessage(part.Raw))
			return true
		}
		images = append(images, json.RawMessage(part.Raw))
		return true
	})
	if len(images) == 0 {
		return nil, nil, false
	}
	reference, err := json.Marshal(map[string]string{"type": "input_text", "text": zaiToolImageReference})
	if err != nil {
		return nil, nil, false
	}
	remaining = append(remaining, reference)
	encoded, err := json.Marshal(remaining)
	if err != nil {
		return nil, nil, false
	}
	if wasString {
		encoded, err = json.Marshal(string(encoded))
		if err != nil {
			return nil, nil, false
		}
	}
	return images, encoded, true
}

func zaiToolImageMessage(images []json.RawMessage) ([]byte, error) {
	content := make([]json.RawMessage, 0, len(images)+1)
	marker, err := json.Marshal(map[string]string{"type": "input_text", "text": zaiToolImageMarker})
	if err != nil {
		return nil, err
	}
	content = append(content, marker)
	content = append(content, images...)
	return json.Marshal(map[string]any{"type": "message", "role": "user", "content": content})
}
