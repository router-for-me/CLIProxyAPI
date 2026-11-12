package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Changing thinking or safeguards between turns must not change the wire beta
// profile that Anthropic bound to the Thread's first successful message.
func TestClaudeThreadBetasRemainStable(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, callerBetas := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/caller-betas=%v", stream, callerBetas), func(t *testing.T) {
				auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-beta"}}
				headers := http.Header{}
				if callerBetas {
					headers.Set("Anthropic-Beta", "message-threads-2026-08-12,effort-2025-11-24")
				} else {
					auth.Attributes["api_key"] = "sk-ant-oat-thread-beta"
					auth.Metadata = map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
				}
				var sent []string
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					body, errRead := io.ReadAll(req.Body)
					if errRead != nil {
						return nil, errRead
					}
					betas := strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ",")
					sent = append(sent, betas)
					if len(sent) > 1 {
						if got := ojson.GetBytes(body, "thread.previous_message_id").String(); got != "msg_thread_1" {
							t.Errorf("previous_message_id = %q, want msg_thread_1", got)
						}
						if betas != sent[0] {
							return claudeThreadTestResponse(req, http.StatusBadRequest, "application/json", `{"type":"error","error":{"type":"invalid_request_error","message":"differing field(s): anthropic-beta. These fields must stay the same across a Thread's turns"}}`), nil
						}
					}
					id := fmt.Sprintf("msg_thread_%d", len(sent))
					if ojson.GetBytes(body, "stream").Bool() {
						return claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", claudeThreadTestSSE(id)), nil
					}
					return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage(id)), nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				executor := NewClaudeExecutor(&config.Config{})
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers}
				bodies := []string{
					`{"model":"claude-opus-5","max_tokens":2048,"thread":{"type":"create"},"thinking":{"type":"enabled","budget_tokens":1024},"safeguards":{},"messages":[{"role":"user","content":"first"}]}`,
					`{"model":"claude-opus-5","max_tokens":2048,"thread":{"type":"continue","previous_message_id":"msg_thread_1"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"second"}]}`,
				}
				for turn, body := range bodies {
					request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(body)}
					if stream {
						result, errStream := executor.ExecuteStream(ctx, auth, request, opts)
						if errStream != nil {
							t.Fatalf("turn %d ExecuteStream: %v; wire betas=%q", turn+1, errStream, sent)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatalf("turn %d stream: %v", turn+1, chunk.Err)
							}
						}
					} else if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
						t.Fatalf("turn %d Execute: %v; wire betas=%q", turn+1, errExecute, sent)
					}
				}
				if len(sent) != 2 || sent[0] == "" || sent[0] != sent[1] {
					t.Fatalf("wire betas = %q, want two identical nonempty profiles", sent)
				}
			})
		}
	}
}

// Declared Thread capabilities must survive the first turn even if that turn
// does not exercise them: later turns cannot add them to the bound profile.
func TestClaudeThreadFirstTurnKeepsDeclaredCapabilities(t *testing.T) {
	const betas = "message-threads-2026-08-12,effort-2025-11-24,thinking-display-updates-2026-08-18,extended-cache-ttl-2025-04-11"
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirmed=%v", confirmed), func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`)
			req, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			incoming := http.Header{"Anthropic-Beta": []string{betas}}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-thread-declarations"}}
			if errHeaders := applyClaudeHeaders(req, auth, "key-thread-declarations", false, nil, body, nil, incoming, confirmed); errHeaders != nil {
				t.Fatal(errHeaders)
			}
			if got := req.Header.Get("Anthropic-Beta"); got != betas {
				t.Fatalf("first-turn betas = %q, want declared Thread capabilities %q", got, betas)
			}
		})
	}
}

func TestClaudeThreadFirstTurnKeepsRequiredBetas(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		beta string
	}{
		{name: "fast", body: `"speed":"fast"`, beta: "fast-mode-2026-02-01"},
		{name: "advisor", body: `"tools":[{"type":"advisor_20260301","name":"advisor"}]`, beta: "advisor-tool-2026-03-01"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-5","thread":{"type":"create"},` + test.body + `,"messages":[{"role":"user","content":"hello"}]}`)
			req, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			incoming := http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12"}}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-thread-required"}}
			if errHeaders := applyClaudeHeaders(req, auth, "key-thread-required", false, nil, body, nil, incoming, false); errHeaders != nil {
				t.Fatal(errHeaders)
			}
			if got := req.Header.Get("Anthropic-Beta"); !strings.Contains(got, test.beta) {
				t.Fatalf("first-turn beta = %q, missing required %s", got, test.beta)
			}
		})
	}
}

