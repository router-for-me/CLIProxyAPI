// Package bedrock provides response translation between AWS Bedrock Converse API
// and Anthropic Claude Messages API format.
package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// non-streaming

// ConvertBedrockResponseToClaude converts a Bedrock Converse non-streaming
// response to Anthropic Messages API format.
func ConvertBedrockResponseToClaude(_ context.Context, modelName string, _, _, rawJSON []byte, _ *any) []byte {
	root := gjson.ParseBytes(rawJSON)

	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "model", modelName)

	// content blocks
	if contentBlocks := root.Get("output.message.content"); contentBlocks.Exists() && contentBlocks.IsArray() {
		contentBlocks.ForEach(func(_, block gjson.Result) bool {
			switch {
			case block.Get("text").Exists():
				textBlock := []byte(`{"type":"text","text":""}`)
				textBlock, _ = sjson.SetBytes(textBlock, "text", block.Get("text").String())
				out, _ = sjson.SetRawBytes(out, "content.-1", textBlock)
			case block.Get("reasoningContent").Exists() || block.Get("reasoning").Exists() || block.Get("type").String() == "thinking":
				var text string
				var signature string
				if block.Get("reasoningContent").Exists() {
					reasoning := block.Get("reasoningContent")
					// Support multiple nested paths for reasoning text
					switch {
					case reasoning.Get("reasoningText.text").Exists():
						text = reasoning.Get("reasoningText.text").String()
					case reasoning.Get("reasoning").Exists():
						text = reasoning.Get("reasoning").String()
					case reasoning.Get("text").Exists():
						text = reasoning.Get("text").String()
					}

					// Support multiple nested paths for signature
					switch {
					case reasoning.Get("reasoningText.signature").Exists():
						signature = reasoning.Get("reasoningText.signature").String()
					case reasoning.Get("signature").Exists():
						signature = reasoning.Get("signature").String()
					}
				} else if block.Get("reasoning").Exists() {
					reasoning := block.Get("reasoning")
					if reasoning.Type == gjson.String {
						text = reasoning.String()
					} else {
						text = reasoning.Get("text").String()
						signature = reasoning.Get("signature").String()
					}
				} else {
					text = block.Get("thinking").String()
					signature = block.Get("signature").String()
				}

				if text != "" {
					thinkBlock := []byte(`{"type":"thinking","thinking":""}`)
					thinkBlock, _ = sjson.SetBytes(thinkBlock, "thinking", text)
					out, _ = sjson.SetRawBytes(out, "content.-1", thinkBlock)
				}
				if signature != "" {
					redactedBlock := []byte(`{"type":"redacted_thinking","data":""}`)
					redactedBlock, _ = sjson.SetBytes(redactedBlock, "data", signature)
					out, _ = sjson.SetRawBytes(out, "content.-1", redactedBlock)
				}
			case block.Get("toolUse").Exists():
				tu := block.Get("toolUse")
				toolBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolBlock, _ = sjson.SetBytes(toolBlock, "id", tu.Get("toolUseId").String())
				toolBlock, _ = sjson.SetBytes(toolBlock, "name", tu.Get("name").String())
				if input := tu.Get("input"); input.Exists() {
					toolBlock, _ = sjson.SetRawBytes(toolBlock, "input", []byte(input.Raw))
				}
				out, _ = sjson.SetRawBytes(out, "content.-1", toolBlock)
			}
			return true
		})
	}

	// stop_reason
	if stopReason := root.Get("stopReason"); stopReason.Exists() {
		out, _ = sjson.SetBytes(out, "stop_reason", mapBedrockStopReasonToClaude(stopReason.String()))
	}

	// usage
	if usage := root.Get("usage"); usage.Exists() {
		out, _ = sjson.SetBytes(out, "usage.input_tokens", usage.Get("inputTokens").Int())
		out, _ = sjson.SetBytes(out, "usage.output_tokens", usage.Get("outputTokens").Int())
	}

	return out
}

// streaming

// BedrockClaudeStreamState holds state across streaming chunks.
type BedrockClaudeStreamState struct {
	MessageID    string
	Model        string
	FinishReason string
	// Block tracking.
	// Keyed by Bedrock contentBlockIndex. We lazily create blocks so the stream
	// remains valid even when Bedrock omits contentBlockStart events.
	Blocks         map[int]*contentBlockState
	NextBlockIdx   int
	MessageStarted bool
	MessageStopped bool
	InputTokens    int64
	OutputTokens   int64
}

type contentBlockState struct {
	claudeIdx   int
	kind        string // text | thinking | tool_use
	started     bool
	toolUseID   string
	name        string
	inputBuffer strings.Builder
}

