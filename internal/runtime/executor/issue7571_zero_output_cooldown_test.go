package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func assertZeroOutputTransientTransportError(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected non-nil 502 error", label)
	}
	var sc interface{ StatusCode() int }
	if !errors.As(err, &sc) || sc.StatusCode() != http.StatusBadGateway {
		t.Fatalf("%s: StatusCode() = %v (err=%T %v), want 502", label, sc, err, err)
	}
	var ec interface{ ErrorCode() string }
	if !errors.As(err, &ec) || ec.ErrorCode() != cliproxyauth.ErrorCodeTransientTransport {
		t.Fatalf("%s: ErrorCode() = %q (err=%T %v), want %q", label, func() string {
			if ec == nil {
				return ""
			}
			return ec.ErrorCode()
		}(), err, err, cliproxyauth.ErrorCodeTransientTransport)
	}
}

func TestIssue7571_GeminiFamilyZeroOutputCarriesTransientTransportAndSkipsCooldown(t *testing.T) {
	t.Run("antigravity_nonstream_stream_claude_sigonly_postthought", func(t *testing.T) {
		malformedBody := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-only","text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: bash"}],"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":0,"thoughtsTokenCount":10,"totalTokenCount":130},"modelVersion":"gemini-4-hydrogen"}}`
		sigOnlyBody := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-only","text":""}]}}],"usageMetadata":{"promptTokenCount":120,"totalTokenCount":120},"modelVersion":"gemini-4-hydrogen"}}`
		postThoughtChunk := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking"}]}}],"modelVersion":"gemini-4-hydrogen"}}`

		mode := "malformed"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "streamGenerateContent") {
				w.Header().Set("Content-Type", "text/event-stream")
				switch mode {
				case "sigonly":
					_, _ = io.WriteString(w, "data: "+sigOnlyBody+"\n\n")
				case "post_thought":
					_, _ = io.WriteString(w, "data: "+postThoughtChunk+"\n\ndata: "+malformedBody+"\n\n")
				default:
					_, _ = io.WriteString(w, "data: "+malformedBody+"\n\n")
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, malformedBody)
		}))
		defer server.Close()

		exec := NewAntigravityExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			ID:       "ag-issue7571-unit",
			Provider: "antigravity",
			Metadata: map[string]any{
				"access_token": "token-ag",
				"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"project_id":   "project-ag",
			},
			Attributes: map[string]string{"base_url": server.URL},
		}

		// 1. Standard non-stream Execute path.
		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-4-hydrogen",
			Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-hydrogen", false),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		assertZeroOutputTransientTransportError(t, "antigravity.Execute", errExec)

		// 2. Claude-family non-stream executeClaudeNonStream path (gemini-3-pro + FormatClaude).
		_, errClaudeExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3-pro",
			Payload: issue7513RequestPayload(sdktranslator.FormatClaude, "gemini-3-pro", false),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatClaude,
			ResponseFormat: sdktranslator.FormatClaude,
		})
		assertZeroOutputTransientTransportError(t, "antigravity.executeClaudeNonStream", errClaudeExec)

		// 3. Stream MALFORMED_FUNCTION_CALL, signature-only EOF, and post-thinking MALFORMED_FUNCTION_CALL.
		for _, streamCase := range []struct {
			name   string
			mode   string
			format sdktranslator.Format
		}{
			{name: "stream_malformed", mode: "malformed", format: sdktranslator.FormatGemini},
			{name: "stream_sigonly_eof", mode: "sigonly", format: sdktranslator.FormatOpenAI},
			{name: "stream_post_thought_claude", mode: "post_thought", format: sdktranslator.FormatClaude},
		} {
			mode = streamCase.mode
			streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gemini-4-hydrogen",
				Payload: issue7513RequestPayload(streamCase.format, "gemini-4-hydrogen", true),
			}, cliproxyexecutor.Options{
				SourceFormat:   streamCase.format,
				ResponseFormat: streamCase.format,
				Stream:         true,
			})
			if errStream != nil {
				t.Fatalf("%s: ExecuteStream() error = %v", streamCase.name, errStream)
			}
			var chunkErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					chunkErr = chunk.Err
				}
			}
			assertZeroOutputTransientTransportError(t, "antigravity."+streamCase.name, chunkErr)
		}
	})

	for _, provider := range []string{"gemini", "vertex_api_key", "vertex_service_account"} {
		t.Run(provider, func(t *testing.T) {
			malformedBody := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"totalTokenCount":50},"modelVersion":"gemini-4-argon"}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+malformedBody+"\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, malformedBody)
			}))
			defer server.Close()

			ctx, exec, auth := issue7513GeminiOrVertexExecutor(t, provider, server.URL, "issue7571-"+provider)

			_, errExec := exec.Execute(ctx, auth, cliproxyexecutor.Request{
				Model:   "gemini-4-argon",
				Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-argon", false),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
			})
			assertZeroOutputTransientTransportError(t, provider+".Execute", errExec)

			streamRes, errStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gemini-4-argon",
				Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-argon", true),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
				Stream:         true,
			})
			if errStream != nil {
				t.Fatalf("%s.ExecuteStream() error = %v", provider, errStream)
			}
			var chunkErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					chunkErr = chunk.Err
				}
			}
			assertZeroOutputTransientTransportError(t, provider+".ExecuteStream", chunkErr)
		})
	}

	t.Run("aistudio", func(t *testing.T) {
		malformedBody := `{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"totalTokenCount":50},"modelVersion":"gemini-4-argon"}`
		authID := "issue7571-aistudio"
		connected := make(chan struct{})
		relay := wsrelay.NewManager(wsrelay.Options{
			ProviderFactory: func(*http.Request) (string, error) { return authID, nil },
			OnConnected:     func(string) { close(connected) },
		})
		server := httptest.NewServer(relay.Handler())
		defer server.Close()
		defer func() {
			if errStop := relay.Stop(context.Background()); errStop != nil {
				t.Error(errStop)
			}
		}()

		conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
		if errDial != nil {
			t.Fatal(errDial)
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Error(errClose)
			}
		}()
		<-connected

		clientDone := make(chan error, 1)
		go func() {
			for i := 0; i < 2; i++ {
				var msg wsrelay.Message
				if errRead := conn.ReadJSON(&msg); errRead != nil {
					clientDone <- errRead
					return
				}
				var responses []wsrelay.Message
				if i == 0 {
					responses = []wsrelay.Message{{
						ID:   msg.ID,
						Type: wsrelay.MessageTypeHTTPResp,
						Payload: map[string]any{
							"status":  float64(http.StatusOK),
							"headers": map[string]any{"Content-Type": "application/json"},
							"body":    malformedBody,
						},
					}}
				} else {
					responses = []wsrelay.Message{
						{ID: msg.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}},
						{ID: msg.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": "data: " + malformedBody + "\n\n"}},
						{ID: msg.ID, Type: wsrelay.MessageTypeStreamEnd},
					}
				}
				for _, r := range responses {
					if errWrite := conn.WriteJSON(r); errWrite != nil {
						clientDone <- errWrite
						return
					}
				}
			}
			clientDone <- nil
		}()

		exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
		auth := &cliproxyauth.Auth{ID: authID, Provider: "aistudio"}

		_, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-4-argon",
			Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-argon", false),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		assertZeroOutputTransientTransportError(t, "aistudio.Execute", errExec)

		streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-4-argon",
			Payload: issue7513RequestPayload(sdktranslator.FormatGemini, "gemini-4-argon", true),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
			Stream:         true,
		})
		if errStream != nil {
			t.Fatalf("aistudio.ExecuteStream() error = %v", errStream)
		}
		var chunkErr error
		for chunk := range streamRes.Chunks {
			if chunk.Err != nil {
				chunkErr = chunk.Err
			}
		}
		assertZeroOutputTransientTransportError(t, "aistudio.ExecuteStream", chunkErr)

		if errClient := <-clientDone; errClient != nil {
			t.Fatal(errClient)
		}
	})

	t.Run("manager_execute_does_not_cool_credentials", func(t *testing.T) {
		malformedBody := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-only","text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"totalTokenCount":50},"modelVersion":"gemini-4-hydrogen"}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, malformedBody)
		}))
		defer server.Close()

		cfg := &config.Config{}
		manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
		manager.RegisterExecutor(NewAntigravityExecutor(cfg))

		model := "gemini-4-hydrogen-issue7571-mgr"
		auth := &cliproxyauth.Auth{
			ID:       "ag-issue7571-mgr",
			Provider: "antigravity",
			Status:   cliproxyauth.StatusActive,
			Metadata: map[string]any{
				"access_token": "token-ag",
				"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"project_id":   "project-ag",
			},
			Attributes: map[string]string{"base_url": server.URL},
		}
		if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		})

		_, errExec := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
			Model:   model,
			Payload: issue7513RequestPayload(sdktranslator.FormatGemini, model, false),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		assertZeroOutputTransientTransportError(t, "manager.Execute", errExec)

		updated, ok := manager.GetByID(auth.ID)
		if !ok || updated == nil {
			t.Fatal("expected auth to remain registered")
		}
		if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
			t.Fatalf("expected auth not to enter cooldown, got unavailable=%v next_retry_after=%v", updated.Unavailable, updated.NextRetryAfter)
		}
		if state := updated.ModelStates[model]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
			t.Fatalf("expected model state not to enter cooldown, got unavailable=%v next_retry_after=%v", state.Unavailable, state.NextRetryAfter)
		}
	})

	t.Run("translated_claude_error_status_classification", func(t *testing.T) {
		malformedErr := newGeminiTranslatedClaudeErrorStatusErr(`{"type":"error","error":{"type":"api_error","message":"Gemini response finished with MALFORMED_FUNCTION_CALL"}}`)
		assertZeroOutputTransientTransportError(t, "malformed_claude_payload", malformedErr)

		otherErr := newGeminiTranslatedClaudeErrorStatusErr(`{"type":"error","error":{"type":"api_error","message":"unrelated upstream error"}}`)
		if otherErr.StatusCode() != http.StatusBadGateway {
			t.Fatalf("otherErr.StatusCode() = %d, want 502", otherErr.StatusCode())
		}
		if otherErr.ErrorCode() != "" {
			t.Fatalf("otherErr.ErrorCode() = %q, want empty (must remain coolable)", otherErr.ErrorCode())
		}
	})

	t.Run("grounding_chunks_only_claude_translated_malformed", func(t *testing.T) {
		geminiGroundingMalformed := `{"candidates":[{"content":{"role":"model","parts":[]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"totalTokenCount":50},"modelVersion":"gemini-4-argon"}`
		antigravityGroundingMalformed := `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":50,"totalTokenCount":50},"modelVersion":"gemini-4-hydrogen"}}`

		t.Run("antigravity", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "streamGenerateContent") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+antigravityGroundingMalformed+"\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, antigravityGroundingMalformed)
			}))
			defer server.Close()

			exec := NewAntigravityExecutor(&config.Config{})
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.RegisterExecutor(exec)

			model := "gemini-4-hydrogen"
			auth := &cliproxyauth.Auth{
				ID:       "ag-issue7571-grounding",
				Provider: "antigravity",
				Status:   cliproxyauth.StatusActive,
				Metadata: map[string]any{
					"access_token": "token-ag",
					"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
					"project_id":   "project-ag",
				},
				Attributes: map[string]string{"base_url": server.URL},
			}
			if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register() error = %v", errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}, {ID: "gemini-3-pro"}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})

			// Non-stream via manager (standard + Claude-family executeClaudeNonStream).
			for _, m := range []string{model, "gemini-3-pro"} {
				_, errExec := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
					Model:   m,
					Payload: issue7513RequestPayload(sdktranslator.FormatClaude, m, false),
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatClaude,
					ResponseFormat: sdktranslator.FormatClaude,
				})
				assertZeroOutputTransientTransportError(t, "antigravity.grounding.Execute("+m+")", errExec)
			}

			// Stream via manager.
			streamRes, errStream := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{
				Model:   model,
				Payload: issue7513RequestPayload(sdktranslator.FormatClaude, model, true),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errStream != nil {
				t.Fatalf("antigravity.grounding.ExecuteStream() error = %v", errStream)
			}
			var chunkErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					chunkErr = chunk.Err
				}
			}
			assertZeroOutputTransientTransportError(t, "antigravity.grounding.ExecuteStream", chunkErr)

			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
				t.Fatalf("groundingChunks-only malformed cooled antigravity auth: %+v", updated)
			}
		})

		for _, provider := range []string{"gemini", "vertex_api_key", "vertex_service_account"} {
			t.Run(provider, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
						return
					}
					if strings.Contains(r.URL.Path, "streamGenerateContent") {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+geminiGroundingMalformed+"\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, geminiGroundingMalformed)
				}))
				defer server.Close()

				ctx, exec, auth := issue7513GeminiOrVertexExecutor(t, provider, server.URL, "issue7571-grounding-"+provider)
				manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
				manager.RegisterExecutor(exec)
				auth.Status = cliproxyauth.StatusActive
				if _, errReg := manager.Register(ctx, auth); errReg != nil {
					t.Fatalf("Register() error = %v", errReg)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gemini-4-argon"}})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient(auth.ID)
				})

				_, errExec := manager.Execute(ctx, []string{auth.Provider}, cliproxyexecutor.Request{
					Model:   "gemini-4-argon",
					Payload: issue7513RequestPayload(sdktranslator.FormatClaude, "gemini-4-argon", false),
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatClaude,
					ResponseFormat: sdktranslator.FormatClaude,
				})
				assertZeroOutputTransientTransportError(t, provider+".grounding.Execute", errExec)

				streamRes, errStream := manager.ExecuteStream(ctx, []string{auth.Provider}, cliproxyexecutor.Request{
					Model:   "gemini-4-argon",
					Payload: issue7513RequestPayload(sdktranslator.FormatClaude, "gemini-4-argon", true),
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatClaude,
					ResponseFormat: sdktranslator.FormatClaude,
					Stream:         true,
				})
				if errStream != nil {
					t.Fatalf("%s.grounding.ExecuteStream() error = %v", provider, errStream)
				}
				var chunkErr error
				for chunk := range streamRes.Chunks {
					if chunk.Err != nil {
						chunkErr = chunk.Err
					}
				}
				assertZeroOutputTransientTransportError(t, provider+".grounding.ExecuteStream", chunkErr)

				updated, ok := manager.GetByID(auth.ID)
				if !ok || updated == nil || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
					t.Fatalf("groundingChunks-only malformed cooled %s auth: %+v", provider, updated)
				}
			})
		}

		t.Run("aistudio", func(t *testing.T) {
			authID := "issue7571-grounding-aistudio"
			connected := make(chan struct{})
			relay := wsrelay.NewManager(wsrelay.Options{
				ProviderFactory: func(*http.Request) (string, error) { return authID, nil },
				OnConnected:     func(string) { close(connected) },
			})
			server := httptest.NewServer(relay.Handler())
			defer server.Close()
			defer func() {
				if errStop := relay.Stop(context.Background()); errStop != nil {
					t.Error(errStop)
				}
			}()

			conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+relay.Path(), nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
			<-connected

			clientDone := make(chan error, 1)
			go func() {
				for i := 0; i < 2; i++ {
					var msg wsrelay.Message
					if errRead := conn.ReadJSON(&msg); errRead != nil {
						clientDone <- errRead
						return
					}
					var responses []wsrelay.Message
					if i == 0 {
						responses = []wsrelay.Message{{
							ID:   msg.ID,
							Type: wsrelay.MessageTypeHTTPResp,
							Payload: map[string]any{
								"status":  float64(http.StatusOK),
								"headers": map[string]any{"Content-Type": "application/json"},
								"body":    geminiGroundingMalformed,
							},
						}}
					} else {
						responses = []wsrelay.Message{
							{ID: msg.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}},
							{ID: msg.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": "data: " + geminiGroundingMalformed + "\n\n"}},
							{ID: msg.ID, Type: wsrelay.MessageTypeStreamEnd},
						}
					}
					for _, r := range responses {
						if errWrite := conn.WriteJSON(r); errWrite != nil {
							clientDone <- errWrite
							return
						}
					}
				}
				clientDone <- nil
			}()

			exec := NewAIStudioExecutor(&config.Config{}, "aistudio", relay)
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.RegisterExecutor(exec)
			auth := &cliproxyauth.Auth{ID: authID, Provider: "aistudio", Status: cliproxyauth.StatusActive}
			if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
				t.Fatalf("Register() error = %v", errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "gemini-4-argon"}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(auth.ID)
			})

			_, errExec := manager.Execute(context.Background(), []string{"aistudio"}, cliproxyexecutor.Request{
				Model:   "gemini-4-argon",
				Payload: issue7513RequestPayload(sdktranslator.FormatClaude, "gemini-4-argon", false),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
			})
			assertZeroOutputTransientTransportError(t, "aistudio.grounding.Execute", errExec)

			streamRes, errStream := manager.ExecuteStream(context.Background(), []string{"aistudio"}, cliproxyexecutor.Request{
				Model:   "gemini-4-argon",
				Payload: issue7513RequestPayload(sdktranslator.FormatClaude, "gemini-4-argon", true),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errStream != nil {
				t.Fatalf("aistudio.grounding.ExecuteStream() error = %v", errStream)
			}
			var chunkErr error
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					chunkErr = chunk.Err
				}
			}
			assertZeroOutputTransientTransportError(t, "aistudio.grounding.ExecuteStream", chunkErr)

			if errClient := <-clientDone; errClient != nil {
				t.Fatal(errClient)
			}
			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
				t.Fatalf("groundingChunks-only malformed cooled aistudio auth: %+v", updated)
			}
		})
	})
}