func TestClaudeThreadOrdinaryRefusalCanContinue(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			calls := 0
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if stream && calls == 1 {
					return claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", strings.ReplaceAll(claudeThreadTestSSE("msg_thread_1"), "end_turn", "refusal")), nil
				}
				return claudeThreadTestResponse(req, http.StatusOK, "application/json", strings.ReplaceAll(claudeThreadTestMessage(fmt.Sprintf("msg_thread_%d", calls)), "end_turn", "refusal")), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-refusal"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12"}}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
			if stream {
				result, errStream := executor.ExecuteStream(ctx, auth, request, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"messages":[{"role":"user","content":"second"}]}`)
			if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatalf("continuation after successful ordinary refusal: %v", errExecute)
			}
			if calls != 2 {
				t.Fatalf("upstream requests = %d, want 2", calls)
			}
		})
	}
}

// The reader is paused exactly before message_stop. Once its gate is reached,
// all earlier sends have happened and the producer cannot enqueue more output.
// Draining the channel at that point deterministically exposes early completion.
func TestClaudeThreadStreamPublishesStateBeforeCompletion(t *testing.T) {
	for _, test := range []struct {
		format sdktranslator.Format
		oauth  bool
	}{
		{format: sdktranslator.FormatClaude},
		{format: sdktranslator.FormatOpenAI},
		{format: sdktranslator.FormatOpenAIResponse},
		{format: sdktranslator.FormatClaude, oauth: true},
		{format: sdktranslator.FormatOpenAI, oauth: true},
		{format: sdktranslator.FormatOpenAIResponse, oauth: true},
	} {
		t.Run(fmt.Sprintf("%s/oauth=%v", test.format, test.oauth), func(t *testing.T) {
			waitingStop := make(chan struct{})
			releaseStop := make(chan struct{})
			defer func() {
				select {
				case <-releaseStop:
				default:
					close(releaseStop)
				}
			}()
			prefix, suffix, ok := strings.Cut(claudeThreadTestSSE("msg_thread_1"), "event: message_stop")
			if !ok {
				t.Fatal("SSE fixture has no message_stop")
			}
			body := &claudeThreadGatedReader{prefix: strings.NewReader(prefix), suffix: strings.NewReader("event: message_stop" + suffix), waiting: waitingStop, release: releaseStop}
			var sent []string
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				sent = append(sent, strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ","))
				if len(sent) == 1 {
					resp := claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", "")
					resp.Body = io.NopCloser(body)
					return resp, nil
				}
				if sent[0] != sent[1] {
					t.Errorf("continuation beta changed: %q", sent)
				}
				return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_2")), nil
			})
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport)))
			defer cancel()
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-timing"}}
			if test.oauth {
				auth.Attributes["api_key"] = "sk-ant-oat-thread-timing"
				auth.Metadata = map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
			}
			headers := http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12,effort-2025-11-24"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: test.format, Headers: headers}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","max_tokens":2048,"thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
			result, errStream := executor.ExecuteStream(ctx, auth, request, opts)
			if errStream != nil {
				t.Fatal(errStream)
			}
			var beforeStop [][]byte
		waiting:
			for {
				select {
				case chunk, open := <-result.Chunks:
					if !open || chunk.Err != nil {
						t.Fatalf("stream ended before stop gate: %v", chunk.Err)
					}
					beforeStop = append(beforeStop, chunk.Payload)
				case <-waitingStop:
					for len(result.Chunks) > 0 {
						chunk := <-result.Chunks
						beforeStop = append(beforeStop, chunk.Payload)
					}
					break waiting
				}
			}
			for _, payload := range beforeStop {
				if claudeThreadTestTerminal(payload) {
					t.Errorf("completion escaped before upstream message_stop: %s", payload)
				}
			}
			close(releaseStop)
			continued := false
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream: %v", chunk.Err)
				}
				if !continued && claudeThreadTestTerminal(chunk.Payload) {
					continued = true
					request.Payload = []byte(`{"model":"claude-opus-5","max_tokens":2048,"thread":{"type":"continue","previous_message_id":"msg_thread_1"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"second"}]}`)
					opts.ResponseFormat = sdktranslator.FormatClaude
					if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
						t.Errorf("immediate continuation after first completion: %v", errExecute)
					}
				}
			}
			if !continued || len(sent) != 2 {
				t.Fatalf("continued=%v, upstream requests=%d, want successful immediate continuation", continued, len(sent))
			}
		})
	}
}

func TestClaudeThreadBetasSharedAcrossScopedCopiesAndIsolated(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			calls := 0
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if got := strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ","); got != "gateway-thread-beta" {
					t.Errorf("final configured wire profile = %q, want gateway-thread-beta", got)
				}
				if stream && calls == 1 {
					return claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", claudeThreadTestSSE("msg_thread_1")), nil
				}
				return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			registered := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: "credential-a", Attributes: map[string]string{"api_key": "key-scoped-thread", "base_url": "https://gateway.example.com", "header:Anthropic-Beta": "gateway-thread-beta"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-a"}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
			first := registered.ForAPIKey()
			if stream {
				result, errStream := first.ExecuteStream(ctx, auth, request, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, errExecute := first.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"second"}]}`)
			second := registered.ForAPIKey()
			if _, errExecute := second.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatalf("continuation via a fresh scoped executor: %v", errExecute)
			}
			for _, isolated := range []string{"caller", "credential", "upstream"} {
				otherAuth := auth.Clone()
				otherOpts := opts
				switch isolated {
				case "caller":
					otherOpts.Metadata = map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-b"}
				case "credential":
					otherAuth.ID = "credential-b"
				case "upstream":
					otherAuth.Attributes["base_url"] = "https://other.example.com"
				}
				_, errExecute := second.Execute(ctx, otherAuth, request, otherOpts)
				var requestErr cliproxyexecutor.RequestScopedError
				if errExecute == nil || !errors.As(errExecute, &requestErr) || !requestErr.IsRequestScoped() {
					t.Errorf("%s isolation error = %v, want request-scoped missing Thread", isolated, errExecute)
				}
			}
			if calls != 2 {
				t.Fatalf("upstream requests = %d, want no sends for foreign Thread state", calls)
			}
		})
	}
}

