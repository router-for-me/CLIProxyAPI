package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestIssue7536_AntigravitySignatureOnlyZeroOutputWithoutFinishReasonFails502BeforeBootstrap(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	variants := []struct {
		name string
		part string
	}{
		{
			name: "non_thought_signature_only_empty_text",
			part: `{"thoughtSignature":"opaque-sig-7536","text":""}`,
		},
		{
			name: "thought_flag_signature_only_empty_text",
			part: `{"thought":true,"thoughtSignature":"opaque-sig-7536","text":""}`,
		},
	}

	formats := []sdktranslator.Format{
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatClaude,
		sdktranslator.FormatGemini,
		sdktranslator.FormatOpenAIResponse,
	}

	for _, variant := range variants {
		for _, format := range formats {
			t.Run(variant.name+"/"+format.String(), func(t *testing.T) {
				sse := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[` + variant.part + `]}}],"usageMetadata":{"promptTokenCount":378515,"cachedContentTokenCount":376466,"totalTokenCount":378515},"modelVersion":"gemini-4-hydrogen","responseId":"resp-7536"},"traceId":"trace-7536","metadata":{}}` + "\n\n"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, sse)
				}))
				defer server.Close()

				auth := &cliproxyauth.Auth{
					ID:       "ag-7536-" + variant.name + "-" + format.String(),
					Provider: "antigravity",
					Metadata: map[string]any{
						"access_token": "token-123",
						"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
						"project_id":   "project-1",
					},
					Attributes: map[string]string{"base_url": server.URL},
				}
				capture := &malformedFunctionCallUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
				usage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() {
					usage.RegisterNamedPlugin(t.Name(), malformedFunctionCallUsageNoop{})
				})

				exec := NewAntigravityExecutor(&config.Config{})
				streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
					Model:    "gemini-4-hydrogen",
					Payload:  issue7513RequestPayload(format, "gemini-4-hydrogen", true),
					Metadata: map[string]any{"session_id": "sess-7536"},
				}, cliproxyexecutor.Options{
					SourceFormat:   format,
					ResponseFormat: format,
					Stream:         true,
				})
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}

				var emittedPayloads [][]byte
				var streamErr error
				for chunk := range streamRes.Chunks {
					if chunk.Err != nil {
						streamErr = chunk.Err
						continue
					}
					if len(bytes.TrimSpace(chunk.Payload)) > 0 {
						emittedPayloads = append(emittedPayloads, bytes.Clone(chunk.Payload))
					}
				}
				if len(emittedPayloads) > 0 {
					t.Fatalf("ExecuteStream() emitted %d payload chunk(s) before error, want 0 so readStreamBootstrap does not commit 200: %q", len(emittedPayloads), emittedPayloads)
				}
				if streamErr == nil {
					t.Fatal("ExecuteStream() chunk.Err = nil, want 502 error")
				}
				streamStatus, okStatus := streamErr.(interface{ StatusCode() int })
				if !okStatus || streamStatus.StatusCode() != http.StatusBadGateway {
					t.Fatalf("ExecuteStream() chunk.Err = %T %v, want status 502", streamErr, streamErr)
				}
				var record usage.Record
				select {
				case record = <-capture.records:
				case <-time.After(3 * time.Second):
					t.Fatal("timed out waiting for usage record")
				}
				if !record.Failed {
					t.Fatalf("usage record Failed = false, want true; record=%+v", record)
				}
				if record.Detail.InputTokens != 378515 || record.Detail.CachedTokens != 376466 || record.Detail.OutputTokens != 0 || record.Detail.ReasoningTokens != 0 || record.Detail.TotalTokens != 378515 {
					t.Fatalf("usage detail = %+v, want Input=378515 Cached=376466 Output=0 Reasoning=0 Total=378515", record.Detail)
				}
				if items, ok := internalcache.GetAntigravityReasoningReplayItems("gemini-4-hydrogen", "sess-7536"); ok || len(items) != 0 {
					t.Fatalf("expected no reasoning replay items committed, got %v", items)
				}
			})
		}
	}
}

func TestIssue7536_AntigravityLeadingSignatureOnlyFollowedByVisibleContentSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"opaque-sig-7536","text":""}]}}],"modelVersion":"gemini-4-hydrogen","responseId":"resp-7536-ok"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered answer"}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":5,"totalTokenCount":105},"modelVersion":"gemini-4-hydrogen","responseId":"resp-7536-ok"}}`+"\n\n")
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       "ag-7536-follow-ok",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}
	exec := NewAntigravityExecutor(&config.Config{})
	streamRes, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-4-hydrogen",
		Payload: issue7513RequestPayload(sdktranslator.FormatOpenAI, "gemini-4-hydrogen", true),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var combined bytes.Buffer
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}
	out := combined.String()
	if !strings.Contains(out, "recovered answer") || !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("expected recovered answer and finish_reason stop, got: %s", out)
	}
}
