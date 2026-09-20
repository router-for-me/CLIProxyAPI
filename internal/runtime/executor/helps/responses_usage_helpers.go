package helps

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// EnsureResponsesUsageDetails ensures that Responses usage objects contain output_tokens_details
// (defaulting reasoning_tokens to 0) and input_tokens_details (defaulting cached_tokens to 0).
// It supports plain JSON payloads, single-line SSE data: lines, and multi-line SSE frames (e.g. event: ...\ndata: ...).
func EnsureResponsesUsageDetails(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return payload
	}

	// 1. JSON-first: If trimmed payload starts with '{', process as a plain JSON object.
	if trimmed[0] == '{' {
		if gjson.GetBytes(trimmed, "object").String() == "response.compaction" {
			return payload
		}
		updated := trimmed
		updated = ensureUsageDetailsAt(updated, "response.usage")
		updated = ensureUsageDetailsAt(updated, "usage")
		if bytes.Equal(updated, trimmed) {
			return payload
		}
		return updated
	}

	// 2. SSE frames: Scan lines for data: prefixed lines and patch their JSON payloads.
	if bytes.Contains(payload, []byte("data:")) {
		lines := bytes.Split(payload, []byte("\n"))
		modified := false
		for i, line := range lines {
			trimmedLine := bytes.TrimSpace(line)
			if !bytes.HasPrefix(trimmedLine, []byte("data:")) {
				continue
			}
			prefixLen := len("data:")
			if bytes.HasPrefix(line, []byte("data: ")) {
				prefixLen = len("data: ")
			} else if bytes.HasPrefix(line, []byte("data:")) {
				prefixLen = len("data:")
			}
			dataPayload := bytes.TrimSpace(line[prefixLen:])
			if len(dataPayload) == 0 || dataPayload[0] != '{' {
				continue
			}
			if gjson.GetBytes(dataPayload, "object").String() == "response.compaction" {
				continue
			}
			updated := dataPayload
			updated = ensureUsageDetailsAt(updated, "response.usage")
			updated = ensureUsageDetailsAt(updated, "usage")
			if !bytes.Equal(updated, dataPayload) {
				newPrefix := bytes.Clone(line[:prefixLen])
				lines[i] = append(newPrefix, updated...)
				modified = true
			}
		}
		if modified {
			return bytes.Join(lines, []byte("\n"))
		}
		return payload
	}

	return payload
}

func ensureUsageDetailsAt(jsonBody []byte, path string) []byte {
	usageNode := gjson.GetBytes(jsonBody, path)
	if !usageNode.Exists() || !usageNode.IsObject() {
		return jsonBody
	}

	jsonBody = ensureUsageCounter(jsonBody, usageNode, path, "output_tokens_details", "reasoning_tokens", "completion_tokens_details.reasoning_tokens")
	jsonBody = ensureUsageCounter(jsonBody, usageNode, path, "input_tokens_details", "cached_tokens", "prompt_tokens_details.cached_tokens")
	return jsonBody
}

func ensureUsageCounter(jsonBody []byte, usageNode gjson.Result, usagePath, detailsField, counterField, fallbackPath string) []byte {
	details := usageNode.Get(detailsField)
	if !details.Exists() || details.Type == gjson.Null || !details.IsObject() {
		jsonBody, _ = sjson.SetBytes(jsonBody, usagePath+"."+detailsField+"."+counterField, usageCounterValue(gjson.Result{}, usageNode.Get(fallbackPath)))
		return jsonBody
	}
	counter := details.Get(counterField)
	if counter.Type == gjson.Number {
		return jsonBody
	}
	jsonBody, _ = sjson.SetBytes(jsonBody, usagePath+"."+detailsField+"."+counterField, usageCounterValue(counter, usageNode.Get(fallbackPath)))
	return jsonBody
}

func usageCounterValue(primary, fallback gjson.Result) int64 {
	if n, ok := parseUsageCounter(primary); ok {
		return n
	}
	if n, ok := parseUsageCounter(fallback); ok {
		return n
	}
	return 0
}

func parseUsageCounter(node gjson.Result) (int64, bool) {
	switch node.Type {
	case gjson.Number:
		return node.Int(), true
	case gjson.String:
		s := strings.TrimSpace(node.String())
		if s == "" {
			return 0, false
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), true
		}
		return 0, false
	default:
		return 0, false
	}
}
