package responses

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeResponsesServerToolStopReason(t *testing.T) {
	for _, tc := range []struct {
		reason  string
		status  string
		detail  string
		event   string
		errCode string
		endTurn string
	}{
		// pause_turn is an unfinished but valid turn: completed with end_turn=false
		// so the client continues it by sending the paused content back.
		{reason: "pause_turn", status: "completed", endTurn: "false"},
		{reason: " PAUSE_TURN ", status: "completed", endTurn: "false"},
		{reason: "max_tokens", status: "incomplete", detail: `{"reason":"max_output_tokens"}`},
		{reason: "refusal", status: "incomplete", detail: `{"reason":"content_filter"}`},
		{reason: "model_context_window_exceeded", status: "incomplete", detail: `{"reason":"max_output_tokens"}`},
		{reason: "end_turn", status: "completed"},
		{reason: "tool_use", status: "completed"},
		{reason: "stop_sequence", status: "completed"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			chunks := claudeWebSearchStreamChunks()
			stop := chunks[len(chunks)-1]
			chunks = append(chunks[:len(chunks)-1], []byte(fmt.Sprintf(
				`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":12}}`, tc.reason)), stop)
			check := func(t *testing.T, response gjson.Result) {
				t.Helper()
				if got := response.Get("status").String(); got != tc.status {
					t.Fatalf("status = %q, want %q", got, tc.status)
				}
				if got := response.Get("incomplete_details").Raw; got != tc.detail && !(tc.detail == "" && (got == "null" || got == "")) {
					t.Fatalf("incomplete_details = %q, want %q", got, tc.detail)
				}
				if got := response.Get("error.code").String(); got != tc.errCode {
					t.Fatalf("error.code = %q, want %q", got, tc.errCode)
				}
				if got := response.Get("end_turn").Raw; got != tc.endTurn {
					t.Fatalf("end_turn = %q, want %q", got, tc.endTurn)
				}
				if got := response.Get("output.0.action.query").String(); got != "lindorm vector" {
					t.Fatalf("search query = %q", got)
				}
				if got := response.Get("output.0.results.0.encrypted_content").String(); got != "ENC_A" {
					t.Fatalf("search replay content = %q", got)
				}
				if got := response.Get("usage.output_tokens").Int(); got != 12 {
					t.Fatalf("output_tokens = %d, want 12", got)
				}
			}
			t.Run("stream", func(t *testing.T) {
				terminals := 0
				for _, output := range translateClaudeResponsesStreamThroughRegistry(chunks) {
					event, data := parseClaudeResponsesSSEEvent(t, output)
					if event != "response.completed" && event != "response.incomplete" && event != "response.failed" {
						continue
					}
					terminals++
					want := tc.event
					if want == "" {
						want = "response." + tc.status
					}
					if event != want {
						t.Fatalf("terminal = %q, want %q", event, want)
					}
					check(t, data.Get("response"))
				}
				if terminals != 1 {
					t.Fatalf("terminal count = %d, want 1", terminals)
				}
			})
			t.Run("buffered", func(t *testing.T) {
				output := ConvertClaudeResponseToOpenAIResponsesNonStream(
					context.Background(), "claude-test", nil, nil, bytes.Join(chunks, []byte("\n")), nil)
				check(t, gjson.ParseBytes(output))
			})
		})
	}
}
