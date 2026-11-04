package executor

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatExecutor_ImageStreamChunkBoundaryObservability(t *testing.T) {
	tests := []struct {
		name      string
		chunks    []string
		wantModel string
	}{
		{
			name: "chunk split across json boundaries (拆包)",
			chunks: []string{
				`data: {"id":"img_split","cre`,
				`ated":123,"mo`,
				`del":"dall-e-3","data":[{"url":"http://example.com/1.png"}]}` + "\n\n",
			},
			wantModel: "dall-e-3",
		},
		{
			name: "multiple events in single network chunk (合包)",
			chunks: []string{
				"event: ping\ndata: {}\n\nevent: completion\ndata: {\"model\":\"dall-e-3\",\"status\":\"done\"}\n\n",
			},
			wantModel: "dall-e-3",
		},
		{
			name: "chunk starting with event: prefix",
			chunks: []string{
				"event: image_event\n",
				"data: {\"model\":\"dall-e-3\",\"data\":[]}\n\n",
			},
			wantModel: "dall-e-3",
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, ok := w.(http.Flusher)
				for _, c := range tt.chunks {
					_, _ = w.Write([]byte(c))
					if ok {
						flusher.Flush()
					}
				}
			}))
			defer server.Close()

			alias := fmt.Sprintf("openai-compat-image-stream-test-%d", i)
			capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
			})

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name: "compat",
				}},
			})
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url":     server.URL,
					"api_key":      "test-key",
					"compat_name":  "compat",
					"provider_key": "compat",
				},
			}

			ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
			payload := []byte(`{"model":"dall-e-3","prompt":"a sunset over mountains"}`)
			streamRes, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "dall-e-3",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-image"),
				Stream:       true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream error: %v", err)
			}

			// Verify verbatim forwarding: collect all streamed bytes
			var received bytes.Buffer
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected chunk error: %v", chunk.Err)
				}
				received.Write(chunk.Payload)
			}

			expectedForwarded := strings.Join(tt.chunks, "")
			if received.String() != expectedForwarded {
				t.Fatalf("forwarded payload mismatch: got %q, want %q", received.String(), expectedForwarded)
			}

			// Verify response model in recorded usage
			record := capture.await(t)
			if record.ResponseModel != tt.wantModel {
				t.Fatalf("recorded ResponseModel = %q, want %q", record.ResponseModel, tt.wantModel)
			}
		})
	}
}

func TestOpenAICompatExecutor_ImageEndpointPath_HonorsOverriddenRequestPath_Issue6196(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"img"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "compat",
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL,
			"api_key":      "test-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	payload := []byte(`{"model":"gpt-image-2.5","prompt":"a red apple"}`)
	// Case 1: default inbound path is edits
	optsEdits := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/edits",
		},
	}
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-2.5",
		Payload: payload,
	}, optsEdits); err != nil {
		t.Fatalf("Execute edits error: %v", err)
	}
	if requestedPath != "/images/edits" {
		t.Fatalf("upstream path = %q, want /images/edits", requestedPath)
	}

	// Case 2: interceptor rewrites to generations
	optsGenerations := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-image-2.5",
		Payload: payload,
	}, optsGenerations)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("Execute returned empty payload")
	}
	if requestedPath != "/images/generations" {
		t.Fatalf("upstream path = %q, want /images/generations", requestedPath)
	}
}

