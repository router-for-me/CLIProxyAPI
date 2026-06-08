package auth

import (
	"bytes"
	"math"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type streamSummaryFields struct {
	outputTokens int64
	finishReason string
}

func (f *streamSummaryFields) observePayload(payload []byte) {
	if f == nil || len(payload) == 0 {
		return
	}

	for _, line := range bytes.Split(payload, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}

		switch {
		case bytes.HasPrefix(trimmed, []byte("data:")):
			f.observeJSON(bytes.TrimSpace(trimmed[len("data:"):]))
		case bytes.HasPrefix(trimmed, []byte("event:")),
			bytes.HasPrefix(trimmed, []byte("id:")),
			bytes.HasPrefix(trimmed, []byte("retry:")),
			bytes.HasPrefix(trimmed, []byte(":")):
			continue
		case bytes.HasPrefix(trimmed, []byte("{")) || bytes.HasPrefix(trimmed, []byte("[")):
			f.observeJSON(trimmed)
		}
	}
}

func (f *streamSummaryFields) observeJSON(payload []byte) {
	if f == nil || len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !gjson.ValidBytes(payload) {
		return
	}

	root := gjson.ParseBytes(payload)
	if outputTokens, ok := summaryOutputTokens(root); ok {
		f.outputTokens = outputTokens
	}
	if finishReason := summaryFinishReason(root); finishReason != "" {
		f.finishReason = finishReason
	}
}

func summaryOutputTokens(root gjson.Result) (int64, bool) {
	for _, path := range []string{
		"usage.output_tokens",
		"usage.completion_tokens",
		"response.usage.output_tokens",
		"response.usage.completion_tokens",
		"usageMetadata.candidatesTokenCount",
		"usage_metadata.candidatesTokenCount",
		"response.usageMetadata.candidatesTokenCount",
		"response.usage_metadata.candidatesTokenCount",
	} {
		value := root.Get(path)
		if value.Exists() {
			return value.Int(), true
		}
	}
	return 0, false
}

func summaryFinishReason(root gjson.Result) string {
	if reason := firstChoiceString(root, "finish_reason"); reason != "" {
		return reason
	}
	if reason := firstStringPath(root,
		"finish_reason",
		"delta.stop_reason",
		"stop_reason",
		"message.stop_reason",
		"response.stop_reason",
		"response.incomplete_details.reason",
		"incomplete_details.reason",
		"candidates.0.finishReason",
		"finishReason",
	); reason != "" {
		return reason
	}
	if reason := firstChoiceString(root, "native_finish_reason"); reason != "" {
		return reason
	}
	return ""
}

func firstChoiceString(root gjson.Result, field string) string {
	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() {
		return ""
	}
	for _, choice := range choices.Array() {
		if value := cleanSummaryString(choice.Get(field).String()); value != "" {
			return value
		}
	}
	return ""
}

func firstStringPath(root gjson.Result, paths ...string) string {
	for _, path := range paths {
		if value := cleanSummaryString(root.Get(path).String()); value != "" {
			return value
		}
	}
	return ""
}

func cleanSummaryString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "null") {
		return ""
	}
	return value
}

func streamTokensPerSecond(outputTokens int64, streamDuration time.Duration) float64 {
	if outputTokens <= 0 || streamDuration <= 0 {
		return 0
	}
	perSecond := float64(outputTokens) / streamDuration.Seconds()
	return math.Round(perSecond*100) / 100
}
