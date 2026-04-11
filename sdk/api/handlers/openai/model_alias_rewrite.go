package openai

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func rewriteModelAliasInChunk(chunk []byte, requestedModel string) []byte {
	requestedModel = strings.TrimSpace(requestedModel)
	if len(chunk) == 0 || requestedModel == "" {
		return chunk
	}

	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return chunk
	}

	if trimmed[0] == '{' || trimmed[0] == '[' {
		return rewriteModelAliasInJSON(chunk, requestedModel)
	}

	if bytes.Contains(chunk, []byte("data:")) {
		return rewriteModelAliasInSSEChunk(chunk, requestedModel)
	}

	return chunk
}

func rewriteModelAliasInSSEChunk(chunk []byte, requestedModel string) []byte {
	lines := strings.Split(string(chunk), "\n")
	changed := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		rewritten := rewriteModelAliasInJSON([]byte(payload), requestedModel)
		if bytes.Equal(rewritten, []byte(payload)) {
			continue
		}
		lines[i] = "data: " + string(rewritten)
		changed = true
	}
	if !changed {
		return chunk
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteModelAliasInJSON(payload []byte, requestedModel string) []byte {
	rewritten := payload
	changed := false

	if model := gjson.GetBytes(rewritten, "model"); model.Exists() && model.String() != requestedModel {
		if next, err := sjson.SetBytes(rewritten, "model", requestedModel); err == nil {
			rewritten = next
			changed = true
		}
	}
	if model := gjson.GetBytes(rewritten, "response.model"); model.Exists() && model.String() != requestedModel {
		if next, err := sjson.SetBytes(rewritten, "response.model", requestedModel); err == nil {
			rewritten = next
			changed = true
		}
	}

	if !changed {
		return payload
	}
	return rewritten
}
