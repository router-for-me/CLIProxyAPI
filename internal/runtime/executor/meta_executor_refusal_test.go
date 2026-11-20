package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestMetaExecutor_SynthesizesUnstreamedRefusalAcrossFormats(t *testing.T) {
	const refusalText = "I'm sorry, but I can't help with that request."
	const upstreamSSE = "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_refusal\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"muse-ember-1.0\",\"output\":[]}}\n\n" +
		"event: response.in_progress\n" +
		"data: {\"type\":\"response.in_progress\",\"sequence_number\":1,\"response\":{\"id\":\"resp_refusal\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"muse-ember-1.0\",\"output\":[]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_refusal\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-ember-1.0\",\"output\":[{\"id\":\"msg_resp_refusal\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"I'm sorry, but I can't help with that request.\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}

	formats := []struct {
		format  string
		payload string
	}{
		{
			format:  "openai-response",
			payload: `{"model":"muse-ember-1.0","input":"hello"}`,
		},
		{
			format:  "openai",
			payload: `{"model":"muse-ember-1.0","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			format:  "claude",
			payload: `{"model":"muse-ember-1.0","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			format:  "gemini",
			payload: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
		},
		{
			format:  "codex",
			payload: `{"model":"muse-ember-1.0","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
		},
		{
			format:  "interactions",
			payload: `{"model":"muse-ember-1.0","input":"hello"}`,
		},
	}

	for _, tc := range formats {
		t.Run("stream/"+tc.format, func(t *testing.T) {
			res, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "muse-ember-1.0",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(tc.format),
				Stream:       true,
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream(%s) error = %v", tc.format, errStream)
			}
			var buf strings.Builder
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatalf("chunk error = %v", chunk.Err)
				}
				buf.Write(chunk.Payload)
				buf.WriteByte('\n')
			}
			got := buf.String()
			if !strings.Contains(got, refusalText) {
				t.Fatalf("stream(%s) missing refusal text %q:\n%s", tc.format, refusalText, got)
			}
			switch tc.format {
			case "openai-response":
				if !strings.Contains(got, "response.refusal.delta") || !strings.Contains(got, "response.output_item.done") {
					t.Fatalf("openai-response stream missing synthesized refusal events:\n%s", got)
				}
			case "openai":
				if count := strings.Count(got, refusalText); count != 1 {
					t.Fatalf("openai stream emitted refusal text %d times, want 1:\n%s", count, got)
				}
			case "claude":
				if count := strings.Count(got, refusalText); count != 1 {
					t.Fatalf("claude stream emitted refusal text %d times, want 1:\n%s", count, got)
				}
			case "gemini":
				if count := strings.Count(got, refusalText); count != 1 {
					t.Fatalf("gemini stream emitted refusal text %d times, want 1:\n%s", count, got)
				}
			case "codex":
				if !strings.Contains(got, "response.refusal.delta") || !strings.Contains(got, "response.output_item.done") {
					t.Fatalf("codex stream missing synthesized refusal events:\n%s", got)
				}
			case "interactions":
				if count := strings.Count(got, refusalText); count != 1 {
					t.Fatalf("interactions stream emitted refusal text %d times, want 1:\n%s", count, got)
				}
			}
		})

		t.Run("nonstream/"+tc.format, func(t *testing.T) {
			resp, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "muse-ember-1.0",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(tc.format),
			})
			if errExec != nil {
				t.Fatalf("Execute(%s) error = %v", tc.format, errExec)
			}
			got := string(resp.Payload)
			if !strings.Contains(got, refusalText) {
				t.Fatalf("nonstream(%s) missing refusal text %q:\n%s", tc.format, refusalText, got)
			}
			if tc.format == "openai" {
				if content := ojson.GetBytesNoCopy(resp.Payload, "choices.0.message.content").String(); content != refusalText {
					t.Fatalf("openai nonstream content = %q, want %q", content, refusalText)
				}
			}
		})
	}
}

func TestMetaExecutor_AlreadyStreamedRefusalNotDuplicated(t *testing.T) {
	const refusalText = "I'm sorry, but I can't help with that request."
	const upstreamSSE = "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_refusal\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"muse-ember-1.0\",\"output\":[]}}\n\n" +
		"event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg_resp_refusal\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n" +
		"event: response.refusal.delta\n" +
		"data: {\"type\":\"response.refusal.delta\",\"sequence_number\":2,\"item_id\":\"msg_resp_refusal\",\"output_index\":0,\"content_index\":0,\"delta\":\"I'm sorry, but I can't help with that request.\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":0,\"item\":{\"id\":\"msg_resp_refusal\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"I'm sorry, but I can't help with that request.\"}]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"id\":\"resp_refusal\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-ember-1.0\",\"output\":[{\"id\":\"msg_resp_refusal\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"I'm sorry, but I can't help with that request.\"}]}]}}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}

	for _, format := range []string{"openai-response", "openai", "claude", "gemini", "interactions"} {
		t.Run(format, func(t *testing.T) {
			var payload string
			switch format {
			case "openai":
				payload = `{"model":"muse-ember-1.0","messages":[{"role":"user","content":"hello"}]}`
			case "claude":
				payload = `{"model":"muse-ember-1.0","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`
			case "gemini":
				payload = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
			default:
				payload = `{"model":"muse-ember-1.0","input":"hello"}`
			}
			res, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "muse-ember-1.0",
				Payload: []byte(payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(format),
				Stream:       true,
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream(%s) error = %v", format, errStream)
			}
			var buf strings.Builder
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatalf("chunk error = %v", chunk.Err)
				}
				buf.Write(chunk.Payload)
				buf.WriteByte('\n')
			}
			got := buf.String()
			if format == "openai-response" {
				if count := strings.Count(got, `"type":"response.refusal.delta"`); count != 1 {
					t.Fatalf("openai-response emitted %d response.refusal.delta events, want 1:\n%s", count, got)
				}
			} else if count := strings.Count(got, refusalText); count != 1 {
				t.Fatalf("%s emitted refusal text %d times, want 1:\n%s", format, count, got)
			}
		})
	}
}

