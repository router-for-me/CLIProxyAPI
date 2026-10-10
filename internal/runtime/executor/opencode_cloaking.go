package executor

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openCodeBase62Chars    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	openCodeUserAgent      = "opencode/1.18.35 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"
	openCodeClientHeader   = "cli"
	openCodeProjectHeader  = "global"
	openCodeDefaultMaxTokens = 32000
)

var openCodeDummyTools = []map[string]any{
	{
		"type": "function",
		"function": map[string]any{
			"name":        "read",
			"description": "Internal placeholder tool. DO NOT CALL THIS TOOL. Always reply directly in natural language text.",
			"parameters":  map[string]any{"type": "object"},
		},
	},
	{
		"type": "function",
		"function": map[string]any{
			"name":        "bash",
			"description": "Internal placeholder tool. DO NOT CALL THIS TOOL. Always reply directly in natural language text.",
			"parameters":  map[string]any{"type": "object"},
		},
	},
	{
		"type": "function",
		"function": map[string]any{
			"name":        "write",
			"description": "Internal placeholder tool. DO NOT CALL THIS TOOL. Always reply directly in natural language text.",
			"parameters":  map[string]any{"type": "object"},
		},
	},
}

// isOpenCodeTarget checks whether an upstream endpoint/provider is OpenCode.
func isOpenCodeTarget(baseURL, provider, compatName string) bool {
	u := strings.ToLower(strings.TrimSpace(baseURL))
	p := strings.ToLower(strings.TrimSpace(provider))
	c := strings.ToLower(strings.TrimSpace(compatName))
	return strings.Contains(u, "opencode.ai") ||
		p == "opencode" ||
		c == "opencode" ||
		strings.HasPrefix(p, "openai-compatible-opencode")
}

// genOpenCodeID generates a timestamp-bound Base62 ID according to OpenCode's reverse-engineered schema.
// descending = true for session ID (~current & 0xFFFFFFFFFFFF)
// descending = false for request ID (current & 0xFFFFFFFFFFFF)
func genOpenCodeID(prefix string, descending bool) string {
	ts := time.Now().UnixMilli()
	current := uint64(ts)*4096 + 1
	var val uint64
	if descending {
		val = (^current) & 0xFFFFFFFFFFFF
	} else {
		val = current & 0xFFFFFFFFFFFF
	}

	var timePart strings.Builder
	for i := 0; i < 6; i++ {
		shift := 40 - 8*i
		b := byte((val >> shift) & 0xFF)
		fmt.Fprintf(&timePart, "%02x", b)
	}

	randBytes := make([]byte, 14)
	if _, err := rand.Read(randBytes); err != nil {
		// Fallback deterministic random bytes
		for i := range randBytes {
			randBytes[i] = byte(i * 17)
		}
	}
	var randPart strings.Builder
	for _, b := range randBytes {
		randPart.WriteByte(openCodeBase62Chars[int(b)%len(openCodeBase62Chars)])
	}

	return fmt.Sprintf("%s_%s%s", prefix, timePart.String(), randPart.String())
}

// applyOpenCodeHeaders injects required authentication & fingerprint headers to pass OpenCode cloud inspection.
func applyOpenCodeHeaders(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("User-Agent", openCodeUserAgent)
	req.Header.Set("x-opencode-client", openCodeClientHeader)
	req.Header.Set("x-opencode-project", openCodeProjectHeader)

	sesID := genOpenCodeID("ses", true)
	msgID := genOpenCodeID("msg", false)

	req.Header.Set("x-opencode-session", sesID)
	req.Header.Set("x-opencode-session-id", sesID)
	req.Header.Set("x-opencode-request", msgID)
}