func TestClaudeThreadRejectsChangedBetaPolicy(t *testing.T) {
	for _, operation := range []string{"strip", "append", "payload replace", "payload filter"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			initial := &config.Config{}
			if strings.HasPrefix(operation, "payload") {
				initial.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "claude-opus-5"}}, Params: map[string]any{"betas": []string{"payload-a-2099-01-01"}}}}
			}
			executor := NewClaudeExecutor(initial)
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-policy"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12,effort-2025-11-24"}}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
			if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			cfg := &config.Config{}
			switch operation {
			case "strip":
				cfg.ClaudeHeaderDefaults.AnthropicBetaStrip = []string{"effort-2025-11-24"}
			case "append":
				cfg.ClaudeHeaderDefaults.AnthropicBetaAppend = []string{"operator-beta-2099-01-01"}
			case "payload replace":
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "claude-opus-5"}}, Params: map[string]any{"betas": []string{"payload-b-2099-01-01"}}}}
			case "payload filter":
				cfg.Payload.Override = initial.Payload.Override
				cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "claude-opus-5"}}, Params: []string{"betas"}}}
			}
			request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"messages":[{"role":"user","content":"second"}]}`)
			_, errExecute := executor.WithConfig(cfg).Execute(ctx, auth, request, opts)
			var requestErr cliproxyexecutor.RequestScopedError
			if errExecute == nil || !errors.As(errExecute, &requestErr) || !requestErr.IsRequestScoped() {
				t.Fatalf("changed beta policy error = %v, want request-scoped replay requirement", errExecute)
			}
			if calls != 1 {
				t.Fatalf("sent %d requests, want no send that violates configured beta policy", calls)
			}
		})
	}
}

func TestClaudeThreadCallerProfileSurvivesMissingCache(t *testing.T) {
	for _, missing := range []string{"restart", "eviction", "body restart", "body eviction", "body only restart", "body only eviction", "derived restart", "derived eviction"} {
		t.Run(missing, func(t *testing.T) {
			dbPath := claudeThreadTestKVStore(t)
			const betas = "message-threads-2026-08-12,effort-2025-11-24"
			wantBetas := betas
			callerBetas := betas
			bodyField := ""
			if strings.HasPrefix(missing, "body") {
				wantBetas += ",context-1m-2025-08-07"
				bodyField = `"betas":["context-1m-2025-08-07"],`
			}
			if strings.Contains(missing, "only") {
				callerBetas = ""
				bodyField = `"betas":["message-threads-2026-08-12","effort-2025-11-24","context-1m-2025-08-07"],`
			}
			initialFields := bodyField
			if strings.HasPrefix(missing, "derived") {
				wantBetas += ",fast-mode-2026-02-01"
				initialFields += `"speed":"fast",`
			}
			calls := 0
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if got := strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ","); got != wantBetas {
					t.Errorf("wire beta = %q, want client's stable profile %q", got, wantBetas)
				}
				return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage(fmt.Sprintf("msg_thread_%d", calls))), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-cold-cache"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"Anthropic-Beta": []string{callerBetas}}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{` + initialFields + `"model":"claude-opus-5","thread":{"type":"create"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"first"}]}`)}
			if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			if strings.HasSuffix(missing, "restart") {
				if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
					t.Fatal(errShutdown)
				}
				if errConfigure := kvstore.ConfigureLocal(true, dbPath); errConfigure != nil {
					t.Fatal(errConfigure)
				}
				executor = NewClaudeExecutor(&config.Config{})
			} else {
				otherOpts := opts
				otherOpts.Metadata = map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "other-caller"}
				for range 1024 {
					if _, errExecute := executor.Execute(ctx, auth, request, otherOpts); errExecute != nil {
						t.Fatal(errExecute)
					}
				}
			}
			request.Payload = []byte(`{` + bodyField + `"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"second"}]}`)
			if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
				t.Fatalf("valid caller-owned Thread rejected after %s: %v", missing, errExecute)
			}
		})
	}
}

