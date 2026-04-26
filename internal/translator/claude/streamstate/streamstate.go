package streamstate

import (
	"fmt"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/sjson"
)

type blockState struct {
	Index int
	Open  bool
}

type toolBlockState struct {
	Key           string
	Index         int
	ID            string
	Name          string
	Started       bool
	Stopped       bool
	InputFlushed  bool
	PendingInputs strings.Builder
}

type Lifecycle struct {
	nextIndex int
	text      *blockState
	thinking  *blockState
	tools     map[string]*toolBlockState
	toolOrder []string
}

func NewLifecycle() *Lifecycle {
	return &Lifecycle{
		text:     &blockState{Index: -1},
		thinking: &blockState{Index: -1},
		tools:    make(map[string]*toolBlockState),
	}
}

func (l *Lifecycle) AppendThinking(text string) [][]byte {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	var out [][]byte
	out = append(out, l.closeTextBlock()...)
	out = append(out, l.CloseAllToolBlocks()...)
	if !l.thinking.Open {
		if l.thinking.Index < 0 {
			l.thinking.Index = l.allocateIndex()
		}
		out = append(out, buildThinkingStart(l.thinking.Index))
		l.thinking.Open = true
	}
	out = append(out, buildThinkingDelta(l.thinking.Index, text))
	return out
}

