package helps

import (
	"context"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"sync"
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
	if detail, ok := buffer.Detail(); !ok || detail.UsageObserved || detail.TotalTokens != 0 {
		t.Fatalf("billing-only frame must remain unmeasured: %+v, ok=%v", detail, ok)
	}
	buffer.ObserveOpenAIStream([]byte(`data: {"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`))
	buffer.ObserveOpenAIStream([]byte(`data: {"tool_usage":{"file_search":1}}`))
	detail, ok := buffer.Detail()
	if !ok || detail.TotalTokens != 110 || !gjson.Get(detail.RawUsage, "tool_usage.web_search").Exists() || !gjson.Get(detail.RawUsage, "tool_usage.file_search").Exists() {
		t.Fatalf("metadata-only frames corrupted accounting: %+v", detail)
	}
}

type billingOnlyCapture struct {
	mu      sync.Mutex
	model   string
	records []usage.Record
}

func (*billingOnlyCapture) Synchronous() bool { return true }
func (s *billingOnlyCapture) HandleUsage(_ context.Context, r usage.Record) {
	if r.Model == s.model {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.records = append(s.records, r)
	}
}
func TestStreamBillingWithoutTokenUsageIsPublished(t *testing.T) {
	for _, protocol := range []string{"gemini", "antigravity", "openai"} {
		for _, failed := range []bool{false, true} {
			name := protocol + "/success"
			if failed {
				name = protocol + "/failure"
			}
			t.Run(name, func(t *testing.T) {
				payload := `{"candidates":[{"groundingMetadata":{"webSearchQueries":["weather"]}}],"tool_usage":{"web_search":1}}`
				if protocol == "antigravity" {
					payload = `{"response":` + payload + `}`
				}
				var buffer StreamUsageBuffer
				ObservePluginExecutorStreamUsage(protocol, []byte("data: "+payload), &buffer)
				detail, ok := buffer.Detail()
				if !ok || detail.UsageObserved || detail.TotalTokens != 0 || !gjson.Get(detail.RawUsage, "unpriced_server_tools").Bool() {
					t.Fatalf("billing-only detail was lost or marked measured: %+v, ok=%v", detail, ok)
				}
				capture := &billingOnlyCapture{model: t.Name()}
				usage.RegisterNamedPlugin(t.Name(), capture)
				defer usage.RegisterNamedPlugin(t.Name(), &billingOnlyCapture{})
				reporter := NewUsageReporter(context.Background(), protocol, t.Name(), nil)
				if failed {
					buffer.PublishFailure(context.Background(), reporter, errors.New("truncated stream"))
				} else {
					buffer.Publish(context.Background(), reporter)
				}
				reporter.EnsurePublished(context.Background())
				capture.mu.Lock()
				defer capture.mu.Unlock()
				if len(capture.records) != 1 {
					t.Fatalf("records=%d", len(capture.records))
				}
				record := capture.records[0]
				if record.Failed != failed || record.Detail.UsageObserved || !gjson.Get(record.Detail.RawUsage, "tool_usage.web_search").Exists() {
					t.Fatalf("published billing lost: %+v", record)
				}
			})
		}
	}
	var empty StreamUsageBuffer
	empty.ObserveBillingPayload([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`))
	if _, ok := empty.Detail(); ok {
		t.Fatal("unrelated payload manufactured billing metadata")
	}
}
