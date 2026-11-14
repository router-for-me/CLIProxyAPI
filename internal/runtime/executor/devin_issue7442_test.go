package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"google.golang.org/protobuf/encoding/protowire"
)

type issue7442UsageCapture struct {
	prefix  string
	records chan coreusage.Record
}

func (c *issue7442UsageCapture) HandleUsage(_ context.Context, record coreusage.Record) {
	if strings.HasPrefix(record.AuthID, c.prefix) {
		select {
		case c.records <- record:
		default:
		}
	}
}

func buildIssue7442DevinUsageFrame(promptTokens, completionTokens, cachedTokens, cacheWriteTokens uint64) []byte {
	var f7 []byte
	f7 = protowire.AppendTag(f7, 2, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, promptTokens)
	f7 = protowire.AppendTag(f7, 3, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, completionTokens)
	if cacheWriteTokens > 0 {
		f7 = protowire.AppendTag(f7, 4, protowire.VarintType)
		f7 = protowire.AppendVarint(f7, cacheWriteTokens)
	}
	f7 = protowire.AppendTag(f7, 5, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, cachedTokens)

	var frame []byte
	frame = protowire.AppendTag(frame, 7, protowire.BytesType)
	frame = protowire.AppendBytes(frame, f7)
	return helps.WrapConnectEnvelope(frame)
}

func buildIssue7442DevinTextStream(text string, promptTokens, completionTokens uint64) []byte {
	var textFrame []byte
	textFrame = protowire.AppendTag(textFrame, 3, protowire.BytesType)
	textFrame = protowire.AppendString(textFrame, text)

	var buf bytes.Buffer
	buf.Write(helps.WrapConnectEnvelope(textFrame))
	buf.Write(buildIssue7442DevinUsageFrame(promptTokens, completionTokens, 0, 0))
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
	return buf.Bytes()
}

