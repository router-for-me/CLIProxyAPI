package helps

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func newClaudeStreamUsageEstimatorForTest(t *testing.T, model string, inputTokens ...int64) *ClaudeStreamUsageEstimator {
	t.Helper()
	estimator, err := NewClaudeStreamUsageEstimator(model, inputTokens...)
	if err != nil {
		t.Fatalf("new estimator: %v", err)
	}
	return estimator
}

func TestClaudeStreamUsageEstimatorEmitsInputAtResponseCreated(t *testing.T) {
	estimator := newClaudeStreamUsageEstimatorForTest(t, "gpt-5.6-luna", 20_053)
	snapshot, emit := estimator.ObserveCodexEvent([]byte(`{"type":"response.created","response":{"model":"gpt-5.6-luna"}}`))
	if !emit {
		t.Fatal("response.created did not emit input usage")
	}
	if snapshot.InputTokens != 20_053 || snapshot.OutputTokens != 0 || snapshot.ThinkingTokens != 0 {
		t.Fatalf("response.created usage = %+v", snapshot)
	}
}

func TestClaudeStreamUsageEstimatorEmitsCumulativeProgress(t *testing.T) {
	estimator := newClaudeStreamUsageEstimatorForTest(t, "gpt-5.6-sol", 1234)
	estimator.ObserveCodexEvent([]byte(`{"type":"response.created"}`))
	last := int64(0)
	emissions := 0
	for i := 0; i < 20; i++ {
		payload := []byte(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, strings.Repeat("visible output ", 32)))
		snapshot, emit := estimator.ObserveCodexEvent(payload)
		if !emit {
			continue
		}
		if snapshot.OutputTokens <= last {
			t.Fatalf("usage estimate did not increase: previous=%d current=%d", last, snapshot.OutputTokens)
		}
		if snapshot.InputTokens != 1234 {
			t.Fatalf("input tokens = %d, want 1234", snapshot.InputTokens)
		}
		last = snapshot.OutputTokens
		emissions++
	}
	if emissions == 0 {
		t.Fatal("expected at least one cumulative usage emission")
	}
}

func TestClaudeStreamUsageEstimatorWaitsForResponseCreated(t *testing.T) {
	estimator := newClaudeStreamUsageEstimatorForTest(t, "gpt-5.6-sol", 1234)
	payload := []byte(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, strings.Repeat("visible output ", 100)))
	if snapshot, emit := estimator.ObserveCodexEvent(payload); emit || snapshot != (ClaudeUsageSnapshot{}) {
		t.Fatalf("pre-start usage = (%+v, %v), want zero and false", snapshot, emit)
	}
}

func TestClaudeStreamUsageEstimatorEmitsDuringSilentReasoning(t *testing.T) {
	estimator := newClaudeStreamUsageEstimatorForTest(t, "gpt-5.6-luna", 20_053)
	startedAt := time.Unix(100, 0)
	estimator.observeCodexEventAt([]byte(`{"type":"response.created"}`), startedAt)
	if snapshot, emit := estimator.ObserveTime(startedAt.Add(claudeReasoningEstimateWarmup)); emit || snapshot.OutputTokens != 0 {
		t.Fatalf("warmup usage = (%+v, %v), want zero and false", snapshot, emit)
	}
	snapshot, emit := estimator.ObserveTime(startedAt.Add(10 * time.Second))
	if !emit {
		t.Fatal("silent reasoning did not emit live usage")
	}
	wantThinking := int64(5) * claudeReasoningTokensPerSecond
	if snapshot.InputTokens != 20_053 || snapshot.OutputTokens != wantThinking || snapshot.ThinkingTokens != wantThinking {
		t.Fatalf("silent reasoning usage = %+v, want input=20053 output=thinking=%d", snapshot, wantThinking)
	}
}

func TestClaudeStreamUsageEstimatorStopsElapsedReasoningAtVisibleOutput(t *testing.T) {
	estimator := newClaudeStreamUsageEstimatorForTest(t, "gpt-5.6-luna")
	startedAt := time.Unix(100, 0)
	estimator.observeCodexEventAt([]byte(`{"type":"response.created"}`), startedAt)
	estimator.observeCodexEventAt([]byte(`{"type":"response.output_text.delta","delta":"done"}`), startedAt.Add(10*time.Second))
	before, _ := estimator.ObserveTime(startedAt.Add(11 * time.Second))
	after, emit := estimator.ObserveTime(startedAt.Add(60 * time.Second))
	if emit {
		t.Fatalf("elapsed estimate continued after visible output: before=%+v after=%+v", before, after)
	}
	if after.ThinkingTokens != before.ThinkingTokens {
		t.Fatalf("thinking tokens changed after reasoning ended: before=%d after=%d", before.ThinkingTokens, after.ThinkingTokens)
	}
}