func TestOpenAICompatImageStreamAccountsLargeCompletedEvents(t *testing.T) {
	for _, size := range []int{16, 100000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			event := `data: {"type":"image_generation.completed","model":"gpt-image-2","b64_json":"` + strings.Repeat("A", size) + `","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}` + "\n\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := fmt.Fprint(w, event); err != nil {
					t.Errorf("write upstream event: %v", err)
				}
			}))
			defer server.Close()

			alias := t.Name()
			capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
			})
			exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{
				Provider:   "openai-compatibility",
				Attributes: map[string]string{"base_url": server.URL, "api_key": "test"},
			}
			ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
			res, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gpt-image-2",
				Payload: []byte(`{"model":"gpt-image-2","prompt":"test"}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-image"),
				Stream:       true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				got.Write(chunk.Payload)
			}
			if got.String() != event {
				t.Fatal("forwarded image SSE bytes changed")
			}
			record := capture.await(t)
			if record.Detail.TotalTokens != 30 || record.Detail.ImageCount == nil || *record.Detail.ImageCount != 1 {
				t.Fatalf("image completion usage missing: %+v", record.Detail)
			}
		})
	}
}

func TestOpenAICompatAndCodexImageStreamReadFailurePreservesObservedUsage(t *testing.T) {
	for _, provider := range []string{"codex", "compat"} {
		t.Run(provider, func(t *testing.T) {
			event := `data: {"type":"image_generation.completed","model":"gpt-image-2","b64_json":"AA==","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}` + "\n\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Content-Length", fmt.Sprint(len(event)+100))
				if _, err := fmt.Fprint(w, event); err != nil {
					t.Errorf("write upstream event: %v", err)
				}
			}))
			defer server.Close()

			alias := t.Name()
			capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
			})
			ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
			req := cliproxyexecutor.Request{
				Model:   "gpt-image-2",
				Payload: []byte(`{"model":"gpt-image-2","prompt":"test"}`),
			}
			var (
				res *cliproxyexecutor.StreamResult
				err error
			)
			if provider == "codex" {
				res, err = NewCodexExecutor(&config.Config{}).ExecuteStream(ctx, newCodexOpenAIImageTestAuth(server.URL), req, codexOpenAIImageTestOptions(codexImagesGenerationsPath, true))
			} else {
				res, err = NewOpenAICompatExecutor("openai-compatibility", &config.Config{}).ExecuteStream(ctx, &cliproxyauth.Auth{
					Provider:   "openai-compatibility",
					Attributes: map[string]string{"base_url": server.URL, "api_key": "test"},
				}, req, cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai-image"),
					Stream:       true,
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			var streamErr error
			for chunk := range res.Chunks {
				got.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if got.String() != event || streamErr == nil {
				t.Fatalf("unexpected stream output=%q err=%v", got.String(), streamErr)
			}
			record := capture.await(t)
			if !record.Failed || record.Detail.TotalTokens != 30 || record.Detail.ImageCount == nil || *record.Detail.ImageCount != 1 {
				t.Fatalf("read failure lost observed image usage: failed=%v detail=%+v", record.Failed, record.Detail)
			}
		})
	}
}

func TestOpenAICompatMultipartImageEditExtractsRequestedImageDetails(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for k, v := range map[string]string{
				"model":   "gpt-image-2",
				"prompt":  "test",
				"size":    "1024x1536",
				"quality": "high",
			} {
				if err := writer.WriteField(k, v); err != nil {
					t.Fatal(err)
				}
			}
			part, err := writer.CreateFormFile("image", "image.png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = part.Write([]byte("PNG")); err != nil {
				t.Fatal(err)
			}
			if err = writer.Close(); err != nil {
				t.Fatal(err)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if errParse := r.ParseMultipartForm(1 << 20); errParse != nil {
					t.Errorf("parse multipart: %v", errParse)
					return
				}
				defer func() {
					if errRemove := r.MultipartForm.RemoveAll(); errRemove != nil {
						t.Errorf("remove multipart form: %v", errRemove)
					}
				}()
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					if _, errWrite := fmt.Fprint(w, `data: {"type":"image_edit.completed","b64_json":"AA==","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`+"\n\n"); errWrite != nil {
						t.Errorf("write stream: %v", errWrite)
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if _, errWrite := fmt.Fprint(w, `{"data":[{"b64_json":"AA=="}],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`); errWrite != nil {
					t.Errorf("write json: %v", errWrite)
				}
			}))
			defer server.Close()

			alias := t.Name()
			capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
			})
			ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
			exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{
				Provider:   "openai-compatibility",
				Attributes: map[string]string{"base_url": server.URL, "api_key": "test"},
			}
			req := cliproxyexecutor.Request{Model: "gpt-image-2", Payload: body.Bytes()}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-image"),
				Stream:       stream,
				Headers:      http.Header{"Content-Type": []string{writer.FormDataContentType()}},
				Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: "/v1/images/edits"},
			}
			if stream {
				res, errExec := exec.ExecuteStream(ctx, auth, req, opts)
				if errExec != nil {
					t.Fatal(errExec)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else {
				if _, errExec := exec.Execute(ctx, auth, req, opts); errExec != nil {
					t.Fatal(errExec)
				}
			}
			record := capture.await(t)
			if record.Detail.TotalTokens != 30 || record.Detail.RequestedImageDetails == nil ||
				record.Detail.RequestedImageDetails.Size != "1024x1536" ||
				record.Detail.RequestedImageDetails.Quality != "high" {
				t.Fatalf("unexpected multipart image usage: %+v requested=%+v", record.Detail, record.Detail.RequestedImageDetails)
			}
		})
	}
}