func TestMetaExecutor_SynthesizesUnstreamedIncompleteRefusalAcrossFormats(t *testing.T) {
	const refusalText = "I'm sorry, but I can't help with that request."
	const upstreamSSE = "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_inc\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"muse-ember-1.0\",\"output\":[]}}\n\n" +
		"event: response.incomplete\n" +
		"data: {\"type\":\"response.incomplete\",\"sequence_number\":1,\"response\":{\"id\":\"resp_inc\",\"object\":\"response\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"content_filter\"},\"model\":\"muse-ember-1.0\",\"output\":[{\"id\":\"msg_inc\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"incomplete\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"I'm sorry, but I can't help with that request.\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer server.Close()

	exec := NewMetaExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}

	for _, format := range []string{"openai-response", "openai", "claude", "gemini", "interactions"} {
		t.Run(format, func(t *testing.T) {
			var payload string
			switch format {
			case "openai":
				payload = `{"model":"muse-ember-1.0","messages":[{"role":"user","content":"hello"}]}`
			case "claude":
				payload = `{"model":"muse-ember-1.0","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`
			case "gemini":
				payload = `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
			default:
				payload = `{"model":"muse-ember-1.0","input":"hello"}`
			}
			res, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "muse-ember-1.0",
				Payload: []byte(payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(format),
				Stream:       true,
			})
			if errStream != nil {
				t.Fatalf("ExecuteStream(%s) error = %v", format, errStream)
			}
			var buf strings.Builder
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatalf("chunk error = %v", chunk.Err)
				}
				buf.Write(chunk.Payload)
				buf.WriteByte('\n')
			}
			got := buf.String()
			if format == "openai-response" {
				if !strings.Contains(got, "response.refusal.delta") || !strings.Contains(got, "response.incomplete") {
					t.Fatalf("openai-response stream missing synthesized refusal or incomplete terminal:\n%s", got)
				}
			} else if count := strings.Count(got, refusalText); count != 1 {
				t.Fatalf("%s emitted refusal text %d times, want 1:\n%s", format, count, got)
			}
		})
	}
}
