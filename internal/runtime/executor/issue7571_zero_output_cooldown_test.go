package executor

import (
	"context"
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

func assertIssue7571NoCredentialCooldown(t *testing.T, manager *cliproxyauth.Manager, auth *cliproxyauth.Auth, provider, model string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected HTTP 502 error, got nil")
	}
	statusGetter, okStatus := err.(interface{ StatusCode() int })
	if !okStatus || statusGetter.StatusCode() != http.StatusBadGateway {
		t.Fatalf("err = %T (%v), want StatusCode()=502", err, err)
	}
	codeGetter, okCode := err.(interface{ ErrorCode() string })
	if !okCode || codeGetter.ErrorCode() != cliproxyauth.ErrorCodeZeroOutput {
		gotCode := ""
		if okCode {
			gotCode = codeGetter.ErrorCode()
		}
		t.Fatalf("ErrorCode() = %q (err=%T %v), want %q", gotCode, err, err, cliproxyauth.ErrorCodeZeroOutput)
	}

	rerr := cliproxyauth.ResultErrorFromError(err)
	if rerr == nil || rerr.Code != cliproxyauth.ErrorCodeZeroOutput || rerr.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("ResultErrorFromError() = %+v, want code=%q status=502", rerr, cliproxyauth.ErrorCodeZeroOutput)
	}

	manager.MarkResult(context.Background(), cliproxyauth.Result{
		AuthID:   auth.ID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    rerr,
	})

	stored, okAuth := manager.GetByID(auth.ID)
	if !okAuth || stored == nil {
		t.Fatalf("GetByID(%q) missing after MarkResult", auth.ID)
	}
	if stored.Unavailable || !stored.NextRetryAfter.IsZero() {
		t.Fatalf("credential %q cooled down after zero-output 502: Unavailable=%v NextRetryAfter=%v", auth.ID, stored.Unavailable, stored.NextRetryAfter)
	}
	if st := stored.ModelStates[model]; st != nil && (st.Unavailable || !st.NextRetryAfter.IsZero()) {
		t.Fatalf("model %q on credential %q cooled down after zero-output 502: Unavailable=%v NextRetryAfter=%v", model, auth.ID, st.Unavailable, st.NextRetryAfter)
	}
}