func TestClaudeStreamUsageEstimatorStopsAfterCompletion(t *testing.T) {
	estimator := newClaudeStreamUsageEstimatorForTest(t, "gpt-5.6-luna")
	startedAt := time.Unix(100, 0)
	estimator.observeCodexEventAt([]byte(`{"type":"response.created"}`), startedAt)
	estimator.observeCodexEventAt([]byte(`{"type":"response.completed"}`), startedAt.Add(10*time.Second))
	if _, emit := estimator.ObserveTime(startedAt.Add(60 * time.Second)); emit {
		t.Fatal("completed estimator emitted additional usage")
	}
}

func TestClaudeStreamUsageEstimatorCoversTranslatedDeltaTypes(t *testing.T) {
	tests := []struct {
		name         string
		eventType    string
		wantThinking bool
	}{
		{name: "output text", eventType: "response.output_text.delta"},
		{name: "reasoning summary", eventType: "response.reasoning_summary_text.delta", wantThinking: true},
		{name: "function arguments", eventType: "response.function_call_arguments.delta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			estimator := newClaudeStreamUsageEstimatorForTest(t, "gpt-5.6-sol")
			estimator.ObserveCodexEvent([]byte(`{"type":"response.created"}`))
			payload := []byte(fmt.Sprintf(`{"type":%q,"delta":%q}`, tt.eventType, strings.Repeat("visible output ", 100)))
			snapshot, emit := estimator.ObserveCodexEvent(payload)
			if !emit || snapshot.OutputTokens <= 0 {
				t.Fatalf("usage = (%+v, %v), want positive emission", snapshot, emit)
			}
			if tt.wantThinking != (snapshot.ThinkingTokens > 0) {
				t.Fatalf("thinking tokens = %d, wantThinking=%v", snapshot.ThinkingTokens, tt.wantThinking)
			}
		})
	}
}

func TestClaudeStreamUsageEstimatorUsesEncryptedReasoningSize(t *testing.T) {
	tests := []struct {
		model string
		min   int64
		max   int64
	}{
		{model: "gpt-5.6-luna", min: 380, max: 440},
		{model: "gpt-5.6-terra", min: 380, max: 440},
		{model: "gpt-5.6-sol", min: 180, max: 220},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			estimator := newClaudeStreamUsageEstimatorForTest(t, tt.model)
			estimator.ObserveCodexEvent([]byte(fmt.Sprintf(`{"type":"response.created","response":{"model":%q}}`, tt.model)))
			cipher := base64.URLEncoding.EncodeToString(make([]byte, 1801))
			payload := []byte(fmt.Sprintf(`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":%q}}`, cipher))
			snapshot, emit := estimator.ObserveCodexEvent(payload)
			if !emit {
				t.Fatal("reasoning item did not emit usage")
			}
			if snapshot.ThinkingTokens < tt.min || snapshot.ThinkingTokens > tt.max {
				t.Fatalf("thinking estimate = %d, want [%d,%d]", snapshot.ThinkingTokens, tt.min, tt.max)
			}
			if snapshot.OutputTokens != snapshot.ThinkingTokens {
				t.Fatalf("output tokens = %d, thinking tokens = %d", snapshot.OutputTokens, snapshot.ThinkingTokens)
			}
		})
	}
}

func TestClaudeCumulativeUsageEvent(t *testing.T) {
	event := ClaudeCumulativeUsageEvent(ClaudeUsageSnapshot{InputTokens: 20_053, OutputTokens: 1_535, ThinkingTokens: 1_115})
	data := claudeSSEEventData(event)
	if got := gjson.GetBytes(data, "type").String(); got != "message_delta" {
		t.Fatalf("event type = %q, want message_delta; event=%s", got, event)
	}
	if !gjson.GetBytes(data, "delta").Exists() || !gjson.GetBytes(data, "delta").IsObject() {
		t.Fatalf("message delta is missing a delta object; event=%s", event)
	}
	for _, path := range []string{
		"context_management",
		"delta.container",
		"delta.stop_details",
		"delta.stop_reason",
		"delta.stop_sequence",
		"usage.iterations",
	} {
		if got := gjson.GetBytes(data, path).Raw; got != "null" {
			t.Fatalf("%s = %q, want null; event=%s", path, got, event)
		}
	}
	if got := gjson.GetBytes(data, "usage.input_tokens").Raw; got != "null" {
		t.Fatalf("input tokens = %q, want null because message_start owns input usage; event=%s", got, event)
	}
	if got := gjson.GetBytes(data, "usage.output_tokens").Int(); got != 1_535 {
		t.Fatalf("output tokens = %d, want 1535; event=%s", got, event)
	}
	if got := gjson.GetBytes(data, "usage.output_tokens_details.thinking_tokens").Int(); got != 1_115 {
		t.Fatalf("thinking tokens = %d, want 1115; event=%s", got, event)
	}
	for _, path := range []string{
		"usage.cache_creation_input_tokens",
		"usage.cache_read_input_tokens",
		"usage.server_tool_use.web_fetch_requests",
		"usage.server_tool_use.web_search_requests",
	} {
		if !gjson.GetBytes(data, path).Exists() {
			t.Fatalf("%s is missing; event=%s", path, event)
		}
	}
}