// ConvertBedrockStreamResponseToClaude translates Bedrock Converse stream events
// to Anthropic SSE format.
func ConvertBedrockStreamResponseToClaude(_ context.Context, modelName string, _, _, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &BedrockClaudeStreamState{
			Blocks: make(map[int]*contentBlockState),
		}
	}
	state := (*param).(*BedrockClaudeStreamState)
	if state.Model == "" {
		state.Model = modelName
	}

	line := bytes.TrimSpace(rawJSON)
	line = bytes.TrimPrefix(line, []byte("data:"))
	line = bytes.TrimSpace(line)

	if len(line) == 0 {
		return nil
	}

	root := gjson.ParseBytes(line)
	eventType := root.Get("type").String()

	var results [][]byte

	switch eventType {
	case "messageStart":
		if state.MessageStarted {
			break
		}
		state.MessageID = root.Get("p").String()
		if state.MessageID == "" {
			state.MessageID = "msg_bedrock_stream"
		}
		msgStartJSON := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
		msgStartJSON, _ = sjson.SetBytes(msgStartJSON, "message.id", state.MessageID)
		msgStartJSON, _ = sjson.SetBytes(msgStartJSON, "message.model", state.Model)
		results = append(results, appendClaudeSSEEvent("message_start", msgStartJSON))
		state.MessageStarted = true

	case "contentBlockStart":
		blockIdx := int(root.Get("contentBlockIndex").Int())
		contentBlock := extractBedrockStartBlock(root)

		if toolUse := contentBlock.Get("toolUse"); toolUse.Exists() {
			startEvents, _ := ensureContentBlockStarted(state, blockIdx, "tool_use", toolUse.Get("toolUseId").String(), toolUse.Get("name").String())
			results = append(results, startEvents...)
		} else if contentBlock.Get("reasoningContent").Exists() || contentBlock.Get("reasoning").Exists() || contentBlock.Get("type").String() == "thinking" {
			startEvents, _ := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
		} else if contentBlock.Get("text").Exists() || contentBlock.Type == gjson.Null || !contentBlock.Exists() {
			startEvents, _ := ensureContentBlockStarted(state, blockIdx, "text", "", "")
			results = append(results, startEvents...)
		}

	case "contentBlockDelta":
		blockIdx := int(root.Get("contentBlockIndex").Int())
		delta := root.Get("delta")

		if text := delta.Get("text"); text.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "text", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.text", text.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rText := delta.Get("reasoningContent.reasoningText.text"); rText.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.thinking", rText.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rText := delta.Get("reasoningContent.reasoning"); rText.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.thinking", rText.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rText := delta.Get("reasoningContent.text"); rText.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.thinking", rText.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rText := delta.Get("reasoning.text"); rText.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.thinking", rText.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rText := delta.Get("reasoning"); rText.Type == gjson.String && rText.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.thinking", rText.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rText := delta.Get("thinking"); delta.Get("type").String() == "thinking_delta" && rText.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.thinking", rText.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rSignature := delta.Get("reasoningContent.reasoningText.signature"); rSignature.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.signature", rSignature.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rSignature := delta.Get("reasoningContent.signature"); rSignature.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.signature", rSignature.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rSignature := delta.Get("reasoning.signature"); rSignature.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.signature", rSignature.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if rSignature := delta.Get("signature"); delta.Get("type").String() == "signature_delta" && rSignature.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "thinking", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.signature", rSignature.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		} else if toolInput := delta.Get("toolUse.input"); toolInput.Exists() {
			startEvents, cb := ensureContentBlockStarted(state, blockIdx, "tool_use", "", "")
			results = append(results, startEvents...)
			cbDeltaJSON := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "index", cb.claudeIdx)
			cbDeltaJSON, _ = sjson.SetBytes(cbDeltaJSON, "delta.partial_json", toolInput.String())
			results = append(results, appendClaudeSSEEvent("content_block_delta", cbDeltaJSON))
		}

	case "contentBlockStop":
		blockIdx := int(root.Get("contentBlockIndex").Int())
		if cb, ok := state.Blocks[blockIdx]; ok && cb.started {
			cbStopJSON := []byte(`{"type":"content_block_stop","index":0}`)
			cbStopJSON, _ = sjson.SetBytes(cbStopJSON, "index", cb.claudeIdx)
			results = append(results, appendClaudeSSEEvent("content_block_stop", cbStopJSON))
			cb.started = false
		}

	case "messageStop":
		// Some Bedrock model streams omit contentBlockStop; close any started blocks
		// here to keep Anthropic SSE block lifecycle valid.
		results = append(results, closeStartedBlocks(state)...)

		stopReason := root.Get("stopReason").String()
		state.FinishReason = mapBedrockStopReasonToClaude(stopReason)
		// message_delta
		msgDeltaJSON := []byte(`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{"output_tokens":0}}`)
		msgDeltaJSON, _ = sjson.SetBytes(msgDeltaJSON, "delta.stop_reason", state.FinishReason)
		msgDeltaJSON, _ = sjson.SetBytes(msgDeltaJSON, "usage.output_tokens", state.OutputTokens)
		results = append(results, appendClaudeSSEEvent("message_delta", msgDeltaJSON))
		// message_stop
		results = append(results, appendClaudeSSEEvent("message_stop", []byte(`{"type":"message_stop"}`)))
		state.MessageStopped = true

	case "metadata":
		// Update token counts; will be used in messageStop
		if usage := root.Get("usage"); usage.Exists() {
			state.InputTokens = usage.Get("inputTokens").Int()
			state.OutputTokens = usage.Get("outputTokens").Int()
		}
	}

	return results
}

