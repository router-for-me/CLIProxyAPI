package helps

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PluginUsageObservation retains the latest OpenAI-compatible usage snapshot
// before response translation can drop cache writes or synthesize zero details.
// It belongs to one request and is used by its translation goroutine only.
type PluginUsageObservation struct {
	detail  usage.Detail
	seen    bool
	tier    string
	pending []byte
}

func NewPluginUsageObservation(protocol string) *PluginUsageObservation {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "", "openai", "chat-completions", "chat_completions", "openai-response", "codex":
		return &PluginUsageObservation{}
	default:
		return nil
	}
}

func (o *PluginUsageObservation) Observe(payload []byte) {
	if o == nil || len(payload) == 0 {
		return
	}
	if len(o.pending) == 0 && gjson.ValidBytes(bytes.TrimSpace(payload)) {
		o.observeJSON(bytes.TrimSpace(payload))
		return
	}
	o.pending = append(o.pending, payload...)
	for {
		index := bytes.IndexByte(o.pending, '\n')
		if index < 0 {
			break
		}
		line := o.pending[:index]
		o.observeJSON(ExtractStreamJSONPayload(line))
		o.pending = o.pending[index+1:]
	}
	if raw := ExtractStreamJSONPayload(o.pending); len(raw) > 0 && gjson.ValidBytes(raw) {
		o.observeJSON(raw)
		o.pending = nil
	}
}

func pluginCounterPresent(node gjson.Result, paths ...string) bool {
	for _, path := range paths {
		value := node.Get(path)
		if !value.Exists() {
			continue
		}
		if value.Type != gjson.Number && value.Type != gjson.String {
			return false
		}
		n, err := strconv.ParseInt(value.String(), 10, 64)
		return err == nil && n >= 0 && n <= 1<<53-1
	}
	return false
}

func (o *PluginUsageObservation) observeJSON(raw []byte) {
	if len(raw) == 0 || !gjson.ValidBytes(raw) {
		return
	}
	if tier := extractResponseServiceTier(raw); tier != "" {
		o.tier = tier
	}
	node := gjson.GetBytes(raw, "response.usage")
	if !node.IsObject() {
		node = gjson.GetBytes(raw, "usage")
	}
	if !node.IsObject() {
		return
	}
	observed := usage.TokenUsagePresence{
		InputTokens:      pluginCounterPresent(node, "prompt_tokens", "input_tokens"),
		OutputTokens:     pluginCounterPresent(node, "completion_tokens", "output_tokens"),
		CacheReadTokens:  pluginCounterPresent(node, "prompt_tokens_details.cached_tokens", "input_tokens_details.cached_tokens"),
		CacheWriteTokens: pluginCounterPresent(node, "input_tokens_details.cache_creation_tokens", "input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_write_tokens"),
		ReasoningTokens:  pluginCounterPresent(node, "completion_tokens_details.reasoning_tokens", "output_tokens_details.reasoning_tokens", "reasoning_tokens"),
		TotalTokens:      pluginCounterPresent(node, "total_tokens"),
	}
	if !observed.InputTokens && !observed.OutputTokens && !observed.CacheReadTokens && !observed.CacheWriteTokens && !observed.ReasoningTokens && !observed.TotalTokens {
		return
	}
	o.detail = parseOpenAIStyleUsageNode(node)
	o.detail.TokenBreakdown = usage.WithTokenUsagePresence(o.detail.TokenBreakdown, observed)
	o.seen = true
}

func (o *PluginUsageObservation) Detail() usage.Detail {
	if o == nil || !o.seen {
		detail := usage.Detail{TokenBreakdown: usage.NewUnobservedTokenBreakdown()}
		if o != nil {
			detail.ResponseServiceTier = o.tier
		}
		return detail
	}
	detail := o.detail
	detail.ResponseServiceTier = o.tier
	return detail
}

// RestoreResponses repairs only plugin response usage after the normal
// translator/default-detail pass. It never combines snapshots or requests.
func (o *PluginUsageObservation) RestoreResponses(payload []byte) []byte {
	if o == nil || len(payload) == 0 {
		return payload
	}
	if gjson.ValidBytes(bytes.TrimSpace(payload)) {
		return o.restoreJSON(payload)
	}
	lines := bytes.Split(payload, []byte("\n"))
	for index, line := range lines {
		prefix := bytes.Index(line, []byte("data:"))
		if prefix < 0 || len(bytes.TrimSpace(line[:prefix])) != 0 {
			continue
		}
		start := prefix + len("data:")
		if start < len(line) && line[start] == ' ' {
			start++
		}
		raw := bytes.TrimSpace(line[start:])
		if gjson.ValidBytes(raw) {
			lines[index] = append(bytes.Clone(line[:start]), o.restoreJSON(raw)...)
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

func (o *PluginUsageObservation) restoreJSON(payload []byte) []byte {
	root := gjson.ParseBytes(payload)
	terminal := root.Get("type").String() == "response.completed" || root.Get("type").String() == "response.incomplete" || root.Get("type").String() == "response.failed"
	for _, path := range []string{"response.usage", "usage"} {
		node := gjson.GetBytes(payload, path)
		create := terminal && ((path == "response.usage" && root.Get("response").IsObject()) || (path == "usage" && !root.Get("response").IsObject()))
		if path == "usage" && root.Get("object").String() == "response" {
			create = true
		}
		if !node.IsObject() && !create {
			continue
		}
		if !o.seen {
			payload, _ = sjson.DeleteBytes(payload, path)
			continue
		}
		observed := o.detail.TokenBreakdown.Observed
		set := func(key string, present bool, value int64) {
			if present {
				payload, _ = sjson.SetBytes(payload, path+"."+key, value)
			} else {
				payload, _ = sjson.DeleteBytes(payload, path+"."+key)
			}
		}
		set("input_tokens", observed.InputTokens, o.detail.InputTokens)
		set("output_tokens", observed.OutputTokens, o.detail.OutputTokens)
		set("total_tokens", observed.TotalTokens || (observed.InputTokens && observed.OutputTokens), o.detail.TotalTokens)
		set("input_tokens_details.cached_tokens", observed.CacheReadTokens, o.detail.CacheReadTokens)
		set("input_tokens_details.cache_write_tokens", observed.CacheWriteTokens, o.detail.CacheCreationTokens)
		set("output_tokens_details.reasoning_tokens", observed.ReasoningTokens, o.detail.ReasoningTokens)
	}
	return payload
}
