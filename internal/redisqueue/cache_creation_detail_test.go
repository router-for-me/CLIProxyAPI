package redisqueue_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// Replay a sanitized Claude tool-loop stream and verify the Redis payload.
func TestClaudeCacheCreationDetailReachesRedisPayload(t *testing.T) {
	const payload = `data: {"type":"message_start","message":{"usage":{"input_tokens":4,"cache_creation_input_tokens":2962,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2962},"output_tokens":16}}}
data: {"type":"message_delta","usage":{"input_tokens":6,"cache_creation_input_tokens":11193,"cache_read_input_tokens":2962,"output_tokens":89,"iterations":[{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2962}},{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":8231,"ephemeral_1h_input_tokens":0}}]}}`
	prevEnabled, prevUsage := redisqueue.Enabled(), redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	defer func() {
		redisqueue.SetEnabled(prevEnabled)
		redisqueue.SetUsageStatisticsEnabled(prevUsage)
	}()
	messages, unsubscribe := redisqueue.SubscribeUsage()
	defer unsubscribe()
	var buffer helps.StreamUsageBuffer
	for _, line := range strings.Split(payload, "\n") {
		buffer.ObserveClaudeStream([]byte(line))
	}
	reporter := helps.NewUsageReporter(context.Background(), "claude", "claude-opus-5-5", nil)
	if !buffer.Publish(context.Background(), reporter) {
		t.Fatal("usage not published")
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case raw := <-messages:
			var event struct {
				SupportRefresh bool `json:"support_refresh"`
				Tokens         struct {
					CacheCreationTokens int64 `json:"cache_creation_tokens"`
					TotalTokens         int64 `json:"total_tokens"`
					Detail              *struct {
						FiveMinute int64 `json:"ephemeral_5m_input_tokens"`
						OneHour    int64 `json:"ephemeral_1h_input_tokens"`
					} `json:"cache_creation_detail"`
				} `json:"tokens"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.SupportRefresh {
				continue
			}
			if event.Tokens.CacheCreationTokens != 11193 || event.Tokens.TotalTokens != 14250 || event.Tokens.Detail == nil || event.Tokens.Detail.FiveMinute != 8231 || event.Tokens.Detail.OneHour != 2962 {
				t.Fatalf("unexpected Redis usage: %s", raw)
			}
			return
		case <-timer.C:
			t.Fatal("timed out waiting for Redis usage")
		}
	}
}