func TestIssue7571_GeminiFamilyZeroOutputCarriesZeroOutputAndSkipsCooldown(t *testing.T) {
	const model = "gemini-4-hydrogen"

	agBody := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"c2ln","text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11},"modelVersion":"gemini-4-hydrogen"}}`
	plainBody := `{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"c2ln","text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11},"modelVersion":"gemini-4-hydrogen"}`
	sigOnlyFrame := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"thoughtSignature":"c2ln","text":""}]}}],"modelVersion":"gemini-4-hydrogen"}}`
	thoughtFrame := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking draft"}]}}],"modelVersion":"gemini-4-hydrogen"}}`

	agServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			switch r.Header.Get("X-Test-Case") {
			case "sig-only-eof":
				_, _ = io.WriteString(w, "data: "+sigOnlyFrame+"\n\n")
			case "post-thinking":
				_, _ = io.WriteString(w, "data: "+thoughtFrame+"\n\ndata: "+agBody+"\n\n")
			default:
				_, _ = io.WriteString(w, "data: "+agBody+"\n\n")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, agBody)
	}))
	defer agServer.Close()

	plainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+plainBody+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, plainBody)
	}))
	defer plainServer.Close()

	cfg := &config.Config{DisableCooling: false}

	t.Run("antigravity_nonstream_and_stream_variants", func(t *testing.T) {
		manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
		manager.SetConfig(cfg)
		exec := NewAntigravityExecutor(cfg)
		manager.RegisterExecutor(exec)

		auth := &cliproxyauth.Auth{
			ID:       "ag-7571-unit",
			Provider: "antigravity",
			Status:   cliproxyauth.StatusActive,
			Metadata: map[string]any{
				"access_token": "tok-ag",
				"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"project_id":   "proj-ag",
			},
			Attributes: map[string]string{"base_url": agServer.URL},
		}
		if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

		req := cliproxyexecutor.Request{
			Model:   model,
			Payload: issue7513RequestPayload(sdktranslator.FormatGemini, model, false),
		}
		_, errNonStream := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		assertIssue7571NoCredentialCooldown(t, manager, auth, "antigravity", model, errNonStream)

		for _, mode := range []string{"malformed", "sig-only-eof", "post-thinking"} {
			headers := make(http.Header)
			headers.Set("X-Test-Case", mode)
			streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: issue7513RequestPayload(sdktranslator.FormatGemini, model, true),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
				Stream:         true,
				Headers:        headers,
			})
			var chunkErr error
			if errStream != nil {
				chunkErr = errStream
			} else {
				for chunk := range streamRes.Chunks {
					if chunk.Err != nil {
						chunkErr = chunk.Err
					}
				}
			}
			assertIssue7571NoCredentialCooldown(t, manager, auth, "antigravity", model, chunkErr)
		}
	})

	t.Run("gemini_and_vertex_nonstream_and_stream", func(t *testing.T) {
		for _, tc := range []struct {
			provider string
			exec     cliproxyauth.ProviderExecutor
			auth     *cliproxyauth.Auth
		}{
			{
				provider: "gemini",
				exec:     NewGeminiExecutor(cfg),
				auth: &cliproxyauth.Auth{
					ID:         "gem-7571-unit",
					Provider:   "gemini",
					Status:     cliproxyauth.StatusActive,
					Attributes: map[string]string{"api_key": "gem-key", "base_url": plainServer.URL},
				},
			},
			{
				provider: "vertex",
				exec:     NewGeminiVertexExecutor(cfg),
				auth: &cliproxyauth.Auth{
					ID:         "vtx-7571-unit",
					Provider:   "vertex",
					Status:     cliproxyauth.StatusActive,
					Attributes: map[string]string{"api_key": "vtx-key", "base_url": plainServer.URL},
				},
			},
		} {
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(tc.exec)
			if _, errReg := manager.Register(context.Background(), tc.auth); errReg != nil {
				t.Fatalf("Register(%s) error = %v", tc.provider, errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(tc.auth.ID, tc.provider, []*registry.ModelInfo{{ID: model}})
			authID := tc.auth.ID
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

			_, errNonStream := tc.exec.Execute(context.Background(), tc.auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: issue7513RequestPayload(sdktranslator.FormatGemini, model, false),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
			})
			assertIssue7571NoCredentialCooldown(t, manager, tc.auth, tc.provider, model, errNonStream)

			streamRes, errStream := tc.exec.ExecuteStream(context.Background(), tc.auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: issue7513RequestPayload(sdktranslator.FormatGemini, model, true),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
				Stream:         true,
			})
			var chunkErr error
			if errStream != nil {
				chunkErr = errStream
			} else {
				for chunk := range streamRes.Chunks {
					if chunk.Err != nil {
						chunkErr = chunk.Err
					}
				}
			}
			assertIssue7571NoCredentialCooldown(t, manager, tc.auth, tc.provider, model, chunkErr)
		}
	})

	t.Run("aistudio_nonstream_and_stream", func(t *testing.T) {
		sessionID := "aistudio-7571-unit"
		connected := make(chan struct{})
		relay := wsrelay.NewManager(wsrelay.Options{
			ProviderFactory: func(*http.Request) (string, error) { return sessionID, nil },
			OnConnected:     func(string) { close(connected) },
		})
		relaySrv := httptest.NewServer(relay.Handler())
		defer relaySrv.Close()
		defer func() {
			if errStop := relay.Stop(context.Background()); errStop != nil {
				t.Error(errStop)
			}
		}()

		conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(relaySrv.URL, "http")+relay.Path(), nil)
		if errDial != nil {
			t.Fatal(errDial)
		}
		defer func() {
			_ = conn.Close()
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
							"body":    plainBody,
						},
					}}
				} else {
					responses = []wsrelay.Message{
						{ID: msg.ID, Type: wsrelay.MessageTypeStreamStart, Payload: map[string]any{"status": 200}},
						{ID: msg.ID, Type: wsrelay.MessageTypeStreamChunk, Payload: map[string]any{"data": "data: " + plainBody + "\n\n"}},
						{ID: msg.ID, Type: wsrelay.MessageTypeStreamEnd},
					}
				}
				for _, resp := range responses {
					if errWrite := conn.WriteJSON(resp); errWrite != nil {
						clientDone <- errWrite
						return
					}
				}
			}
			clientDone <- nil
		}()

		manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
		manager.SetConfig(cfg)
		exec := NewAIStudioExecutor(cfg, sessionID, relay)
		manager.RegisterExecutor(exec)
		auth := &cliproxyauth.Auth{
			ID:       sessionID,
			Provider: "aistudio",
			Status:   cliproxyauth.StatusActive,
		}
		if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register(aistudio) error = %v", errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

		_, errNonStream := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   model,
			Payload: issue7513RequestPayload(sdktranslator.FormatGemini, model, false),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
		})
		assertIssue7571NoCredentialCooldown(t, manager, auth, "aistudio", model, errNonStream)

		streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   model,
			Payload: issue7513RequestPayload(sdktranslator.FormatGemini, model, true),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
			Stream:         true,
		})
		var chunkErr error
		if errStream != nil {
			chunkErr = errStream
		} else {
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					chunkErr = chunk.Err
				}
			}
		}
		assertIssue7571NoCredentialCooldown(t, manager, auth, "aistudio", model, chunkErr)
		if errClient := <-clientDone; errClient != nil {
			t.Fatalf("relay client error = %v", errClient)
		}
	})
}
