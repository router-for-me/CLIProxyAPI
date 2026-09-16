package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNativeResponsesPreservesSSEEventNamesAndDataFrames(t *testing.T) {
	for _, terminal := range []string{"completed", "incomplete"} {
		for _, tt := range []struct {
			name                                     string
			named, mixed, afterData, multiline, crlf bool
		}{
			{name: "named", named: true},
			{name: "named CRLF", named: true, crlf: true},
			{name: "data only"},
			{name: "data only then named", named: true, mixed: true},
			{name: "event field after data", named: true, afterData: true},
			{name: "multiline data", named: true, multiline: true},
			{name: "multiline CRLF", named: true, multiline: true, crlf: true},
		} {
			t.Run(terminal+"/"+tt.name, func(t *testing.T) {
				types := []string{"response.created", "response.output_text.delta", "response." + terminal}
				payloads := []string{
					`{"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
					`{"type":"response.output_text.delta","delta":"hello\nworld"}`,
					fmt.Sprintf(`{"type":"response.%s","response":{"id":"r1","status":%q,"output":[]}}`, terminal, terminal),
				}
				var upstream strings.Builder
				for i, payload := range payloads {
					named := tt.named && (!tt.mixed || i != 0)
					if named && !tt.afterData {
						fmt.Fprintf(&upstream, "event: %s\n", types[i])
					}
					if tt.multiline {
						var pretty bytes.Buffer
						if err := json.Indent(&pretty, []byte(payload), "", "  "); err != nil {
							t.Fatal(err)
						}
						payload = pretty.String()
					}
					for _, line := range strings.Split(payload, "\n") {
						fmt.Fprintf(&upstream, "data: %s\n", line)
					}
					if named && tt.afterData {
						fmt.Fprintf(&upstream, "event: %s\n", types[i])
					}
					upstream.WriteByte('\n')
				}
				wire := upstream.String()
				if tt.crlf {
					wire = strings.ReplaceAll(wire, "\n", "\r\n")
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, wire)
				}))
				defer server.Close()
				const provider = "native-sse-probe"
				cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: provider, WireAPI: "responses", BaseURL: server.URL}}}
				executor := runtimeexecutor.NewOpenAICompatExecutor(provider, cfg)
				auth := &coreauth.Auth{Provider: provider, Attributes: map[string]string{"base_url": server.URL}}
				body := []byte(`{"model":"gpt-4o","input":"hi","stream":true}`)
				result, err := executor.ExecuteStream(t.Context(), auth, coreexecutor.Request{Model: "gpt-4o", Payload: body}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true, OriginalRequest: body})
				if err != nil {
					t.Fatal(err)
				}
				var downstream bytes.Buffer
				framer := &responsesSSEFramer{}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					framer.WriteChunk(&downstream, chunk.Payload)
				}
				framer.Flush(&downstream)
				frames := strings.Split(strings.TrimSpace(strings.ReplaceAll(downstream.String(), "\r\n", "\n")), "\n\n")
				if len(frames) != len(types) {
					t.Fatalf("got %d frames, want %d: %q", len(frames), len(types), downstream.String())
				}
				for i, frame := range frames {
					var name string
					var data []string
					for _, line := range strings.Split(frame, "\n") {
						field, value, _ := strings.Cut(line, ":")
						value = strings.TrimPrefix(value, " ")
						if field == "event" {
							name = value
						}
						if field == "data" {
							data = append(data, value)
						}
					}
					wantName := ""
					if tt.named && (!tt.mixed || i != 0) {
						wantName = types[i]
					}
					if name != wantName {
						t.Errorf("frame %d event = %q, want %q", i, name, wantName)
					}
					payload := strings.Join(data, "\n")
					if !json.Valid([]byte(payload)) || gjson.Get(payload, "type").String() != types[i] {
						t.Fatalf("frame %d lost JSON data: %q", i, frame)
					}
					if i == 1 && gjson.Get(payload, "delta").String() != "hello\nworld" {
						t.Fatalf("delta text changed: %q", payload)
					}
				}
			})
		}
	}
}