func TestClaudeApplyMessageStartUsagePreservesFollowingEvents(t *testing.T) {
	thinkingStart := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n"
	chunks := [][]byte{[]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n" + thinkingStart)}

	if !ClaudeApplyMessageStartUsage(chunks, ClaudeUsageSnapshot{InputTokens: 20_053}) {
		t.Fatal("expected message_start usage patch")
	}
	data := claudeSSEEventData(chunks[0])
	if got := gjson.GetBytes(data, "message.usage.input_tokens").Int(); got != 20_053 {
		t.Fatalf("message_start input tokens = %d, want 20053; chunk=%s", got, chunks[0])
	}
	if !strings.Contains(string(chunks[0]), thinkingStart) {
		t.Fatalf("message_start patch discarded following thinking event: %s", chunks[0])
	}
}

func TestClaudeThinkingTokenCountEmitter(t *testing.T) {
	emitter := NewClaudeThinkingTokenCountEmitter(true)
	emitter.ObserveTranslatedChunks([][]byte{[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":3,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")})

	event := emitter.Event(ClaudeUsageSnapshot{ThinkingTokens: 130})
	data := claudeSSEEventData(event)
	if got := gjson.GetBytes(data, "type").String(); got != "content_block_delta" {
		t.Fatalf("event type = %q, want content_block_delta; event=%s", got, event)
	}
	if got := gjson.GetBytes(data, "index").Int(); got != 3 {
		t.Fatalf("block index = %d, want 3; event=%s", got, event)
	}
	if got := gjson.GetBytes(data, "delta.type").String(); got != "thinking_delta" {
		t.Fatalf("delta type = %q, want thinking_delta; event=%s", got, event)
	}
	if got := gjson.GetBytes(data, "delta.estimated_tokens").Int(); got != 128 {
		t.Fatalf("estimated token total = %d, want 128; event=%s", got, event)
	}
	if got := gjson.GetBytes(data, "delta.thinking").String(); got != "" {
		t.Fatalf("thinking = %q, want empty", got)
	}

	if event = emitter.Event(ClaudeUsageSnapshot{ThinkingTokens: 190}); len(event) != 0 {
		t.Fatalf("sub-quantum progress emitted an event: %s", event)
	}
	if event = emitter.Event(ClaudeUsageSnapshot{ThinkingTokens: 260}); gjson.GetBytes(claudeSSEEventData(event), "delta.estimated_tokens").Int() != 256 {
		t.Fatalf("second estimated token event = %s, want cumulative 256", event)
	}

	emitter.ObserveTranslatedChunks([][]byte{[]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":3}\n\n")})
	if event = emitter.Event(ClaudeUsageSnapshot{ThinkingTokens: 512}); len(event) != 0 {
		t.Fatalf("closed thinking block emitted an event: %s", event)
	}

	emitter.ObserveTranslatedChunks([][]byte{[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":4,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")})
	if event = emitter.Event(ClaudeUsageSnapshot{ThinkingTokens: 390}); gjson.GetBytes(claudeSSEEventData(event), "delta.estimated_tokens").Int() != 128 {
		t.Fatalf("new block estimated token event = %s, want block-local cumulative 128", event)
	}

	t.Run("disabled", func(t *testing.T) {
		disabled := NewClaudeThinkingTokenCountEmitter(false)
		disabled.ObserveTranslatedChunks([][]byte{[]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n")})
		if event := disabled.Event(ClaudeUsageSnapshot{ThinkingTokens: 128}); len(event) != 0 {
			t.Fatalf("disabled emitter produced an event: %s", event)
		}
	})
}