// applyOpenCodePayload adjusts request body to satisfy OpenCode's FreeTier requirements.
// Specifically, tools are mandatory, and stream is required upstream.
func applyOpenCodePayload(payload []byte, forceStream bool) ([]byte, error) {
	if len(payload) == 0 {
		return payload, nil
	}

	// 1. Ensure tools are present (dummy functions if missing)
	toolsVal := gjson.GetBytes(payload, "tools")
	if !toolsVal.Exists() || !toolsVal.IsArray() || len(toolsVal.Array()) == 0 {
		var err error
		payload, err = sjson.SetBytes(payload, "tools", openCodeDummyTools)
		if err != nil {
			return nil, fmt.Errorf("opencode: inject dummy tools error: %w", err)
		}
		if !gjson.GetBytes(payload, "tool_choice").Exists() {
			payload, err = sjson.SetBytes(payload, "tool_choice", "auto")
			if err != nil {
				return nil, fmt.Errorf("opencode: inject tool_choice error: %w", err)
			}
		}
	}

	// 2. Default max_tokens if missing
	if !gjson.GetBytes(payload, "max_tokens").Exists() && !gjson.GetBytes(payload, "max_completion_tokens").Exists() {
		var err error
		payload, err = sjson.SetBytes(payload, "max_tokens", openCodeDefaultMaxTokens)
		if err != nil {
			return nil, fmt.Errorf("opencode: inject max_tokens error: %w", err)
		}
	}

	// 3. Force stream upstream if required
	if forceStream {
		var err error
		payload, err = sjson.SetBytes(payload, "stream", true)
		if err != nil {
			return nil, fmt.Errorf("opencode: force stream error: %w", err)
		}
	}

	return payload, nil
}

// aggregateOpenCodeSSEToChatCompletion consumes an SSE stream from OpenCode Zen
// and rebuilds a standard non-streaming OpenAI chat completion JSON response.
func aggregateOpenCodeSSEToChatCompletion(sseReader io.Reader, fallbackModel string) ([]byte, error) {
	scanner := bufio.NewScanner(sseReader)
	// OpenCode chunks can be large (up to 1MB)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	var (
		resID         string
		resModel      = fallbackModel
		created       int64
		contentBuffer bytes.Buffer
		reasoningBuf  bytes.Buffer
		finishReason  = "stop"
		promptTokens  int64
		compTokens    int64
	)

	for scanner.Scan() {
		line := scanner.Bytes()
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(data, []byte("[DONE]")) {
			break
		}
		if len(data) == 0 {
			continue
		}

		parsed := gjson.ParseBytes(data)
		if id := parsed.Get("id").String(); id != "" && resID == "" {
			resID = id
		}
		if m := parsed.Get("model").String(); m != "" {
			resModel = m
		}
		if c := parsed.Get("created").Int(); c != 0 {
			created = c
		}

		choices := parsed.Get("choices").Array()
		if len(choices) > 0 {
			choice := choices[0]
			delta := choice.Get("delta")
			if delta.Exists() {
				if text := delta.Get("content").String(); text != "" {
					contentBuffer.WriteString(text)
				}
				if r := delta.Get("reasoning").String(); r != "" {
					reasoningBuf.WriteString(r)
				}
				if rc := delta.Get("reasoning_content").String(); rc != "" {
					reasoningBuf.WriteString(rc)
				}
			}
			if fr := choice.Get("finish_reason").String(); fr != "" {
				finishReason = fr
			}
		}

		if usage := parsed.Get("usage"); usage.Exists() {
			if pt := usage.Get("prompt_tokens").Int(); pt > 0 {
				promptTokens = pt
			}
			if ct := usage.Get("completion_tokens").Int(); ct > 0 {
				compTokens = ct
			}
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, fmt.Errorf("opencode: read stream error: %w", err)
	}

	if resID == "" {
		resID = fmt.Sprintf("chatcmpl-opencode-%d", time.Now().UnixMilli())
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	if promptTokens == 0 {
		promptTokens = 10
	}
	if compTokens == 0 {
		// Estimate completion tokens from character count
		compTokens = int64(contentBuffer.Len() / 4)
		if compTokens == 0 && contentBuffer.Len() > 0 {
			compTokens = 1
		}
	}

	finalContent := contentBuffer.String()
	if finalContent == "" && reasoningBuf.Len() > 0 {
		finalContent = reasoningBuf.String()
	}

	finalResponse := map[string]any{
		"id":      resID,
		"object":  "chat.completion",
		"created": created,
		"model":   resModel,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": finalContent,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": compTokens,
			"total_tokens":      promptTokens + compTokens,
		},
	}

	return json.Marshal(finalResponse)
}
