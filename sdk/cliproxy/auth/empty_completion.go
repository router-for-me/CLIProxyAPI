package auth

import (
	"bytes"
	"encoding/json"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// errEmptyCompletion indicates the upstream returned a terminal but empty
// completion (no content, no tool calls, zero completion tokens). It is
// retriable so the conductor marks the auth as failed, cools it down, and
// rotates to the next auth/model.
var errEmptyCompletion = &Error{
	Code:      "empty_completion",
	Message:   "upstream returned an empty completion",
	Retryable: true,
}

// openAIChunk is the minimal OpenAI-style SSE/JSON shape used to detect empty
// completions. Non-OpenAI payloads simply won't unmarshal into it and the
// predicate conservatively returns false.
type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"delta"`
		Message struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		CompletionTokens *int `json:"completion_tokens"`
	} `json:"usage"`
}

// emptyCompletionAccum accumulates the properties relevant to deciding whether
// an OpenAI-style completion is empty.
type emptyCompletionAccum struct {
	recognized       bool
	terminal         bool
	hasContent       bool
	hasToolCalls     bool
	completionTokens int
	sawUsage         bool
}

func (a *emptyCompletionAccum) evalJSON(data []byte) {
	var chunk openAIChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return
	}
	if len(chunk.Choices) > 0 {
		a.recognized = true
	}
	if chunk.Usage != nil {
		a.sawUsage = true
		if chunk.Usage.CompletionTokens != nil {
			a.completionTokens += *chunk.Usage.CompletionTokens
		}
	}
	for _, ch := range chunk.Choices {
		if ch.FinishReason != nil && *ch.FinishReason == "stop" {
			a.terminal = true
		}
		content := ch.Delta.Content + ch.Message.Content + ch.Delta.ReasoningContent + ch.Message.ReasoningContent
		if strings.TrimSpace(content) != "" {
			a.hasContent = true
		}
		if len(ch.Delta.ToolCalls) > 0 || len(ch.Message.ToolCalls) > 0 {
			a.hasToolCalls = true
		}
	}
}

// empty reports whether the accumulated stream is an empty completion.
func (a *emptyCompletionAccum) empty() bool {
	if !a.recognized || !a.terminal {
		return false
	}
	if a.hasContent || a.hasToolCalls {
		return false
	}
	if a.sawUsage && a.completionTokens > 0 {
		return false
	}
	return true
}

// isEmptyCompletion reports whether the buffered SSE stream chunks aggregate to
// an empty completion. It is conservative: non-OpenAI formats return false.
func isEmptyCompletion(chunks []cliproxyexecutor.StreamChunk) bool {
	var buf bytes.Buffer
	for _, c := range chunks {
		buf.Write(c.Payload)
	}
	return isEmptyCompletionPayload(buf.Bytes())
}

// streamBootstrapShouldForward reports whether the stream bootstrap should stop
// buffering and forward the buffered chunks to the client. It returns true when
// the aggregate already contains real output (content or tool calls), or when
// the payload format is not recognized as OpenAI-style (conservative: no empty
// completion detection for non-OpenAI streams). It returns false only while the
// aggregate is a recognized OpenAI-style completion with no real output yet, so
// the bootstrap keeps reading to detect a complete empty completion.
func streamBootstrapShouldForward(chunks []cliproxyexecutor.StreamChunk) bool {
	var acc emptyCompletionAccum
	var buf bytes.Buffer
	for _, c := range chunks {
		buf.Write(c.Payload)
	}
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(data, []byte("[DONE]")) || len(data) == 0 {
			continue
		}
		acc.evalJSON(data)
	}
	return acc.hasContent || acc.hasToolCalls || !acc.recognized
}

// isEmptyCompletionPayload reports whether a payload (aggregated SSE chunks or
// a single non-stream JSON response) represents an empty completion. It is
// conservative: non-OpenAI formats return false.
func isEmptyCompletionPayload(payload []byte) bool {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return false
	}

	var acc emptyCompletionAccum

	// SSE events are prefixed with "data:". If the payload is not SSE, treat it
	// as a single non-stream JSON object.
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		acc.evalJSON(trimmed)
		return acc.empty()
	}

	for _, line := range bytes.Split(payload, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(data, []byte("[DONE]")) {
			acc.recognized = true
			acc.terminal = true
			continue
		}
		if len(data) == 0 {
			continue
		}
		acc.evalJSON(data)
	}

	return acc.empty()
}