func TestOpenAICompatMultilineSSEAndXAIOutputItemDoneUsage(t *testing.T) {
	t.Run("openai_compat_multiline_sse_usage", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, strings.Join([]string{
				`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[],"usage":`,
				`data: {"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`,
				"",
				"data: [DONE]",
				"",
			}, "\n"))
		}))
		defer srv.Close()

		alias := t.Name()
		capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
		coreusage.RegisterNamedPlugin(t.Name(), capture)
		t.Cleanup(func() {
			coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
		})
		ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
		exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
		auth := &cliproxyauth.Auth{
			Provider:   "openai-compatibility",
			Attributes: map[string]string{"base_url": srv.URL, "api_key": "test"},
		}
		res, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
			Model:   "gpt-4o",
			Payload: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatOpenAI,
			Stream:       true,
		})
		if err != nil {
			t.Fatal(err)
		}
		for range res.Chunks {
		}
		rec := capture.await(t)
		if rec.Detail.TotalTokens != 20 {
			t.Fatalf("multiline SSE lost usage: %+v", rec.Detail)
		}
	})

	t.Run("xai_output_item_done_with_empty_completed_output", func(t *testing.T) {
		sseBody := strings.Join([]string{
			"event: response.output_item.done",
			`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ct_1","type":"custom_tool_call","status":"completed","call_id":"xs_1","name":"x_keyword_search","input":"{}"}}`,
			"",
			"event: response.output_item.done",
			`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","response":{"id":"resp_1","model":"grok-4","status":"completed","output":[],"usage":{"input_tokens":20,"output_tokens":10,"total_tokens":30}}}`,
			"",
		}, "\n")

		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, sseBody)
				}))
				defer srv.Close()

				alias := t.Name()
				capture := &multiProviderUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
				coreusage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() {
					coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{})
				})
				ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
				exec := NewXAIExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{
					Provider:   "xai",
					Attributes: map[string]string{"base_url": srv.URL, "api_key": "xai-test"},
				}
				req := cliproxyexecutor.Request{
					Model:   "grok-4",
					Payload: []byte(`{"model":"grok-4","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAIResponse,
					Stream:       stream,
				}
				if stream {
					res, err := exec.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if _, err := exec.Execute(ctx, auth, req, opts); err != nil {
						t.Fatal(err)
					}
				}
				rec := capture.await(t)
				if rec.Detail.ToolUsageDetails == nil ||
					rec.Detail.ToolUsageDetails.XSearchCalls == nil || *rec.Detail.ToolUsageDetails.XSearchCalls != 1 ||
					rec.Detail.ToolUsageDetails.ToolCallsRequested == nil || *rec.Detail.ToolUsageDetails.ToolCallsRequested != 1 {
					t.Fatalf("lost output_item.done tool usage: %+v", rec.Detail.ToolUsageDetails)
				}
			})
		}
	})
}
