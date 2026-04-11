package executor

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// validateOpenAINonStreamSuccessBody guards against pseudo-success payloads:
// HTTP 200 with non-OpenAI business error JSON, or empty OpenAI completions.
func validateOpenAINonStreamSuccessBody(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return statusErr{code: http.StatusBadGateway, msg: "upstream returned empty body with HTTP 200"}
	}
	if !gjson.ValidBytes(trimmed) {
		return statusErr{code: http.StatusBadGateway, msg: "upstream returned non-json body with HTTP 200"}
	}

	root := gjson.ParseBytes(trimmed)

	if errNode := root.Get("error"); errNode.Exists() {
		msg := strings.TrimSpace(errNode.Get("message").String())
		if msg == "" {
			msg = strings.TrimSpace(errNode.String())
		}
		if msg == "" {
			msg = "upstream returned OpenAI error payload with HTTP 200"
		}
		code := httpStatusFromResult(errNode.Get("code"))
		if code == 0 {
			code = http.StatusBadGateway
		}
		return statusErr{code: code, msg: msg}
	}

	if statusRaw := strings.TrimSpace(root.Get("status").String()); statusRaw != "" && root.Get("msg").Exists() && !root.Get("choices").Exists() {
		msg := strings.TrimSpace(root.Get("msg").String())
		if msg == "" {
			msg = "upstream business error payload with HTTP 200"
		}
		return statusErr{code: mapBusinessStatusToHTTP(statusRaw), msg: msg}
	}

	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() || len(choices.Array()) == 0 {
		return statusErr{code: http.StatusBadGateway, msg: "upstream returned HTTP 200 but no choices"}
	}

	first := choices.Array()[0]
	message := first.Get("message")
	if !message.Exists() {
		if strings.TrimSpace(first.Get("text").String()) == "" {
			return statusErr{code: http.StatusBadGateway, msg: "upstream returned HTTP 200 but first choice is empty"}
		}
		return nil
	}

	hasContent := strings.TrimSpace(message.Get("content").String()) != ""
	hasReasoning := strings.TrimSpace(message.Get("reasoning_content").String()) != ""
	hasRefusal := strings.TrimSpace(message.Get("refusal").String()) != ""
	hasToolCalls := hasNonEmptyArray(message.Get("tool_calls"))
	hasImages := hasNonEmptyArray(message.Get("images"))
	hasAudio := message.Get("audio").Exists()
	completionTokens := root.Get("usage.completion_tokens").Int()

	if !hasContent && !hasReasoning && !hasRefusal && !hasToolCalls && !hasImages && !hasAudio && completionTokens <= 0 {
		return statusErr{code: http.StatusBadGateway, msg: "upstream returned HTTP 200 but empty completion"}
	}

	return nil
}

func hasNonEmptyArray(r gjson.Result) bool {
	return r.Exists() && r.IsArray() && len(r.Array()) > 0
}

func httpStatusFromResult(r gjson.Result) int {
	if !r.Exists() {
		return 0
	}
	if r.Type == gjson.Number {
		return mapBusinessStatusToHTTP(strconv.FormatInt(r.Int(), 10))
	}
	return mapBusinessStatusToHTTP(strings.TrimSpace(r.String()))
}

func mapBusinessStatusToHTTP(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return http.StatusBadGateway
	}
	switch strings.ToLower(raw) {
	case "434":
		return http.StatusUnauthorized
	case "449":
		return http.StatusTooManyRequests
	}
	if v, err := strconv.Atoi(raw); err == nil {
		if v >= 400 && v <= 599 {
			return v
		}
	}
	return http.StatusBadGateway
}

func summarizeInvalidOpenAI200Body(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	s := string(trimmed)
	if len(s) > 500 {
		return fmt.Sprintf("%s...(truncated)", s[:500])
	}
	return s
}
