package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func codexAllocationStream(deltas, deltaSize int) string {
	var stream strings.Builder
	stream.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_alloc\",\"model\":\"gpt-5.4\"}}\n\n")
	stream.WriteString("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_alloc\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n")
	stream.WriteString("data: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n")
	for i := 0; i < deltas; i++ {
		fmt.Fprintf(&stream, ": comment-%04d\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":%q}\n\n", i, strings.Repeat(string(rune('a'+i%26)), deltaSize))
	}
	stream.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_alloc\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":128,\"total_tokens\":133}}}\n\n")
	return stream.String()
}

type codexAllocationTransport struct{ stream string }

func (tr codexAllocationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(tr.stream)),
		Request:    req,
	}, nil
}

func TestCodexStreamChunksRemainOwnedAfterScannerAdvances(t *testing.T) {
	const deltas = 48
	const deltaSize = 512
	for _, buffered := range []bool{false, true} {
		for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse, "test-codex-raw-ownership"} {
			t.Run(fmt.Sprintf("buffered=%t/%s", buffered, format), func(t *testing.T) {
				cfg := &config.Config{}
				cfg.Codex.StreamBootstrapBuffering = buffered
				executor := NewCodexExecutor(cfg)
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: codexAllocationStream(deltas, deltaSize)})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
				result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)}, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: format})
				if err != nil {
					t.Fatal(err)
				}
				var retained [][]byte
				var snapshots []string
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					retained = append(retained, chunk.Payload)
					snapshots = append(snapshots, string(chunk.Payload))
				}
				if len(retained) == 0 {
					t.Fatal("stream emitted no chunks")
				}
				var text strings.Builder
				for index, chunk := range retained {
					if string(chunk) != snapshots[index] {
						t.Fatalf("chunk %d changed after scanner advanced", index)
					}
					for _, line := range bytes.Split(chunk, []byte("\n")) {
						if !bytes.HasPrefix(line, []byte("data:")) {
							continue
						}
						data := bytes.TrimSpace(line[5:])
						switch gjson.GetBytes(data, "type").String() {
						case "response.output_text.delta":
							text.WriteString(gjson.GetBytes(data, "delta").String())
						case "content_block_delta":
							text.WriteString(gjson.GetBytes(data, "delta.text").String())
						}
					}
				}
				var want strings.Builder
				for i := 0; i < deltas; i++ {
					want.WriteString(strings.Repeat(string(rune('a'+i%26)), deltaSize))
				}
				if text.String() != want.String() {
					t.Fatalf("retained text differs: got %d bytes, want %d", text.Len(), want.Len())
				}
			})
		}
	}
}

func BenchmarkCodexExecuteStreamAllocations(b *testing.B) {
	for _, buffered := range []bool{false, true} {
		for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse} {
			b.Run(fmt.Sprintf("buffered=%t/%s", buffered, format), func(b *testing.B) {
				stream := codexAllocationStream(256, 1024)
				cfg := &config.Config{}
				cfg.Codex.StreamBootstrapBuffering = buffered
				executor := NewCodexExecutor(cfg)
				ctx := context.WithValue(b.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
				request := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)}
				opts := cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: format}
				b.ReportAllocs()
				b.SetBytes(int64(len(stream)))
				b.ResetTimer()
				for b.Loop() {
					result, err := executor.ExecuteStream(ctx, auth, request, opts)
					if err != nil {
						b.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							b.Fatal(chunk.Err)
						}
					}
				}
			})
		}
	}
}
