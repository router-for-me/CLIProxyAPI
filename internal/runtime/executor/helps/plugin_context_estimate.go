package helps

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

const maxClientContextPromptBytes = 16 * 1024 * 1024
const maxClientContextOutputBytes = 4 * 1024 * 1024

// pluginContextEstimate is a client compatibility aid, not provider accounting.
// It lives only for one request; no callbacks or upstream scope are retained.
type pluginContextEstimate struct {
	prompt  []byte
	input   int64
	counted bool
	valid   bool
}

// ConfigureClientContextEstimate supplies the final normalized Chat prompt for
// an eligible Cursor Next -> Responses request. No estimate is made unless the
// plugin explicitly opts in on its terminal frame with a valid Run reference.
func (o *PluginUsageObservation) ConfigureClientContextEstimate(prompt []byte) {
	if o == nil || len(prompt) == 0 || len(prompt) > maxClientContextPromptBytes || !gjson.ValidBytes(prompt) || !gjson.GetBytes(prompt, "messages").IsArray() {
		return
	}
	o.contextEstimate = &pluginContextEstimate{prompt: prompt}
}

func (e *pluginContextEstimate) apply(payload []byte, observed *PluginUsageObservation) []byte {
	if e == nil || observed == nil || observed.seen || observed.accounting == nil || !observed.clientContextEstimate {
		return payload
	}
	root := gjson.ParseBytes(payload)
	node, terminal := pluginAccountingTerminal(root)
	if !terminal || len(node.Raw) > maxClientContextOutputBytes || !node.Get("output").IsArray() {
		return payload
	}
	path := ""
	if root.Get("object").String() == "response" {
		path = "usage"
	} else if root.Get("response").IsObject() && strings.HasPrefix(root.Get("type").String(), "response.") {
		path = "response.usage"
	}
	if path == "" {
		return payload
	}
	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		return payload
	}
	if !e.counted {
		e.counted = true
		e.input, err = countClientContextPrompt(codec, e.prompt)
		e.prompt = nil
		e.valid = err == nil
	}
	if !e.valid {
		return payload
	}
	output, ok := clientContextOutputText(node.Get("output"))
	if !ok {
		return payload
	}
	outputTokens, err := codec.Count(output)
	if err != nil {
		return payload
	}
	// The caller's source observation and usage recorder have already consumed
	// the original payload. This final client-only patch is never fed back into
	// either observer, so estimated context cannot become a billed measurement.
	payload, _ = sjson.SetBytes(payload, path, map[string]any{
		"input_tokens":  e.input,
		"output_tokens": outputTokens,
		"total_tokens":  e.input + int64(outputTokens),
	})
	contextPath := strings.TrimSuffix(path, "usage") + "context_usage"
	payload, _ = sjson.SetBytes(payload, contextPath, map[string]any{
		"estimated": true, "source": "local_tokenizer", "encoding": codec.GetName(), "scope": "request_context",
	})
	return payload
}

func countClientContextPrompt(codec tokenizer.Codec, prompt []byte) (int64, error) {
	// Count complete tool JSON rather than the older helper's selected fields,
	// preserving custom grammar, namespaces, strictness and executable schemas.
	tools := gjson.GetBytes(prompt, "tools")
	withoutTools, _ := sjson.DeleteBytes(prompt, "tools")
	count, err := CountOpenAIChatTokens(codec, withoutTools)
	if err != nil || !tools.Exists() {
		return count, err
	}
	toolTokens, err := codec.Count(tools.Raw)
	return count + int64(toolTokens), err
}

func clientContextOutputText(items gjson.Result) (string, bool) {
	var output strings.Builder
	appendText := func(text string) bool {
		if len(text) > maxClientContextOutputBytes-output.Len() {
			return false
		}
		output.WriteString(text)
		return true
	}
	for _, item := range items.Array() {
		switch item.Get("type").String() {
		case "message":
			for _, part := range item.Get("content").Array() {
				if !appendText(part.Get("text").String()) {
					return "", false
				}
			}
		case "reasoning":
			parts := item.Get("summary").Array()
			if len(parts) == 0 {
				parts = item.Get("content").Array()
			}
			for _, part := range parts {
				if !appendText(part.Get("text").String()) {
					return "", false
				}
			}
		case "function_call":
			if !appendText(item.Get("arguments").String()) {
				return "", false
			}
		case "custom_tool_call":
			if !appendText(item.Get("input").String()) {
				return "", false
			}
		}
	}
	return output.String(), true
}
