package executor

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type toolCallAggregate struct {
	ID        string
	Type      string
	FuncName  string
	Arguments string
}

// aggregateOpenAIChatCompletionSSE converts OpenAI-style chat.completion.chunk SSE into
// a final chat.completion JSON payload and returns parsed usage details.
func aggregateOpenAIChatCompletionSSE(body []byte) ([]byte, usage.Detail, error) {
	lines := bytes.Split(body, []byte("\n"))
	var (
		id               string
		model            string
		created          int64
		content          strings.Builder
		reasoning        strings.Builder
		finishReason     string
		nativeFinish     string
		usageRaw         string
		hasAny           bool
		toolCallsByIndex = map[int]*toolCallAggregate{}
		orderedToolIdx   []int
	)

	for _, line := range lines {
		payload := jsonPayload(line)
		if len(payload) == 0 || !gjson.ValidBytes(payload) {
			continue
		}
		hasAny = true

		if id == "" {
			if v := gjson.GetBytes(payload, "id"); v.Exists() {
				id = v.String()
			}
		}
		if model == "" {
			if v := gjson.GetBytes(payload, "model"); v.Exists() {
				model = v.String()
			}
		}
		if created == 0 {
			if v := gjson.GetBytes(payload, "created"); v.Exists() {
				created = v.Int()
			}
		}

		if v := gjson.GetBytes(payload, "choices.0.delta.content"); v.Exists() {
			content.WriteString(v.String())
		}
		if v := gjson.GetBytes(payload, "choices.0.delta.reasoning_content"); v.Exists() {
			reasoning.WriteString(v.String())
		}

		if v := gjson.GetBytes(payload, "choices.0.finish_reason"); v.Exists() {
			trimmed := strings.TrimSpace(v.String())
			if trimmed != "" {
				finishReason = trimmed
			}
		}
		if v := gjson.GetBytes(payload, "choices.0.native_finish_reason"); v.Exists() {
			trimmed := strings.TrimSpace(v.String())
			if trimmed != "" {
				nativeFinish = trimmed
			}
		}

		if v := gjson.GetBytes(payload, "usage"); v.Exists() {
			usageRaw = v.Raw
		}

		if v := gjson.GetBytes(payload, "choices.0.delta.tool_calls"); v.Exists() {
			v.ForEach(func(_, item gjson.Result) bool {
				idx := int(item.Get("index").Int())
				agg, ok := toolCallsByIndex[idx]
				if !ok {
					agg = &toolCallAggregate{}
					toolCallsByIndex[idx] = agg
					orderedToolIdx = append(orderedToolIdx, idx)
				}
				if idv := item.Get("id"); idv.Exists() {
					agg.ID = idv.String()
				}
				if tv := item.Get("type"); tv.Exists() {
					agg.Type = tv.String()
				}
				if nv := item.Get("function.name"); nv.Exists() {
					agg.FuncName = nv.String()
				}
				if av := item.Get("function.arguments"); av.Exists() {
					agg.Arguments += av.String()
				}
				return true
			})
		}
	}

	if !hasAny {
		return nil, usage.Detail{}, fmt.Errorf("openai compat: no SSE payloads to aggregate")
	}

	if finishReason == "" {
		if len(toolCallsByIndex) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}

	result := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`)
	if id != "" {
		result, _ = sjson.SetBytes(result, "id", id)
	}
	if created != 0 {
		result, _ = sjson.SetBytes(result, "created", created)
	}
	if model != "" {
		result, _ = sjson.SetBytes(result, "model", model)
	}
	if content.Len() > 0 {
		result, _ = sjson.SetBytes(result, "choices.0.message.content", content.String())
	}
	if reasoning.Len() > 0 {
		result, _ = sjson.SetBytes(result, "choices.0.message.reasoning_content", reasoning.String())
	}
	if finishReason != "" {
		result, _ = sjson.SetBytes(result, "choices.0.finish_reason", finishReason)
	}
	if nativeFinish != "" {
		result, _ = sjson.SetBytes(result, "choices.0.native_finish_reason", nativeFinish)
	}

	if len(toolCallsByIndex) > 0 {
		sort.Ints(orderedToolIdx)
		toolCalls := make([]map[string]any, 0, len(orderedToolIdx))
		for _, idx := range orderedToolIdx {
			agg := toolCallsByIndex[idx]
			if agg == nil {
				continue
			}
			entry := map[string]any{
				"id":   agg.ID,
				"type": agg.Type,
				"function": map[string]any{
					"name":      agg.FuncName,
					"arguments": agg.Arguments,
				},
			}
			toolCalls = append(toolCalls, entry)
		}
		if len(toolCalls) > 0 {
			result, _ = sjson.SetBytes(result, "choices.0.message.tool_calls", toolCalls)
		}
	}

	if strings.TrimSpace(usageRaw) != "" && gjson.Valid(usageRaw) {
		result, _ = sjson.SetRawBytes(result, "usage", []byte(usageRaw))
	}

	usageDetail := parseOpenAIUsage(result)
	return result, usageDetail, nil
}
