package claude

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/ccsadvisor"
)

// writeAdvisorEvents frames the completed worker message as named Anthropic
// SSE events. No content from an intermediate worker turn is committed early.
func writeAdvisorEvents(ctx context.Context, w interface {
	io.Writer
	http.Flusher
}, reply ccsadvisor.Message) error {
	type M = ccsadvisor.Message
	emit := func(name string, fields M) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fields["type"] = name
		body, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, body); err != nil {
			return err
		}
		w.Flush()
		return nil
	}
	start := maps.Clone(reply)
	start["content"] = []any{}
	start["stop_reason"] = nil
	start["stop_sequence"] = nil
	if err := emit("message_start", M{"message": start}); err != nil {
		return err
	}
	blocks, _ := reply["content"].([]any)
	for i, value := range blocks {
		block, ok := value.(M)
		if !ok {
			return fmt.Errorf("invalid content block at index %d", i)
		}
		initial := maps.Clone(block)
		var deltas []M
		switch block["type"] {
		case "text":
			initial["text"] = ""
			deltas = append(deltas, M{"type": "text_delta", "text": block["text"]})
			if citations, ok := block["citations"].([]any); ok {
				initial["citations"] = []any{}
				for _, citation := range citations {
					deltas = append(deltas, M{"type": "citations_delta", "citation": citation})
				}
			}
		case "server_tool_use", "tool_use":
			initial["input"] = M{}
			input, err := json.Marshal(block["input"])
			if err != nil {
				return err
			}
			deltas = append(deltas, M{"type": "input_json_delta", "partial_json": string(input)})
		case "thinking":
			initial["thinking"] = ""
			deltas = append(deltas, M{"type": "thinking_delta", "thinking": block["thinking"]})
			if signature, ok := block["signature"]; ok {
				initial["signature"] = ""
				deltas = append(deltas, M{"type": "signature_delta", "signature": signature})
			}
		}
		if err := emit("content_block_start", M{"index": i, "content_block": initial}); err != nil {
			return err
		}
		for _, delta := range deltas {
			if err := emit("content_block_delta", M{"index": i, "delta": delta}); err != nil {
				return err
			}
		}
		if err := emit("content_block_stop", M{"index": i}); err != nil {
			return err
		}
	}
	if err := emit("message_delta", M{"delta": M{"stop_reason": reply["stop_reason"], "stop_sequence": reply["stop_sequence"]}, "usage": reply["usage"]}); err != nil {
		return err
	}
	return emit("message_stop", M{})
}