func TestIssue7442_DevinEmptyCompletionFailsOverWithoutCredentialCooldown(t *testing.T) {
	var metadataFrame []byte
	metadataFrame = protowire.AppendTag(metadataFrame, 1, protowire.BytesType)
	metadataFrame = protowire.AppendString(metadataFrame, "out_dead_slot")

	cases := []struct {
		name      string
		badFrames []byte
	}{
		{
			name: "zero_usage_clean_eos",
			badFrames: append(
				buildIssue7442DevinUsageFrame(0, 0, 0, 0),
				helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...,
			),
		},
		{
			name: "no_usage_metadata_only_clean_eos",
			badFrames: append(
				helps.WrapConnectEnvelope(metadataFrame),
				helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...,
			),
		},
		{
			name: "prompt_tokens_only_zero_output_clean_eos",
			badFrames: append(
				buildIssue7442DevinUsageFrame(420, 0, 120, 0),
				helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...,
			),
		},
		{
			name:      "premature_eof_before_any_output",
			badFrames: helps.WrapConnectEnvelope(metadataFrame),
		},
	}

	type execMode struct {
		name   string
		stream bool
		format sdktranslator.Format
	}
	modes := []execMode{
		{name: "stream_openai", stream: true, format: sdktranslator.FormatOpenAI},
		{name: "stream_responses", stream: true, format: sdktranslator.FormatOpenAIResponse},
		{name: "stream_interactions", stream: true, format: sdktranslator.FormatInteractions},
		{name: "stream_claude", stream: true, format: sdktranslator.FormatClaude},
		{name: "stream_gemini", stream: true, format: sdktranslator.FormatGemini},
		{name: "nonstream_openai", stream: false, format: sdktranslator.FormatOpenAI},
		{name: "nonstream_responses", stream: false, format: sdktranslator.FormatOpenAIResponse},
		{name: "nonstream_interactions", stream: false, format: sdktranslator.FormatInteractions},
		{name: "nonstream_claude", stream: false, format: sdktranslator.FormatClaude},
		{name: "nonstream_gemini", stream: false, format: sdktranslator.FormatGemini},
	}
	for _, tc := range cases {
		for _, em := range modes {
			stream := em.stream
			clientFormat := em.format
			t.Run(tc.name+"/"+em.name, func(t *testing.T) {
				prefix := "issue7442-" + uuid.NewString()
				capture := &issue7442UsageCapture{prefix: prefix, records: make(chan coreusage.Record, 8)}
				coreusage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() {
					coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
				})

				var badHits atomic.Int32
				var goodHits atomic.Int32
				goodFrames := buildIssue7442DevinTextStream("recovered-from-second-credential", 25, 6)

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _, _ = helps.ReadConnectFrame(r.Body)
					w.Header().Set("Content-Type", "application/connect+proto")
					authHeader := r.Header.Get("Authorization")
					if strings.Contains(authHeader, "bad-token") {
						badHits.Add(1)
						_, _ = w.Write(tc.badFrames)
						return
					}
					goodHits.Add(1)
					_, _ = w.Write(goodFrames)
				}))
				defer server.Close()

				cfg := &config.Config{DisableCooling: false}
				manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewDevinExecutor(cfg))

				const model = "devin/swe-2"
				badAuth := &cliproxyauth.Auth{
					ID:       prefix + "-bad",
					Provider: "devin",
					Status:   cliproxyauth.StatusActive,
					Attributes: map[string]string{
						"api_key":  "bad-token",
						"base_url": server.URL,
						"priority": "10",
					},
				}
				goodAuth := &cliproxyauth.Auth{
					ID:       prefix + "-good",
					Provider: "devin",
					Status:   cliproxyauth.StatusActive,
					Attributes: map[string]string{
						"api_key":  "good-token",
						"base_url": server.URL,
						"priority": "5",
					},
				}
				for _, candidate := range []*cliproxyauth.Auth{badAuth, goodAuth} {
					registry.GetGlobalRegistry().RegisterClient(candidate.ID, "devin", []*registry.ModelInfo{{ID: model}})
					id := candidate.ID
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
						t.Fatalf("register %s: %v", candidate.ID, errRegister)
					}
				}

				var rawReq []byte
				switch clientFormat {
				case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions:
					rawReq = []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"input":[{"role":"user","content":"hello"}]}`, model, stream))
				case sdktranslator.FormatClaude:
					rawReq = []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, model, stream))
				case sdktranslator.FormatGemini:
					rawReq = []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
				default:
					rawReq = []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, model, stream))
				}
				req := cliproxyexecutor.Request{Model: model, Payload: rawReq}
				opts := cliproxyexecutor.Options{
					Stream:          stream,
					SourceFormat:    clientFormat,
					ResponseFormat:  clientFormat,
					OriginalRequest: rawReq,
				}

				ctx := context.Background()
				var combinedOut strings.Builder
				if stream {
					streamRes, errStream := manager.ExecuteStream(ctx, []string{"devin"}, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream failed: %v", errStream)
					}
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							t.Fatalf("unexpected chunk error after failover: %v", chunk.Err)
						}
						combinedOut.Write(chunk.Payload)
					}
				} else {
					resp, errExec := manager.Execute(ctx, []string{"devin"}, req, opts)
					if errExec != nil {
						t.Fatalf("Execute failed: %v", errExec)
					}
					combinedOut.Write(resp.Payload)
				}

				if badHits.Load() != 1 || goodHits.Load() != 1 {
					t.Fatalf("upstream hits bad=%d good=%d, want 1 and 1", badHits.Load(), goodHits.Load())
				}
				if !strings.Contains(combinedOut.String(), "recovered-from-second-credential") {
					t.Fatalf("expected failover response text, got: %s", combinedOut.String())
				}

				gotBad, okBad := manager.GetByID(badAuth.ID)
				if !okBad || gotBad == nil {
					t.Fatalf("missing registered auth %s", badAuth.ID)
				}
				now := time.Now()
				if gotBad.Unavailable || !gotBad.NextRetryAfter.IsZero() || !cliproxyauth.IsAuthAvailableForModel(gotBad, model, now) {
					t.Fatalf("dead-slot failure cooled healthy credential %s: unavailable=%v nextRetryAfter=%v modelStates=%+v",
						badAuth.ID, gotBad.Unavailable, gotBad.NextRetryAfter, gotBad.ModelStates)
				}

				sawBadFailureRecord := false
				deadline := time.NewTimer(2 * time.Second)
				defer deadline.Stop()
				for i := 0; i < 2; i++ {
					select {
					case rec := <-capture.records:
						if rec.AuthID == badAuth.ID {
							if !rec.Failed {
								t.Fatalf("bad auth usage record marked Failed=false on dead slot: %+v", rec)
							}
							sawBadFailureRecord = true
						}
					case <-deadline.C:
						t.Fatal("timed out waiting for usage records")
					}
				}
				if !sawBadFailureRecord {
					t.Fatalf("did not observe failure usage record for %s", badAuth.ID)
				}
			})
		}
	}
}

func TestIssue7442_DevinAllDeadCredentialsFailOverToFallbackProviderLayerWithoutCoolingDevinPool(t *testing.T) {
	deadDevinFrames := append(
		buildIssue7442DevinUsageFrame(0, 0, 0, 0),
		helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...,
	)

	for _, stream := range []bool{true, false} {
		mode := "nonstream"
		if stream {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			var devinAttempts atomic.Int32
			devinServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _, _ = helps.ReadConnectFrame(r.Body)
				devinAttempts.Add(1)
				w.Header().Set("Content-Type", "application/connect+proto")
				_, _ = w.Write(deadDevinFrames)
			}))
			defer devinServer.Close()

			var fallbackAttempts atomic.Int32
			fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackAttempts.Add(1)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-fb\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"fallback-layer-ok\"}}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-fb\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":3,\"total_tokens\":11}}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-fb","object":"chat.completion","model":"shared-swe-2","choices":[{"index":0,"message":{"role":"assistant","content":"fallback-layer-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`))
			}))
			defer fallbackServer.Close()

			const routeModel = "shared-swe-2"
			cfg := &config.Config{
				DisableCooling: false,
				OAuthModelAlias: map[string][]config.OAuthModelAlias{
					"devin": {
						{Name: "devin/swe-2", Alias: routeModel},
					},
				},
				OpenAICompatibility: []config.OpenAICompatibility{
					{
						Name:    "fallback-compat",
						BaseURL: fallbackServer.URL,
						APIKeyEntries: []config.OpenAICompatibilityAPIKey{
							{APIKey: "fb-key"},
						},
						Models: []config.OpenAICompatibilityModel{
							{Name: routeModel, Alias: routeModel},
						},
					},
				},
			}

			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewDevinExecutor(cfg))
			manager.RegisterExecutor(NewOpenAICompatExecutor("openai-compatibility", cfg))

			prefix := "issue7442-layer-" + uuid.NewString()
			devinAuth1 := &cliproxyauth.Auth{
				ID:       prefix + "-devin-1",
				Provider: "devin",
				Status:   cliproxyauth.StatusActive,
				Attributes: map[string]string{
					"api_key":  "d1",
					"base_url": devinServer.URL,
					"priority": "20",
				},
			}
			devinAuth2 := &cliproxyauth.Auth{
				ID:       prefix + "-devin-2",
				Provider: "devin",
				Status:   cliproxyauth.StatusActive,
				Attributes: map[string]string{
					"api_key":  "d2",
					"base_url": devinServer.URL,
					"priority": "15",
				},
			}
			fallbackAuth := &cliproxyauth.Auth{
				ID:       prefix + "-fb",
				Provider: "openai-compatibility",
				Status:   cliproxyauth.StatusActive,
				Attributes: map[string]string{
					"api_key":  "fb-key",
					"base_url": fallbackServer.URL,
					"priority": "1",
				},
			}

			for _, candidate := range []*cliproxyauth.Auth{devinAuth1, devinAuth2} {
				registry.GetGlobalRegistry().RegisterClient(candidate.ID, "devin", []*registry.ModelInfo{{ID: "devin/swe-2"}, {ID: routeModel}})
				id := candidate.ID
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
					t.Fatalf("register %s: %v", candidate.ID, errRegister)
				}
			}
			registry.GetGlobalRegistry().RegisterClient(fallbackAuth.ID, "openai-compatibility", []*registry.ModelInfo{{ID: routeModel}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(fallbackAuth.ID) })
			if _, errRegister := manager.Register(context.Background(), fallbackAuth); errRegister != nil {
				t.Fatalf("register fallback: %v", errRegister)
			}

			rawReq := []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, routeModel, stream))
			req := cliproxyexecutor.Request{Model: routeModel, Payload: rawReq}
			opts := cliproxyexecutor.Options{
				Stream:          stream,
				SourceFormat:    sdktranslator.FormatOpenAI,
				ResponseFormat:  sdktranslator.FormatOpenAI,
				OriginalRequest: rawReq,
			}

			var outText strings.Builder
			if stream {
				res, errStream := manager.ExecuteStream(context.Background(), []string{"devin", "openai-compatibility"}, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream failed: %v", errStream)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected chunk error: %v", chunk.Err)
					}
					outText.Write(chunk.Payload)
				}
			} else {
				resp, errExec := manager.Execute(context.Background(), []string{"devin", "openai-compatibility"}, req, opts)
				if errExec != nil {
					t.Fatalf("Execute failed: %v", errExec)
				}
				outText.Write(resp.Payload)
			}

			if devinAttempts.Load() != 2 || fallbackAttempts.Load() != 1 {
				t.Fatalf("attempts devin=%d fallback=%d, want 2 and 1", devinAttempts.Load(), fallbackAttempts.Load())
			}
			if !strings.Contains(outText.String(), "fallback-layer-ok") {
				t.Fatalf("expected fallback layer output, got: %s", outText.String())
			}

			now := time.Now()
			for _, id := range []string{devinAuth1.ID, devinAuth2.ID} {
				got, ok := manager.GetByID(id)
				if !ok || got == nil {
					t.Fatalf("missing auth %s", id)
				}
				if got.Unavailable || !got.NextRetryAfter.IsZero() || !cliproxyauth.IsAuthAvailableForModel(got, "devin/swe-2", now) {
					t.Fatalf("Devin credential %s was cooled by dead upstream slot: unavailable=%v nextRetryAfter=%v modelStates=%+v",
						id, got.Unavailable, got.NextRetryAfter, got.ModelStates)
				}
			}
		})
	}
}

