package auth

import (
	"bytes"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var modelFieldPaths = []string{"model", "modelVersion", "response.model", "response.modelVersion", "message.model"}

const maxPendingBufSize = 1 << 20 // 1MB limit for pending buffer

func rewriteModelInResponse(data []byte, targetModel string) []byte {
	if targetModel == "" || len(data) == 0 {
		return data
	}
	for _, path := range modelFieldPaths {
		if gjson.GetBytes(data, path).Exists() {
			data, _ = sjson.SetBytes(data, path, targetModel)
			log.Debugf("response rewriter: rewrote model at path %s to %s", path, targetModel)
		}
	}
	return data
}

type StreamRewriteOptions struct {
	RewriteModel  string
	StripThinking bool
}

type StreamRewriter struct {
	options              StreamRewriteOptions
	thinkingBlockIndexes map[int]bool
	pendingBuf           []byte
}

func NewStreamRewriter(options StreamRewriteOptions) *StreamRewriter {
	return &StreamRewriter{
		options:              options,
		thinkingBlockIndexes: make(map[int]bool),
		pendingBuf:           nil,
	}
}

func (r *StreamRewriter) RewriteChunk(chunk []byte) []byte {
	if r.options.RewriteModel == "" && !r.options.StripThinking {
		return chunk
	}

	if len(r.pendingBuf) > 0 {
		chunk = append(r.pendingBuf, chunk...)
		r.pendingBuf = nil
	}

	if len(chunk) > maxPendingBufSize {
		return chunk
	}

	lastDoubleNewline := bytes.LastIndex(chunk, []byte("\n\n"))
	lastNewline := -1
	if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
		lastNewline = len(chunk) - 1
	}

	var processChunk []byte
	if lastDoubleNewline >= 0 {
		afterComplete := chunk[lastDoubleNewline+2:]
		if len(afterComplete) > 0 && !bytes.Equal(afterComplete, []byte("\n")) {
			processChunk = chunk[:lastDoubleNewline+2]
			r.pendingBuf = make([]byte, len(afterComplete))
			copy(r.pendingBuf, afterComplete)
		} else {
			processChunk = chunk
		}
	} else if lastNewline >= 0 && gjson.ValidBytes(extractLastDataPayload(chunk)) {
		processChunk = chunk
	} else if len(chunk) > 0 {
		r.pendingBuf = make([]byte, len(chunk))
		copy(r.pendingBuf, chunk)
		return nil
	} else {
		return chunk
	}

	lines := bytes.Split(processChunk, []byte("\n"))
	var result [][]byte
	var pendingEvent []byte
	skipBlanks := false

	for _, line := range lines {
		if len(line) == 0 && skipBlanks {
			continue
		}
		if len(line) != 0 && skipBlanks {
			skipBlanks = false
		}

		if bytes.HasPrefix(line, []byte("event:")) {
			pendingEvent = line
			continue
		}

		if jsonData, found := bytes.CutPrefix(line, []byte("data: ")); found && len(jsonData) > 0 && jsonData[0] == '{' {
			if !gjson.ValidBytes(jsonData) {
				if pendingEvent != nil {
					r.pendingBuf = append(pendingEvent, '\n')
					r.pendingBuf = append(r.pendingBuf, line...)
					pendingEvent = nil
				} else {
					r.pendingBuf = append(r.pendingBuf, line...)
				}
				continue
			}

			if r.options.StripThinking && r.isThinkingEvent(jsonData) {
				pendingEvent = nil
				skipBlanks = true
				continue
			}

			if pendingEvent != nil {
				result = append(result, pendingEvent)
				pendingEvent = nil
			}

			rewritten := jsonData
			if r.options.RewriteModel != "" {
				rewritten = rewriteModelInResponse(jsonData, r.options.RewriteModel)
			}
			result = append(result, append([]byte("data: "), rewritten...))
			continue
		}

		if pendingEvent != nil {
			result = append(result, pendingEvent)
			pendingEvent = nil
		}
		result = append(result, line)
	}

	if pendingEvent != nil {
		result = append(result, pendingEvent)
	}

	return bytes.Join(result, []byte("\n"))
}

func extractLastDataPayload(chunk []byte) []byte {
	lines := bytes.Split(chunk, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if jsonData, found := bytes.CutPrefix(lines[i], []byte("data: ")); found && len(jsonData) > 0 {
			return jsonData
		}
	}
	return nil
}

func (r *StreamRewriter) isThinkingEvent(data []byte) bool {
	eventType := gjson.GetBytes(data, "type").String()

	switch eventType {
	case "content_block_start":
		blockType := gjson.GetBytes(data, "content_block.type").String()
		if blockType == "thinking" {
			index := int(gjson.GetBytes(data, "index").Int())
			r.thinkingBlockIndexes[index] = true
			log.Debugf("response rewriter: stripping thinking block start at index %d", index)
			return true
		}
		return false

	case "content_block_delta":
		index := int(gjson.GetBytes(data, "index").Int())
		if r.thinkingBlockIndexes[index] {
			return true
		}
		deltaType := gjson.GetBytes(data, "delta.type").String()
		return deltaType == "thinking_delta"

	case "content_block_stop":
		index := int(gjson.GetBytes(data, "index").Int())
		if r.thinkingBlockIndexes[index] {
			delete(r.thinkingBlockIndexes, index)
			log.Debugf("response rewriter: stripping thinking block stop at index %d", index)
			return true
		}
		return false
	}

	return false
}

func stripThinkingBlocksFromResponse(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	contentResult := gjson.GetBytes(data, "content")
	if !contentResult.Exists() || !contentResult.IsArray() {
		return data
	}

	var filteredContent []any
	var strippedCount int
	contentResult.ForEach(func(_, value gjson.Result) bool {
		blockType := value.Get("type").String()
		if blockType != "thinking" {
			filteredContent = append(filteredContent, value.Value())
		} else {
			strippedCount++
		}
		return true
	})

	if strippedCount > 0 {
		log.Debugf("response rewriter: stripped %d thinking blocks from non-streaming response", strippedCount)
	}

	result, err := sjson.SetBytes(data, "content", filteredContent)
	if err != nil {
		return data
	}
	return result
}