func appendClaudeSSEEvent(event string, payload []byte) []byte {
	return translatorcommon.AppendSSEEventBytes(nil, event, payload, 2)
}

func extractBedrockStartBlock(root gjson.Result) gjson.Result {
	// Some Bedrock Converse-stream payloads expose tool starts under:
	// {"type":"contentBlockStart","contentBlockIndex":N,"start":{"toolUse":...}}
	// while synthetic/local payloads may use {"contentBlock":{...}}.
	if block := root.Get("contentBlock"); block.Exists() {
		return block
	}
	if block := root.Get("start"); block.Exists() {
		return block
	}
	if block := root.Get("contentBlockStart.start"); block.Exists() {
		return block
	}
	return gjson.Result{}
}

func ensureContentBlockStarted(state *BedrockClaudeStreamState, blockIdx int, kind, toolUseID, toolName string) ([][]byte, *contentBlockState) {
	cb, ok := state.Blocks[blockIdx]
	if !ok || cb == nil {
		cb = &contentBlockState{
			claudeIdx: state.NextBlockIdx,
			kind:      kind,
		}
		state.Blocks[blockIdx] = cb
		state.NextBlockIdx++
	}

	// If the upstream reuses a block index for a different type, rotate to a new
	// Claude block index so we still keep a valid, monotonic block sequence.
	var events [][]byte
	if cb.kind != kind {
		if cb.started {
			cbStopJSON := []byte(`{"type":"content_block_stop","index":0}`)
			cbStopJSON, _ = sjson.SetBytes(cbStopJSON, "index", cb.claudeIdx)
			events = append(events, appendClaudeSSEEvent("content_block_stop", cbStopJSON))
		}
		cb = &contentBlockState{
			claudeIdx: state.NextBlockIdx,
			kind:      kind,
		}
		state.Blocks[blockIdx] = cb
		state.NextBlockIdx++
	}

	if toolUseID != "" {
		cb.toolUseID = toolUseID
	}
	if toolName != "" {
		cb.name = toolName
	}

	if cb.started {
		return events, cb
	}

	switch kind {
	case "thinking":
		cbStartJSON := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
		cbStartJSON, _ = sjson.SetBytes(cbStartJSON, "index", cb.claudeIdx)
		events = append(events, appendClaudeSSEEvent("content_block_start", cbStartJSON))
	case "tool_use":
		toolID := strings.TrimSpace(cb.toolUseID)
		if toolID == "" {
			toolID = fallbackToolUseID(blockIdx)
			cb.toolUseID = toolID
		}
		toolNameFinal := strings.TrimSpace(cb.name)
		if toolNameFinal == "" {
			toolNameFinal = "bedrock_tool"
			cb.name = toolNameFinal
		}
		cbStartJSON := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`)
		cbStartJSON, _ = sjson.SetBytes(cbStartJSON, "index", cb.claudeIdx)
		cbStartJSON, _ = sjson.SetBytes(cbStartJSON, "content_block.id", toolID)
		cbStartJSON, _ = sjson.SetBytes(cbStartJSON, "content_block.name", toolNameFinal)
		events = append(events, appendClaudeSSEEvent("content_block_start", cbStartJSON))
	default:
		cbStartJSON := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		cbStartJSON, _ = sjson.SetBytes(cbStartJSON, "index", cb.claudeIdx)
		events = append(events, appendClaudeSSEEvent("content_block_start", cbStartJSON))
	}

	cb.started = true
	return events, cb
}

func closeStartedBlocks(state *BedrockClaudeStreamState) [][]byte {
	type kv struct {
		bedrockIdx int
		claudeIdx  int
	}
	ordered := make([]kv, 0, len(state.Blocks))
	for bedrockIdx, cb := range state.Blocks {
		if cb == nil || !cb.started {
			continue
		}
		ordered = append(ordered, kv{bedrockIdx: bedrockIdx, claudeIdx: cb.claudeIdx})
	}

	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].claudeIdx < ordered[j].claudeIdx
	})

	out := make([][]byte, 0, len(ordered))
	for _, item := range ordered {
		cb := state.Blocks[item.bedrockIdx]
		cbStopJSON := []byte(`{"type":"content_block_stop","index":0}`)
		cbStopJSON, _ = sjson.SetBytes(cbStopJSON, "index", cb.claudeIdx)
		out = append(out, appendClaudeSSEEvent("content_block_stop", cbStopJSON))
		cb.started = false
	}
	return out
}

func fallbackToolUseID(blockIdx int) string {
	if blockIdx < 0 {
		return "toolu_bedrock_unknown"
	}
	return fmt.Sprintf("toolu_bedrock_%d", blockIdx)
}

func mapBedrockStopReasonToClaude(reason string) string {
	switch reason {
	case "end_turn":
		return "end_turn"
	case "max_tokens":
		return "max_tokens"
	case "tool_use":
		return "tool_use"
	case "stop_sequence":
		return "end_turn"
	default:
		return "end_turn"
	}
}
