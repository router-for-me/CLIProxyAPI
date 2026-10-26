package helps

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestPluginUsagePresenceMissingAndMeasuredZero(t *testing.T) {
	for _, payload := range []string{`{}`, `{"usage":null}`, `{"usage":{}}`, `{"service_tier":"priority"}`} {
		detail := ParsePluginExecutorResponseUsage("openai", []byte(payload))
		if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityUnclassified || detail.TokenBreakdown.Observed == nil || detail.TokenBreakdown.Observed.InputTokens {
			t.Fatalf("missing usage became a measurement: %s: %+v", payload, detail)
		}
	}
	zero := ParsePluginExecutorResponseUsage("openai-response", []byte(`{"usage":{"input_tokens":100,"output_tokens":100},"response":{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`))
	if zero.InputTokens != 0 || zero.OutputTokens != 0 || zero.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete {
		t.Fatalf("nested measured zero lost to another usage object: %+v", zero)
	}
}

func TestPluginUsagePresenceUnknownDetailsAreUnclassified(t *testing.T) {
	detail := ParsePluginExecutorResponseUsage("openai", []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"prompt_tokens_details":{"cached_tokens":5,"cache_write_tokens":2}}}`))
	b := detail.TokenBreakdown
	if !b.Valid() || b.Quality != usage.TokenAccountingQualityUnclassified || b.Input.UncachedTokens != 3 || b.Output.TotalTokens != 0 || b.UnclassifiedTokens != 20 || b.Observed.ReasoningTokens {
		t.Fatalf("unknown reasoning was presented as measured zero: %+v", detail)
	}
	if detail.InputTokens != 10 || detail.OutputTokens != 20 || detail.CacheCreationTokens != 2 {
		t.Fatalf("observed parent counters were changed: %+v", detail)
	}
}

func TestPluginUsagePresenceRestoresResponsesAfterEverySplit(t *testing.T) {
	source := []byte("data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20,\"total_tokens\":30,\"prompt_tokens_details\":{\"cached_tokens\":5,\"cache_write_tokens\":2}}}\n\n")
	target := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"total_tokens\":30}}}\n\n")
	for split := 0; split <= len(source); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			observed := NewPluginUsageObservation("openai")
			observed.Observe(source[:split])
			observed.Observe(source[split:])
			observed.Observe([]byte("data: [DONE]\n\n"))
			patched := observed.RestoreResponses(EnsureResponsesUsageDetails(target))
			var result gjson.Result
			IterateStreamLines(patched, func(line []byte) {
				if raw := ExtractStreamJSONPayload(line); gjson.ValidBytes(raw) {
					result = gjson.ParseBytes(raw)
				}
			})
			if result.Get("response.usage.input_tokens_details.cache_write_tokens").Int() != 2 || result.Get("response.usage.input_tokens_details.cached_tokens").Int() != 5 || result.Get("response.usage.output_tokens_details.reasoning_tokens").Exists() {
				t.Fatalf("usage presence lost after split %d: %s", split, patched)
			}
		})
	}
}

func TestPluginUsagePresenceKeepsExplicitZeroAndRemovesUnknownParents(t *testing.T) {
	observed := NewPluginUsageObservation("openai-response")
	observed.Observe([]byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}`))
	patched := observed.RestoreResponses(EnsureResponsesUsageDetails([]byte(`{"object":"response","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`)))
	for _, path := range []string{"usage.input_tokens_details.cached_tokens", "usage.input_tokens_details.cache_write_tokens", "usage.output_tokens_details.reasoning_tokens"} {
		if node := gjson.GetBytes(patched, path); !node.Exists() || node.Int() != 0 {
			t.Fatalf("explicit zero lost at %s: %s", path, patched)
		}
	}
	partial := NewPluginUsageObservation("openai")
	partial.Observe([]byte(`{"usage":{"prompt_tokens":10}}`))
	patched = partial.RestoreResponses([]byte(`{"object":"response","usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}`))
	if gjson.GetBytes(patched, "usage.output_tokens").Exists() || gjson.GetBytes(patched, "usage.total_tokens").Exists() {
		t.Fatalf("missing parent counters were synthesized: %s", patched)
	}
}

func TestPluginUsagePresenceAbsentUsageNeverSynthesized(t *testing.T) {
	observed := NewPluginUsageObservation("openai")
	observed.Observe([]byte(`{"choices":[]}`))
	patched := observed.RestoreResponses([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`))
	if gjson.GetBytes(patched, "response.usage").Exists() {
		t.Fatalf("unknown usage was sent as zero: %s", patched)
	}
}

func TestPluginUsagePresenceZeroSnapshotWithServiceTierReplacesPositive(t *testing.T) {
	var buffer StreamUsageBuffer
	ObservePluginExecutorStreamUsage("openai", []byte("data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120}}\n\n"), &buffer)
	ObservePluginExecutorStreamUsage("openai", []byte("data: {\"service_tier\":\"priority\",\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n"), &buffer)
	detail, ok := buffer.Detail()
	if !ok || detail.TotalTokens != 0 || detail.ResponseServiceTier != "priority" || detail.TokenBreakdown.Quality != usage.TokenAccountingQualityComplete {
		t.Fatalf("authoritative zero ignored: %+v", detail)
	}
}
