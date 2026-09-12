package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"testing"
)

func TestReviewAntigravityGroundingBilling(t *testing.T) {
	detail := ParseAntigravityUsage([]byte(`{"response":{"candidates":[{"groundingMetadata":{"webSearchQueries":["weather"]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"totalTokenCount":110}}}`))
	if !gjson.Get(detail.RawUsage, "unpriced_server_tools").Bool() {
		t.Fatalf("grounding charge dropped: raw=%s", detail.RawUsage)
	}
}

func TestReviewGeminiStreamRetainsPriorGrounding(t *testing.T) {
	var buffer StreamUsageBuffer
	ObservePluginExecutorStreamUsage("gemini", []byte("data: {\"candidates\":[{\"groundingMetadata\":{\"webSearchQueries\":[\"weather\"]}}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":5,\"totalTokenCount\":105}}\n\n"), &buffer)
	ObservePluginExecutorStreamUsage("gemini", []byte("data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":10,\"totalTokenCount\":110}}\n\n"), &buffer)
	detail, _ := buffer.Detail()
	if !gjson.Get(detail.RawUsage, "unpriced_server_tools").Bool() {
		t.Fatalf("earlier grounding charge dropped: raw=%s", detail.RawUsage)
	}
}

func TestReviewOpenAIStreamRetainsToolBilling(t *testing.T) {
	detail, ok := ParseOpenAIStreamUsage([]byte(`data: {"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110},"tool_usage":{"web_search":1}}`))
	if !ok || !gjson.Get(detail.RawUsage, "tool_usage.web_search").Exists() {
		t.Fatalf("stream root tool billing dropped: raw=%s", detail.RawUsage)
	}
}

func TestBillingMetadataSurvivesNativeGeminiFiltering(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		var billing UsageBillingMetadata
		early := `{"candidates":[{"groundingMetadata":{"webSearchQueries":["weather"]}}]}`
		final := `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"totalTokenCount":110}}`
		if wrapped {
			early = `{"response":` + early + `}`
			final = `{"response":` + final + `}`
		}
		billing.ObservePayload([]byte("data: " + early))
		unmeasured := billing.Apply(usage.Detail{})
		if unmeasured.UsageObserved || unmeasured.TotalTokens != 0 {
			t.Fatal("billing metadata manufactured token usage")
		}
		billing.ObservePayload([]byte("data: " + final))
		filtered := FilterSSEUsageMetadata([]byte("data: " + final))
		detail, ok := ParseGeminiStreamUsage(filtered)
		if wrapped {
			detail, ok = ParseAntigravityStreamUsage(filtered)
		}
		if !ok {
			t.Fatal("missing terminal token usage")
		}
		detail = billing.Apply(detail)
		if detail.TotalTokens != 110 || !gjson.Get(detail.RawUsage, "unpriced_server_tools").Bool() {
			t.Fatalf("filtered native stream lost billing dimensions: %+v", detail)
		}
	}
}

func TestOpenAIStreamBillingOnlyFramesRetainFinalCounters(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveOpenAIStream([]byte(`data: {"tool_usage":{"web_search":1}}`))
	if _, ok := buffer.Detail(); ok {
		t.Fatal("billing-only frame manufactured observed token usage")
	}
	buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`))
	buffer.ObserveOpenAIStream([]byte(`data: {"tool_usage":{"file_search":1}}`))
	detail, ok := buffer.Detail()
	if !ok || detail.TotalTokens != 110 || !gjson.Get(detail.RawUsage, "tool_usage.web_search").Exists() || !gjson.Get(detail.RawUsage, "tool_usage.file_search").Exists() {
		t.Fatalf("metadata-only frames corrupted accounting: %+v", detail)
	}
}