func TestClaudeThreadNativeOAuthResumesWithoutCache(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			claudeThreadTestKVStore(t)
			var sent []string
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				sent = append(sent, strings.Join(helps.HeaderValuesCaseInsensitive(req.Header, "Anthropic-Beta"), ","))
				if len(sent) > 1 && sent[1] != sent[0] {
					t.Errorf("native OAuth profile changed after restart: %q", sent)
				}
				if ojson.GetBytes(body, "stream").Bool() {
					return claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", claudeThreadTestSSE("msg_thread_1")), nil
				}
				return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := claudeNativeHelperOAuthAuth("")
			headers := claudeNativeHelperHeaders("claude-code-20250219,interleaved-thinking-2025-05-14,message-threads-2026-08-12,advanced-tool-use-2025-11-20,effort-2025-11-24,advisor-tool-2026-03-01", "gzip, deflate, br, zstd")
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers}
			for turn := range 2 {
				doc := ojson.NewDocString(`{"model":"claude-opus-5","max_tokens":2048,"thread":{"type":"create"},"messages":[{"role":"user","content":"hello"}]}`)
				if errSet := doc.Set("betas", []string{"context-1m-2025-08-07"}); errSet != nil {
					t.Fatal(errSet)
				}
				if errSet := doc.Set("metadata.user_id", claudeNativeHelperUserID); errSet != nil {
					t.Fatal(errSet)
				}
				if turn == 1 {
					if errSet := doc.Set("thread", map[string]string{"type": "continue", "previous_message_id": "msg_thread_1"}); errSet != nil {
						t.Fatal(errSet)
					}
					if errSet := doc.Set("thinking.type", "disabled"); errSet != nil {
						t.Fatal(errSet)
					}
				}
				request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: doc.Bytes()}
				// A new executor on each turn models a restart with no cached state.
				executor := NewClaudeExecutor(&config.Config{})
				if stream {
					result, errStream := executor.ExecuteStream(ctx, auth, request, opts)
					if errStream != nil {
						t.Fatalf("turn %d: %v", turn+1, errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
					t.Fatalf("turn %d: %v", turn+1, errExecute)
				}
			}
			if len(sent) != 2 {
				t.Fatalf("upstream requests = %d, want 2", len(sent))
			}
		})
	}
}

func TestClaudeThreadSnapshotCannotUndoUncloak(t *testing.T) {
	calls := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return claudeThreadTestResponse(req, http.StatusOK, "application/json", claudeThreadTestMessage("msg_thread_1")), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-thread-uncloak", "cloak_mode": "always"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"messages":[{"role":"user","content":"first"}]}`)}
	if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
		t.Fatal(errExecute)
	}
	auth.Attributes["cloak_mode"] = "uncloak"
	request.Payload = []byte(`{"model":"claude-opus-5","thread":{"type":"continue","previous_message_id":"msg_thread_1"},"messages":[{"role":"user","content":"second"}]}`)
	if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute == nil {
		t.Fatal("continuation restored a cloaked profile after explicit uncloak; want a Thread replay error")
	}
	if calls != 1 {
		t.Fatalf("sent %d requests, want no send after incompatible identity policy change", calls)
	}
}

type claudeThreadGatedReader struct {
	prefix  *strings.Reader
	suffix  *strings.Reader
	waiting chan struct{}
	release chan struct{}
	paused  bool
}

func (r *claudeThreadGatedReader) Read(p []byte) (int, error) {
	if r.prefix.Len() > 0 {
		return r.prefix.Read(p)
	}
	if !r.paused {
		r.paused = true
		close(r.waiting)
		<-r.release
	}
	return r.suffix.Read(p)
}

func claudeThreadTestTerminal(payload []byte) bool {
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") && !strings.HasPrefix(line, "{") {
			continue
		}
		root := ojson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		switch root.Get("type").String() {
		case "message_delta":
			return root.Get("delta.stop_reason").String() != ""
		case "message_stop", "response.completed", "response.incomplete":
			return true
		}
		if value := root.Get("choices.0.finish_reason"); value.Exists() && value.Type != ojson.Null {
			return true
		}
	}
	return false
}

func claudeThreadTestResponse(req *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func claudeThreadTestMessage(id string) string {
	return fmt.Sprintf(`{"id":%q,"type":"message","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`, id)
}

func claudeThreadTestSSE(id string) string {
	return fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":%q,\"type\":\"message\",\"model\":\"claude-opus-5\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", id)
}
