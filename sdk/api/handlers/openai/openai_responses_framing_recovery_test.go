package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesSSEFramerRecoversFragmentedJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
	}{
		{
			name: "multiline-json",
			chunks: []string{
				`data: {"type"`,
				`:"response.metadata",` + "\ndata: " + `"metadata":{"marker":"private"}}` + "\n\n",
			},
		},
		{
			name: "multiple-colons",
			chunks: []string{
				`data: {"type"`,
				`:"response.metadata","metadata"`,
				`:{"marker":"private"}}` + "\n\n",
			},
		},
		{
			name: "real-comment",
			chunks: []string{
				`data: {"type"`,
				`:"response.metadata",` + "\n: keepalive\ndata: " + `"metadata":{"marker":"private"}}` + "\n\n",
			},
		},
		{
			name: "split-data-field",
			chunks: []string{
				`data`,
				`: {"type":"response.metadata","metadata":{"marker":"private"}}` + "\n\n",
			},
		},
		{
			name: "multiple-colons-with-comment",
			chunks: []string{
				`data: {"type"`,
				`:"response.metadata",` + "\n: keepalive\ndata: " + `"metadata"`,
				`:{"marker":"private"}}` + "\n\n",
			},
		},
		{
			name: "separate-comment-with-split-key",
			chunks: []string{
				`data: {"type"`,
				`: keepalive`,
				`data: :"response.metadata","metadata"`,
				`:{"marker":"private"}}` + "\n\n",
			},
		},
		{
			name: "colon-before-string-field",
			chunks: []string{
				`data: {"type":"response.metadata","metadata":{"marker"`,
				`:"`,
				`data: private"}}` + "\n\n",
			},
		},
	} {
		for _, isCodexClient := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				var output bytes.Buffer
				framer := &responsesSSEFramer{isCodexClient: isCodexClient}
				for _, chunk := range tc.chunks {
					framer.WriteChunk(&output, []byte(chunk))
				}
				framer.WriteChunk(&output, []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
				got := output.String()
				if isCodexClient {
					frames := strings.Split(got, "\n\n")
					payload, ok := responsesSSEDataPayload([]byte(frames[0]))
					wantMarker := "private"
					if tc.name == "colon-before-string-field" {
						wantMarker = "data: private"
					}
					if !ok || !json.Valid(payload) || gjson.GetBytes(payload, "type").String() != "response.metadata" || gjson.GetBytes(payload, "metadata.marker").String() != wantMarker {
						t.Fatalf("Codex metadata was corrupted: %q", got)
					}
					if strings.Contains(strings.Join(tc.chunks, ""), ": keepalive") && !strings.Contains(got, ": keepalive") {
						t.Fatalf("genuine SSE comment was removed: %q", got)
					}
				} else if strings.Contains(got, "response.metadata") || strings.Contains(got, "private") {
					t.Fatalf("private metadata leaked: %q", got)
				}
				if !strings.Contains(got, "response.completed") {
					t.Fatalf("completion missing: %q", got)
				}
			})
		}
	}
}

func TestSSEFrameAccumulatorRecoversFragmentedJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
	}{
		{
			name: "multiline-json",
			chunks: []string{
				`data: {"type"`,
				`:"response.completed",` + "\ndata: " + `"response":{"output":[]}}` + "\n\n",
			},
		},
		{
			name: "multiple-colons",
			chunks: []string{
				`data: {"type"`,
				`:"response.completed","response"`,
				`:{"output":[]}}` + "\n\n",
			},
		},
		{
			name: "real-comment",
			chunks: []string{
				`data: {"type"`,
				`:"response.completed",` + "\n: keepalive\ndata: " + `"response":{"output":[]}}` + "\n\n",
			},
		},
		{
			name: "split-data-field",
			chunks: []string{
				`data`,
				`: {"type":"response.completed","response":{"output":[]}}` + "\n\n",
			},
		},
		{
			name: "multiple-colons-with-comment",
			chunks: []string{
				`data: {"type"`,
				`:"response.completed",` + "\n: keepalive\ndata: " + `"response"`,
				`:{"output":[]}}` + "\n\n",
			},
		},
		{
			name: "separate-comment-with-split-key",
			chunks: []string{
				`data: {"type"`,
				`: keepalive`,
				`data: :"response.completed","response"`,
				`:{"output":[]}}` + "\n\n",
			},
		},
		{
			name: "colon-before-string-field",
			chunks: []string{
				`data: {"type":"response.completed","metadata":{"marker"`,
				`:"`,
				`data: private"}}` + "\n\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := &sseFrameAccumulator{}
			var frames [][]byte
			for _, chunk := range tc.chunks {
				frames = append(frames, acc.AddChunk([]byte(chunk))...)
			}
			if len(frames) != 1 {
				t.Fatalf("frames = %q, want one", frames)
			}
			payload, ok := responsesSSEDataPayload(frames[0])
			if !ok || !json.Valid(payload) || gjson.GetBytes(payload, "type").String() != "response.completed" {
				t.Fatalf("image response was corrupted: %q", frames[0])
			}
			if strings.Contains(strings.Join(tc.chunks, ""), ": keepalive") && !bytes.Contains(frames[0], []byte(": keepalive")) {
				t.Fatalf("genuine SSE comment was removed: %q", frames[0])
			}
		})
	}
}