func (l *Lifecycle) AppendThinkingSignature(signature string) [][]byte {
	if strings.TrimSpace(signature) == "" || !l.thinking.Open {
		return nil
	}
	payload := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}`)
	payload, _ = sjson.SetBytes(payload, "index", l.thinking.Index)
	payload, _ = sjson.SetBytes(payload, "delta.signature", signature)
	return [][]byte{translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", payload, 2)}
}

func (l *Lifecycle) AppendText(text string) [][]byte {
	if text == "" {
		return nil
	}

	var out [][]byte
	out = append(out, l.closeThinkingBlock()...)
	out = append(out, l.CloseAllToolBlocks()...)
	if !l.text.Open {
		if l.text.Index < 0 {
			l.text.Index = l.allocateIndex()
		}
		out = append(out, buildTextStart(l.text.Index))
		l.text.Open = true
	}
	out = append(out, buildTextDelta(l.text.Index, text))
	return out
}

func (l *Lifecycle) EnsureToolUse(key, toolID, toolName string) [][]byte {
	if strings.TrimSpace(key) == "" {
		return nil
	}

	var out [][]byte
	out = append(out, l.closeThinkingBlock()...)
	out = append(out, l.closeTextBlock()...)

	tool := l.getOrCreateTool(key)
	if strings.TrimSpace(toolID) != "" {
		tool.ID = util.SanitizeClaudeToolID(toolID)
	}
	if strings.TrimSpace(toolName) != "" {
		tool.Name = toolName
	}
	if tool.Started || tool.Stopped || strings.TrimSpace(tool.Name) == "" {
		return out
	}
	if strings.TrimSpace(tool.ID) == "" {
		tool.ID = util.SanitizeClaudeToolID(fmt.Sprintf("tool_%s", key))
	}

	out = append(out, buildToolStart(tool.Index, tool.ID, tool.Name))
	tool.Started = true
	out = append(out, l.flushPendingToolInput(tool)...)
	return out
}

func (l *Lifecycle) AppendToolInput(key, partialJSON string) [][]byte {
	if strings.TrimSpace(key) == "" || partialJSON == "" {
		return nil
	}
	tool := l.getOrCreateTool(key)
	if tool.Stopped {
		return nil
	}
	if tool.Started {
		return [][]byte{buildToolDelta(tool.Index, partialJSON)}
	}
	tool.PendingInputs.WriteString(partialJSON)
	return nil
}

func (l *Lifecycle) CloseAllToolBlocks() [][]byte {
	if len(l.toolOrder) == 0 {
		return nil
	}

	var out [][]byte
	for _, key := range l.toolOrder {
		tool := l.tools[key]
		if tool == nil || tool.Stopped {
			continue
		}
		if !tool.Started {
			delete(l.tools, key)
			continue
		}
		out = append(out, l.flushPendingToolInput(tool)...)
		out = append(out, buildToolStop(tool.Index))
		tool.Stopped = true
	}
	return out
}

func (l *Lifecycle) CloseThinking() [][]byte {
	return l.closeThinkingBlock()
}

func (l *Lifecycle) CloseText() [][]byte {
	return l.closeTextBlock()
}

func (l *Lifecycle) CloseToolUse(key string) [][]byte {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	tool := l.tools[key]
	if tool == nil || tool.Stopped {
		return nil
	}
	if !tool.Started {
		delete(l.tools, key)
		return nil
	}

	out := l.flushPendingToolInput(tool)
	out = append(out, buildToolStop(tool.Index))
	tool.Stopped = true
	return out
}

func (l *Lifecycle) CloseAll() [][]byte {
	var out [][]byte
	out = append(out, l.CloseThinking()...)
	out = append(out, l.CloseText()...)
	out = append(out, l.CloseAllToolBlocks()...)
	return out
}

func (l *Lifecycle) HasStartedToolUse() bool {
	for _, key := range l.toolOrder {
		tool := l.tools[key]
		if tool != nil && tool.Started && !tool.Stopped {
			return true
		}
	}
	return false
}

func (l *Lifecycle) closeThinkingBlock() [][]byte {
	if !l.thinking.Open {
		return nil
	}
	out := [][]byte{buildContentStop(l.thinking.Index)}
	l.thinking.Open = false
	l.thinking.Index = -1
	return out
}

func (l *Lifecycle) closeTextBlock() [][]byte {
	if !l.text.Open {
		return nil
	}
	out := [][]byte{buildContentStop(l.text.Index)}
	l.text.Open = false
	l.text.Index = -1
	return out
}

func (l *Lifecycle) flushPendingToolInput(tool *toolBlockState) [][]byte {
	if tool == nil || !tool.Started || tool.InputFlushed || tool.PendingInputs.Len() == 0 {
		return nil
	}
	tool.InputFlushed = true
	return [][]byte{buildToolDelta(tool.Index, tool.PendingInputs.String())}
}

func (l *Lifecycle) getOrCreateTool(key string) *toolBlockState {
	if tool := l.tools[key]; tool != nil {
		return tool
	}
	tool := &toolBlockState{
		Key:   key,
		Index: l.allocateIndex(),
	}
	l.tools[key] = tool
	l.toolOrder = append(l.toolOrder, key)
	return tool
}

func (l *Lifecycle) allocateIndex() int {
	index := l.nextIndex
	l.nextIndex++
	return index
}

func buildThinkingStart(index int) []byte {
	payload := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	return translatorcommon.AppendSSEEventBytes(nil, "content_block_start", payload, 2)
}

func buildThinkingDelta(index int, text string) []byte {
	payload := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	payload, _ = sjson.SetBytes(payload, "delta.thinking", text)
	return translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", payload, 2)
}

func buildTextStart(index int) []byte {
	payload := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	return translatorcommon.AppendSSEEventBytes(nil, "content_block_start", payload, 2)
}

func buildTextDelta(index int, text string) []byte {
	payload := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	payload, _ = sjson.SetBytes(payload, "delta.text", text)
	return translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", payload, 2)
}

func buildToolStart(index int, toolID, toolName string) []byte {
	payload := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	payload, _ = sjson.SetBytes(payload, "content_block.id", toolID)
	payload, _ = sjson.SetBytes(payload, "content_block.name", toolName)
	return translatorcommon.AppendSSEEventBytes(nil, "content_block_start", payload, 2)
}

func buildToolDelta(index int, partialJSON string) []byte {
	payload := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	payload, _ = sjson.SetBytes(payload, "delta.partial_json", partialJSON)
	return translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", payload, 2)
}

func buildToolStop(index int) []byte {
	return buildContentStop(index)
}

func buildContentStop(index int) []byte {
	payload := []byte(`{"type":"content_block_stop","index":0}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	return translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", payload, 2)
}