func TestIssue7442_DevinDanglingStreamCanceledDoesNotEmitCompletedOrRecordSuccessUsage(t *testing.T) {
	t.Run("before_any_frames", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())

		prefix := "issue7442-cancel-" + uuid.NewString()
		capture := &issue7442UsageCapture{prefix: prefix, records: make(chan coreusage.Record, 4)}
		coreusage.RegisterNamedPlugin(t.Name(), capture)
		t.Cleanup(func() {
			coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
		})

		headersFlushed := make(chan struct{})
		releaseServer := make(chan struct{})

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, _ = helps.ReadConnectFrame(r.Body)
			w.Header().Set("Content-Type", "application/connect+proto")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			close(headersFlushed)
			select {
			case <-r.Context().Done():
			case <-releaseServer:
			}
		}))
		defer close(releaseServer)
		defer server.Close()

		cfg := &config.Config{}
		exec := NewDevinExecutor(cfg)
		auth := &cliproxyauth.Auth{
			ID:       prefix + "-auth",
			Provider: "devin",
			Attributes: map[string]string{
				"api_key":  "dangling-token",
				"base_url": server.URL,
			},
		}

		baseCtx := context.WithValue(context.Background(), "gin", ginCtx)
		ctx, cancel := context.WithCancel(baseCtx)
		defer cancel()

		rawReq := []byte(`{"model":"devin/swe-2","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
		req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: rawReq}
		opts := cliproxyexecutor.Options{
			Stream:          true,
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
			OriginalRequest: rawReq,
		}

		streamRes, errStream := exec.ExecuteStream(ctx, auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream failed: %v", errStream)
		}

		<-headersFlushed
		cancel()

		for chunk := range streamRes.Chunks {
			if len(chunk.Payload) > 0 && strings.Contains(string(chunk.Payload), "finish_reason") {
				t.Fatalf("dangling canceled stream emitted completion chunk: %s", chunk.Payload)
			}
		}

		select {
		case rec := <-capture.records:
			if !rec.Failed {
				t.Fatalf("dangling canceled stream published Failed=false usage record: %+v", rec)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for usage record on canceled dangling stream")
		}

		if value, exists := ginCtx.Get(logging.DeferredAPIResponseContextKey); exists {
			if deferred, ok := value.(logging.DeferredAPIResponse); ok && deferred != nil && deferred.HasPayload() {
				rendered := string(deferred.RenderAPIResponse())
				if strings.Contains(rendered, `"event_type":"interaction.completed"`) || strings.Contains(rendered, `"event_type":"interaction.created"`) {
					t.Fatalf("dangling canceled stream synthesized fake interaction lifecycle in log:\n%s", rendered)
				}
			}
		}
	})

	t.Run("during_downstream_emit_with_partial_usage", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())

		prefix := "issue7442-cancelemit-" + uuid.NewString()
		capture := &issue7442UsageCapture{prefix: prefix, records: make(chan coreusage.Record, 4)}
		coreusage.RegisterNamedPlugin(t.Name(), capture)
		t.Cleanup(func() {
			coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
		})

		var textFrame1, textFrame2 []byte
		textFrame1 = protowire.AppendTag(textFrame1, 3, protowire.BytesType)
		textFrame1 = protowire.AppendString(textFrame1, "chunk-one ")
		textFrame2 = protowire.AppendTag(textFrame2, 3, protowire.BytesType)
		textFrame2 = protowire.AppendString(textFrame2, "chunk-two")

		releaseServer := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, _ = helps.ReadConnectFrame(r.Body)
			w.Header().Set("Content-Type", "application/connect+proto")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(buildIssue7442DevinUsageFrame(42, 7, 0, 0))
			_, _ = w.Write(helps.WrapConnectEnvelope(textFrame1))
			_, _ = w.Write(helps.WrapConnectEnvelope(textFrame2))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
			case <-releaseServer:
			}
		}))
		defer close(releaseServer)
		defer server.Close()

		cfg := &config.Config{}
		exec := NewDevinExecutor(cfg)
		auth := &cliproxyauth.Auth{
			ID:       prefix + "-auth",
			Provider: "devin",
			Attributes: map[string]string{
				"api_key":  "dangling-emit-token",
				"base_url": server.URL,
			},
		}

		baseCtx := context.WithValue(context.Background(), "gin", ginCtx)
		ctx, cancel := context.WithCancel(baseCtx)
		defer cancel()

		rawReq := []byte(`{"model":"devin/swe-2","stream":true,"input":[{"role":"user","content":"hello"}]}`)
		req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: rawReq}
		opts := cliproxyexecutor.Options{
			Stream:          true,
			SourceFormat:    sdktranslator.FormatInteractions,
			ResponseFormat:  sdktranslator.FormatInteractions,
			OriginalRequest: rawReq,
		}

		streamRes, errStream := exec.ExecuteStream(ctx, auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream failed: %v", errStream)
		}

		// Read only the first SSE chunk so streamDevinFramesForAuth blocks inside
		// emitInteractionsEvent on subsequent chunks, then cancel ctx mid-emit.
		firstChunk, ok := <-streamRes.Chunks
		if !ok || len(firstChunk.Payload) == 0 {
			t.Fatalf("expected initial SSE chunk before cancel, got ok=%v chunk=%+v", ok, firstChunk)
		}
		cancel()

		for chunk := range streamRes.Chunks {
			if bytes.Contains(chunk.Payload, []byte(`"event_type":"interaction.completed"`)) {
				t.Fatalf("canceled mid-emit stream synthesized interaction.completed: %s", chunk.Payload)
			}
		}

		select {
		case rec := <-capture.records:
			if !rec.Failed {
				t.Fatalf("mid-emit canceled stream published Failed=false usage record: %+v", rec)
			}
			if rec.Detail.InputTokens != 42 || rec.Detail.OutputTokens != 7 {
				t.Fatalf("mid-emit canceled stream lost partial usage detail: got in=%d out=%d, want 42 and 7",
					rec.Detail.InputTokens, rec.Detail.OutputTokens)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for usage record on mid-emit canceled stream")
		}
	})

	t.Run("during_close_open_steps_after_eos_preserves_cached_usage_and_skips_completed", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())

		prefix := "issue7442-cancelclose-" + uuid.NewString()
		capture := &issue7442UsageCapture{prefix: prefix, records: make(chan coreusage.Record, 4)}
		coreusage.RegisterNamedPlugin(t.Name(), capture)
		t.Cleanup(func() {
			coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
		})

		var thoughtFrame, textFrame []byte
		thoughtFrame = protowire.AppendTag(thoughtFrame, 9, protowire.BytesType)
		thoughtFrame = protowire.AppendString(thoughtFrame, "reasoning-step")
		textFrame = protowire.AppendTag(textFrame, 3, protowire.BytesType)
		textFrame = protowire.AppendString(textFrame, "final-answer")

		eosBody := append([]byte{}, buildIssue7442DevinUsageFrame(31, 6, 10, 4)...)
		eosBody = append(eosBody, helps.WrapConnectEnvelope(thoughtFrame)...)
		eosBody = append(eosBody, helps.WrapConnectEnvelope(textFrame)...)
		eosBody = append(eosBody, helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...)

		serverDone := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, _ = helps.ReadConnectFrame(r.Body)
			w.Header().Set("Content-Type", "application/connect+proto")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(eosBody)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			close(serverDone)
		}))
		defer server.Close()

		cfg := &config.Config{}
		exec := NewDevinExecutor(cfg)
		auth := &cliproxyauth.Auth{
			ID:       prefix + "-auth",
			Provider: "devin",
			Attributes: map[string]string{
				"api_key":  "close-cancel-token",
				"base_url": server.URL,
			},
		}

		baseCtx := context.WithValue(context.Background(), "gin", ginCtx)
		ctx, cancel := context.WithCancel(baseCtx)
		defer cancel()

		rawReq := []byte(`{"model":"devin/swe-2","stream":true,"input":[{"role":"user","content":"hello"}]}`)
		req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: rawReq}
		opts := cliproxyexecutor.Options{
			Stream:          true,
			SourceFormat:    sdktranslator.FormatInteractions,
			ResponseFormat:  sdktranslator.FormatInteractions,
			OriginalRequest: rawReq,
		}

		streamRes, errStream := exec.ExecuteStream(ctx, auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream failed: %v", errStream)
		}

		// Wait until server wrote all frames including EOS, read only the first two
		// chunks (interaction.created + thinking step.start + thinking step.delta so
		// closeOpenSteps blocks emitting pending content after EOS), then cancel and drain.
		<-serverDone
		for i := 0; i < 3; i++ {
			chunk, ok := <-streamRes.Chunks
			if !ok || len(chunk.Payload) == 0 {
				t.Fatalf("expected initial chunk %d before cancel, got ok=%v chunk=%+v", i, ok, chunk)
			}
		}
		cancel()

		for chunk := range streamRes.Chunks {
			if bytes.Contains(chunk.Payload, []byte(`"event_type":"interaction.completed"`)) {
				t.Fatalf("post-EOS canceled closeOpenSteps still emitted interaction.completed: %s", chunk.Payload)
			}
		}

		select {
		case rec := <-capture.records:
			if !rec.Failed {
				t.Fatalf("post-EOS canceled stream published Failed=false usage record: %+v", rec)
			}
			// Total input = 31 uncached + 10 cached + 4 cache_write = 45.
			if rec.Detail.InputTokens != 45 || rec.Detail.OutputTokens != 6 || rec.Detail.CachedTokens != 10 || rec.Detail.CacheCreationTokens != 4 {
				t.Fatalf("post-EOS canceled stream lost cached usage detail: %+v", rec.Detail)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for usage record on post-EOS canceled stream")
		}
	})
}

func TestIssue7442_DevinDeclaredApplyPatchPreservesEmptyCompletionAndQuotaErrorsWhenNoPatchActivity(t *testing.T) {
	for _, badCase := range []struct {
		name      string
		badFrames []byte
	}{
		{
			name: "empty_completion_rotates_instead_of_apply_patch_error",
			badFrames: append(
				buildIssue7442DevinUsageFrame(0, 0, 0, 0),
				helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...,
			),
		},
		{
			name:      "premature_eof_rotates_instead_of_apply_patch_error",
			badFrames: nil,
		},
		{
			name:      "stream_read_error_rotates_instead_of_apply_patch_error",
			badFrames: []byte{0, 0},
		},
	} {
		for _, stream := range []bool{false, true} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(badCase.name+"/"+mode, func(t *testing.T) {
				goodFrames := buildIssue7442DevinTextStream("patch-free-ok", 20, 4)

				var badHits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _, _ = helps.ReadConnectFrame(r.Body)
					w.Header().Set("Content-Type", "application/connect+proto")
					if strings.Contains(r.Header.Get("Authorization"), "bad-patch") {
						badHits.Add(1)
						_, _ = w.Write(badCase.badFrames)
						return
					}
					_, _ = w.Write(goodFrames)
				}))
				defer server.Close()

				cfg := &config.Config{DisableCooling: false}
				manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
				manager.SetConfig(cfg)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewDevinExecutor(cfg))

				const model = "devin/swe-2"
				prefix := "issue7442-patch-" + uuid.NewString()
				badAuth := &cliproxyauth.Auth{
					ID:         prefix + "-bad",
					Provider:   "devin",
					Status:     cliproxyauth.StatusActive,
					Attributes: map[string]string{"api_key": "bad-patch", "base_url": server.URL, "priority": "10"},
				}
				goodAuth := &cliproxyauth.Auth{
					ID:         prefix + "-good",
					Provider:   "devin",
					Status:     cliproxyauth.StatusActive,
					Attributes: map[string]string{"api_key": "good-patch", "base_url": server.URL, "priority": "5"},
				}
				for _, candidate := range []*cliproxyauth.Auth{badAuth, goodAuth} {
					registry.GetGlobalRegistry().RegisterClient(candidate.ID, "devin", []*registry.ModelInfo{{ID: model}})
					id := candidate.ID
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
						t.Fatal(errRegister)
					}
				}

				req := cliproxyexecutor.Request{Model: model, Payload: []byte(task6PatchRequest)}
				opts := cliproxyexecutor.Options{
					Stream:          stream,
					SourceFormat:    sdktranslator.FormatOpenAIResponse,
					ResponseFormat:  sdktranslator.FormatOpenAIResponse,
					OriginalRequest: []byte(task6PatchRequest),
				}

				var out strings.Builder
				if stream {
					res, errStream := manager.ExecuteStream(context.Background(), []string{"devin"}, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream failed: %v", errStream)
					}
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
						}
						out.Write(chunk.Payload)
					}
				} else {
					resp, errExec := manager.Execute(context.Background(), []string{"devin"}, req, opts)
					if errExec != nil {
						t.Fatalf("Execute failed: %v", errExec)
					}
					out.Write(resp.Payload)
				}
				if badHits.Load() != 1 || !strings.Contains(out.String(), "patch-free-ok") {
					t.Fatalf("badHits=%d payload=%s", badHits.Load(), out.String())
				}
				gotBad, _ := manager.GetByID(badAuth.ID)
				if gotBad.Unavailable || !gotBad.NextRetryAfter.IsZero() {
					t.Fatalf("badAuth was cooled on pre-output failure: unavailable=%v nextRetryAfter=%v", gotBad.Unavailable, gotBad.NextRetryAfter)
				}
			})
		}
	}

	for _, stream := range []bool{false, true} {
		mode := "nonstream"
		if stream {
			mode = "stream"
		}
		t.Run("trailer_rate_limit_not_masked_as_apply_patch_error/"+mode, func(t *testing.T) {
			trailer429 := helps.WrapConnectEnvelopeWithFlag(
				helps.ConnectFlagEndStream,
				[]byte(`{"error":{"code":"resource_exhausted","message":"rate limit"}}`),
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _, _ = helps.ReadConnectFrame(r.Body)
				w.Header().Set("Content-Type", "application/connect+proto")
				_, _ = w.Write(trailer429)
			}))
			defer server.Close()

			exec := NewDevinExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				ID:         "issue7442-patch-429-" + mode,
				Provider:   "devin",
				Attributes: map[string]string{"api_key": "tok", "base_url": server.URL},
			}
			req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(task6PatchRequest)}
			opts := cliproxyexecutor.Options{
				Stream:          stream,
				SourceFormat:    sdktranslator.FormatOpenAIResponse,
				ResponseFormat:  sdktranslator.FormatOpenAIResponse,
				OriginalRequest: []byte(task6PatchRequest),
			}

			var gotErr error
			if stream {
				res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					gotErr = errStream
				} else {
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							gotErr = chunk.Err
						} else if len(chunk.Payload) > 0 {
							t.Fatalf("unexpected SSE payload before trailer 429 error: %s", chunk.Payload)
						}
					}
				}
			} else {
				_, gotErr = exec.Execute(context.Background(), auth, req, opts)
			}
			if gotErr == nil {
				t.Fatal("expected 429 error, got nil")
			}
			if strings.Contains(gotErr.Error(), helps.ApplyPatchUpstreamErrorMessage) {
				t.Fatalf("trailer 429 was masked as apply_patch error: %v", gotErr)
			}
			var status cliproxyexecutor.StatusError
			if !errors.As(gotErr, &status) || status.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("status error = %T (%v), want 429", gotErr, gotErr)
			}
		})
	}

	for _, stream := range []bool{false, true} {
		mode := "nonstream"
		if stream {
			mode = "stream"
		}
		t.Run("truncated_active_apply_patch_preserves_stream_truncated_without_replay_or_cooldown/"+mode, func(t *testing.T) {
			truncatedPatchFrames := task6DevinFrames(`{"input":"*** Begin Patch\n*** Add File: a.txt\n+ok\n*** End Patch"}`, false, "")
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _, _ = helps.ReadConnectFrame(r.Body)
				hits.Add(1)
				w.Header().Set("Content-Type", "application/connect+proto")
				_, _ = w.Write(truncatedPatchFrames)
			}))
			defer server.Close()

			cfg := &config.Config{DisableCooling: false}
			manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewDevinExecutor(cfg))

			const model = "devin/swe-2"
			prefix := "issue7442-truncpatch-" + uuid.NewString()
			auth1 := &cliproxyauth.Auth{
				ID:         prefix + "-1",
				Provider:   "devin",
				Status:     cliproxyauth.StatusActive,
				Attributes: map[string]string{"api_key": "tp1", "base_url": server.URL, "priority": "10"},
			}
			auth2 := &cliproxyauth.Auth{
				ID:         prefix + "-2",
				Provider:   "devin",
				Status:     cliproxyauth.StatusActive,
				Attributes: map[string]string{"api_key": "tp2", "base_url": server.URL, "priority": "5"},
			}
			for _, candidate := range []*cliproxyauth.Auth{auth1, auth2} {
				registry.GetGlobalRegistry().RegisterClient(candidate.ID, "devin", []*registry.ModelInfo{{ID: model}})
				id := candidate.ID
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
					t.Fatal(errRegister)
				}
			}

			req := cliproxyexecutor.Request{Model: model, Payload: []byte(task6PatchRequest)}
			opts := cliproxyexecutor.Options{
				Stream:          stream,
				SourceFormat:    sdktranslator.FormatOpenAIResponse,
				ResponseFormat:  sdktranslator.FormatOpenAIResponse,
				OriginalRequest: []byte(task6PatchRequest),
			}

			var gotErr error
			if stream {
				res, errStream := manager.ExecuteStream(context.Background(), []string{"devin"}, req, opts)
				if errStream != nil {
					gotErr = errStream
				} else {
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							gotErr = chunk.Err
						}
					}
				}
			} else {
				_, gotErr = manager.Execute(context.Background(), []string{"devin"}, req, opts)
			}
			assertTask6PatchError(t, gotErr)
			var truncErr cliproxyexecutor.StreamTruncatedError
			if !errors.As(gotErr, &truncErr) || !truncErr.IsStreamTruncated() {
				t.Fatalf("gotErr = %T (%v), want IsStreamTruncated()==true", gotErr, gotErr)
			}
			if hits.Load() != 1 {
				t.Fatalf("hits = %d, want 1 (active apply_patch truncation must not replay)", hits.Load())
			}
			got1, _ := manager.GetByID(auth1.ID)
			if got1.Unavailable || !got1.NextRetryAfter.IsZero() {
				t.Fatalf("truncated apply_patch stream cooled credential %s: unavailable=%v nextRetryAfter=%v",
					auth1.ID, got1.Unavailable, got1.NextRetryAfter)
			}
		})
	}

	for _, patchCase := range []struct {
		name   string
		frames []byte
	}{
		{
			name: "invalid_patch_json_preserves_partial_usage",
			frames: append(
				buildIssue7442DevinUsageFrame(55, 9, 0, 0),
				task6DevinFrames(`{"input":7}`, false, `{}`)...,
			),
		},
		{
			name: "truncated_patch_before_eos_preserves_partial_usage",
			frames: append(
				buildIssue7442DevinUsageFrame(55, 9, 0, 0),
				task6DevinFrames(`{"input":"*** Begin Patch\n*** Add File: a.txt\n+ok\n*** End Patch"}`, false, "")...,
			),
		},
	} {
		for _, stream := range []bool{false, true} {
			mode := "nonstream"
			if stream {
				mode = "stream"
			}
			t.Run(patchCase.name+"/"+mode, func(t *testing.T) {
				prefix := "issue7442-patchusage-" + uuid.NewString()
				capture := &issue7442UsageCapture{prefix: prefix, records: make(chan coreusage.Record, 4)}
				coreusage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() {
					coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
				})

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _, _ = helps.ReadConnectFrame(r.Body)
					w.Header().Set("Content-Type", "application/connect+proto")
					_, _ = w.Write(patchCase.frames)
				}))
				defer server.Close()

				exec := NewDevinExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{
					ID:         prefix + "-auth",
					Provider:   "devin",
					Attributes: map[string]string{"api_key": "pu", "base_url": server.URL},
				}
				req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: []byte(task6PatchRequest)}
				opts := cliproxyexecutor.Options{
					Stream:          stream,
					SourceFormat:    sdktranslator.FormatOpenAIResponse,
					ResponseFormat:  sdktranslator.FormatOpenAIResponse,
					OriginalRequest: []byte(task6PatchRequest),
				}

				if stream {
					res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for range res.Chunks {
					}
				} else {
					_, errExec := exec.Execute(context.Background(), auth, req, opts)
					assertTask6PatchError(t, errExec)
				}

				select {
				case rec := <-capture.records:
					if !rec.Failed {
						t.Fatalf("expected Failed=true usage record, got %+v", rec)
					}
					if rec.Detail.InputTokens != 55 || rec.Detail.OutputTokens != 9 {
						t.Fatalf("patch failure lost partial usage: in=%d out=%d, want 55 and 9",
							rec.Detail.InputTokens, rec.Detail.OutputTokens)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for patch failure usage record")
				}
			})
		}
	}
}

func TestIssue7442_DevinEmptyCompletionExhaustedReturns502AndSupportsRequestScopedStopRule(t *testing.T) {
	deadFrames := append(
		buildIssue7442DevinUsageFrame(0, 0, 0, 0),
		helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...,
	)

	t.Run("exhausted_returns_502_without_cooling", func(t *testing.T) {
		var hits atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, _ = helps.ReadConnectFrame(r.Body)
			hits.Add(1)
			w.Header().Set("Content-Type", "application/connect+proto")
			_, _ = w.Write(deadFrames)
		}))
		defer server.Close()

		cfg := &config.Config{DisableCooling: false}
		manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
		manager.SetConfig(cfg)
		manager.SetRetryConfig(0, 0, 0)
		manager.RegisterExecutor(NewDevinExecutor(cfg))

		const model = "devin/swe-2"
		prefix := "issue7442-exhaust-" + uuid.NewString()
		for i, prio := range []string{"10", "5"} {
			candidate := &cliproxyauth.Auth{
				ID:         fmt.Sprintf("%s-%d", prefix, i),
				Provider:   "devin",
				Status:     cliproxyauth.StatusActive,
				Attributes: map[string]string{"api_key": fmt.Sprintf("k%d", i), "base_url": server.URL, "priority": prio},
			}
			registry.GetGlobalRegistry().RegisterClient(candidate.ID, "devin", []*registry.ModelInfo{{ID: model}})
			id := candidate.ID
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
				t.Fatal(errRegister)
			}
		}

		rawReq := []byte(`{"model":"devin/swe-2","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
		req := cliproxyexecutor.Request{Model: model, Payload: rawReq}
		opts := cliproxyexecutor.Options{
			Stream:          true,
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
			OriginalRequest: rawReq,
		}
		res, errStream := manager.ExecuteStream(context.Background(), []string{"devin"}, req, opts)
		var finalStreamErr error
		if errStream != nil {
			finalStreamErr = errStream
		} else if res != nil {
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					finalStreamErr = chunk.Err
				} else if len(chunk.Payload) > 0 {
					t.Fatalf("expected no SSE payload before 502 bootstrap error, got %q", string(chunk.Payload))
				}
			}
		}
		if finalStreamErr == nil {
			t.Fatal("expected 502 error when all Devin credentials return empty completion, got nil")
		}
		var statusErr cliproxyexecutor.StatusError
		if !errors.As(finalStreamErr, &statusErr) || statusErr.StatusCode() != http.StatusBadGateway {
			t.Fatalf("finalStreamErr = %T (%v), want HTTP 502 StatusError", finalStreamErr, finalStreamErr)
		}
		if hits.Load() != 2 {
			t.Fatalf("hits = %d, want 2", hits.Load())
		}

		// Also verify non-streaming returns HTTP 502 directly when exhausted.
		opts.Stream = false
		_, errExec := manager.Execute(context.Background(), []string{"devin"}, req, opts)
		if !errors.As(errExec, &statusErr) || statusErr.StatusCode() != http.StatusBadGateway {
			t.Fatalf("errExec = %T (%v), want HTTP 502 StatusError", errExec, errExec)
		}
		if hits.Load() != 4 {
			t.Fatalf("hits after non-stream = %d, want 4", hits.Load())
		}
	})

	t.Run("request_scoped_errors_stop_rule_halts_dead_payload_retries", func(t *testing.T) {
		var hits atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _, _ = helps.ReadConnectFrame(r.Body)
			hits.Add(1)
			w.Header().Set("Content-Type", "application/connect+proto")
			_, _ = w.Write(deadFrames)
		}))
		defer server.Close()

		cfg := &config.Config{
			DisableCooling: false,
			OAuthRequestScopedErrors: map[string][]config.RequestScopedErrorRule{
				"devin": {
					{
						Status: http.StatusBadGateway,
						Match:  []string{"empty completion"},
						Action: "stop",
					},
				},
			},
		}
		manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
		manager.SetConfig(cfg)
		manager.SetRetryConfig(0, 0, 0)
		manager.RegisterExecutor(NewDevinExecutor(cfg))

		const model = "devin/swe-2"
		prefix := "issue7442-stoprule-" + uuid.NewString()
		for i, prio := range []string{"10", "5"} {
			candidate := &cliproxyauth.Auth{
				ID:       fmt.Sprintf("%s-%d", prefix, i),
				Provider: "devin",
				Status:   cliproxyauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"base_url":  server.URL,
					"priority":  prio,
				},
				Metadata: map[string]any{
					"session_token": fmt.Sprintf("oauth-tok-%d", i),
				},
			}
			registry.GetGlobalRegistry().RegisterClient(candidate.ID, "devin", []*registry.ModelInfo{{ID: model}})
			id := candidate.ID
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
				t.Fatal(errRegister)
			}
		}

		rawReq := []byte(`{"model":"devin/swe-2","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
		req := cliproxyexecutor.Request{Model: model, Payload: rawReq}
		opts := cliproxyexecutor.Options{
			Stream:          true,
			SourceFormat:    sdktranslator.FormatOpenAI,
			ResponseFormat:  sdktranslator.FormatOpenAI,
			OriginalRequest: rawReq,
		}
		_, errStream := manager.ExecuteStream(context.Background(), []string{"devin"}, req, opts)
		if errStream == nil {
			t.Fatal("expected error from stop rule, got nil")
		}
		if hits.Load() != 1 {
			t.Fatalf("hits = %d, want 1 when oauth-request-scoped-errors action=stop matches empty completion", hits.Load())
		}
	})
}

func TestIssue7442_DevinPartialOutputPrematureEOFIsStreamTruncatedWithoutReplayOrCooldown(t *testing.T) {
	var partialTextFrame []byte
	partialTextFrame = protowire.AppendTag(partialTextFrame, 3, protowire.BytesType)
	partialTextFrame = protowire.AppendString(partialTextFrame, "partial-generated-text")
	partialEOFFrames := helps.WrapConnectEnvelope(partialTextFrame)
	partialReadErrFrames := append(append([]byte{}, partialEOFFrames...), 0, 0)

	var activeFrames atomic.Value
	activeFrames.Store(partialEOFFrames)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, _ = helps.ReadConnectFrame(r.Body)
		hits.Add(1)
		w.Header().Set("Content-Type", "application/connect+proto")
		_, _ = w.Write(activeFrames.Load().([]byte))
	}))
	defer server.Close()

	cfg := &config.Config{DisableCooling: false}
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(NewDevinExecutor(cfg))

	const model = "devin/swe-2"
	prefix := "issue7442-partial-" + uuid.NewString()
	auth1 := &cliproxyauth.Auth{
		ID:         prefix + "-1",
		Provider:   "devin",
		Status:     cliproxyauth.StatusActive,
		Attributes: map[string]string{"api_key": "p1", "base_url": server.URL, "priority": "10"},
	}
	auth2 := &cliproxyauth.Auth{
		ID:         prefix + "-2",
		Provider:   "devin",
		Status:     cliproxyauth.StatusActive,
		Attributes: map[string]string{"api_key": "p2", "base_url": server.URL, "priority": "5"},
	}
	for _, candidate := range []*cliproxyauth.Auth{auth1, auth2} {
		registry.GetGlobalRegistry().RegisterClient(candidate.ID, "devin", []*registry.ModelInfo{{ID: model}})
		id := candidate.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatal(errRegister)
		}
	}

	for _, dropCase := range []struct {
		name   string
		frames []byte
	}{
		{name: "premature_eof", frames: partialEOFFrames},
		{name: "read_error", frames: partialReadErrFrames},
	} {
		for _, clientFormat := range []sdktranslator.Format{
			sdktranslator.FormatOpenAI,
			sdktranslator.FormatOpenAIResponse,
			sdktranslator.FormatInteractions,
			sdktranslator.FormatClaude,
			sdktranslator.FormatGemini,
		} {
			for _, stream := range []bool{false, true} {
				mode := "nonstream"
				if stream {
					mode = "stream"
				}
				t.Run(fmt.Sprintf("%s/%s/%s", dropCase.name, clientFormat.String(), mode), func(t *testing.T) {
					activeFrames.Store(dropCase.frames)
					hits.Store(0)
					var rawReq []byte
					switch clientFormat {
					case sdktranslator.FormatOpenAIResponse, sdktranslator.FormatInteractions:
						rawReq = []byte(fmt.Sprintf(`{"model":"devin/swe-2","stream":%t,"input":[{"role":"user","content":"hello"}]}`, stream))
					case sdktranslator.FormatClaude:
						rawReq = []byte(fmt.Sprintf(`{"model":"devin/swe-2","max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream))
					case sdktranslator.FormatGemini:
						rawReq = []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
					default:
						rawReq = []byte(fmt.Sprintf(`{"model":"devin/swe-2","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, stream))
					}
					req := cliproxyexecutor.Request{Model: model, Payload: rawReq}
					opts := cliproxyexecutor.Options{
						Stream:          stream,
						SourceFormat:    clientFormat,
						ResponseFormat:  clientFormat,
						OriginalRequest: rawReq,
					}

					var gotErr error
					if stream {
						res, errStream := manager.ExecuteStream(context.Background(), []string{"devin"}, req, opts)
						if errStream != nil {
							gotErr = errStream
						} else {
							for chunk := range res.Chunks {
								if chunk.Err != nil {
									gotErr = chunk.Err
								}
							}
						}
					} else {
						_, gotErr = manager.Execute(context.Background(), []string{"devin"}, req, opts)
					}
					if gotErr == nil {
						t.Fatal("expected truncated error on premature EOF after partial output, got nil")
					}
					var truncErr cliproxyexecutor.StreamTruncatedError
					if !errors.As(gotErr, &truncErr) || !truncErr.IsStreamTruncated() {
						t.Fatalf("gotErr = %T (%v), want StreamTruncatedError", gotErr, gotErr)
					}
					if hits.Load() != 1 {
						t.Fatalf("hits = %d, want 1 (partial accepted generation must not replay)", hits.Load())
					}
					got1, _ := manager.GetByID(auth1.ID)
					if got1.Unavailable || !got1.NextRetryAfter.IsZero() {
						t.Fatalf("partial truncated stream cooled credential %s: unavailable=%v nextRetryAfter=%v", auth1.ID, got1.Unavailable, got1.NextRetryAfter)
					}
				})
			}
		}
	}
}

func TestIssue7442_DevinContentFilterZeroTextPreservedAsIncomplete(t *testing.T) {
	var stopFrame []byte
	stopFrame = protowire.AppendTag(stopFrame, 5, protowire.VarintType)
	stopFrame = protowire.AppendVarint(stopFrame, 11) // CONTENT_FILTER

	frames := append(
		helps.WrapConnectEnvelope(stopFrame),
		helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))...,
	)

	out, _, errConsume := consumeDevinFramesToInteractions(bytes.NewReader(frames), "devin/swe-2", "swe-2-high")
	if errConsume != nil {
		t.Fatalf("content_filter stop should not fail as empty completion: %v", errConsume)
	}
	parsed := ojson.ParseBytes(out)
	if parsed.Get("status").String() != "incomplete" || parsed.Get("finish_reason").String() != "content_filter" {
		t.Fatalf("unexpected content_filter output: %s", out)
	}
}
