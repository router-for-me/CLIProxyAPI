package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Upstream terminates the stream with a promptFeedback block and no candidates at all, so the
// request produced no content. A translated egress cannot express that, and must see an error
// naming the block reason rather than an empty completion or a truncation error.
const geminiPromptBlockChunk = "data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\",\"blockReasonMessage\":\"The prompt is blocked due to safety\"},\"usageMetadata\":{\"promptTokenCount\":413751},\"modelVersion\":\"gemini-4-argon\"}\n\n"

type promptBlockStreamExecutor interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

// promptBlockStreamCase serves the blocked chunk and returns everything needed to drive one of
// the three Gemini/Vertex stream loops.
func promptBlockStreamCase(t *testing.T, provider string) (context.Context, promptBlockStreamExecutor, *cliproxyauth.Auth) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, geminiPromptBlockChunk)
	}))
	t.Cleanup(server.Close)

	if provider == "vertex_service_account" {
		var serviceAccount map[string]any
		if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		target, errParse := parseTestURL(t, server.URL)
		if errParse != nil {
			t.Fatal(errParse)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		t.Cleanup(transport.CloseIdleConnections)
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
		return ctx, NewGeminiVertexExecutor(&config.Config{}), &cliproxyauth.Auth{
			Provider: "vertex",
			Metadata: map[string]any{
				"project_id":      "proxy-test",
				"location":        "global",
				"service_account": serviceAccount,
			},
		}
	}

	auth := &cliproxyauth.Auth{
		Provider:   provider,
		Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL},
	}
	if provider == "gemini" {
		return context.Background(), NewGeminiExecutor(&config.Config{}), auth
	}
	return context.Background(), NewGeminiVertexExecutor(&config.Config{}), auth
}

func TestGeminiAndVertexPromptBlockReportsError(t *testing.T) {
	for _, responseFormat := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatClaude} {
		for _, provider := range []string{"gemini", "vertex", "vertex_service_account"} {
			t.Run(provider+"/"+string(responseFormat), func(t *testing.T) {
				ctx, exec, auth := promptBlockStreamCase(t, provider)
				result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
					Model:   "gemini-2.5-flash",
					Payload: []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
				}, cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatOpenAI,
					ResponseFormat: responseFormat,
					Stream:         true,
				})
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}

				var output bytes.Buffer
				var streamErr error
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						streamErr = chunk.Err
						continue
					}
					output.Write(chunk.Payload)
				}
				if streamErr == nil {
					t.Fatalf("expected an error chunk for a blocked prompt, got none; output=%q", output.String())
				}
				if !strings.Contains(streamErr.Error(), "SAFETY") {
					t.Fatalf("error must name the upstream block reason, got %q", streamErr.Error())
				}
				if !strings.Contains(streamErr.Error(), "The prompt is blocked due to safety") {
					t.Fatalf("error must carry blockReasonMessage, got %q", streamErr.Error())
				}
				if strings.Contains(streamErr.Error(), "closed before finishReason") {
					t.Fatalf("a blocked prompt must not be reported as truncation, got %q", streamErr.Error())
				}
			})
		}
	}
}

// The native Gemini egress forwards promptFeedback verbatim, so the client is already told what
// happened. Passthrough must stay byte-faithful to upstream and gain no synthesized error.
func TestGeminiPromptBlockPassesThroughOnNativeEgress(t *testing.T) {
	for _, provider := range []string{"gemini", "vertex", "vertex_service_account"} {
		t.Run(provider, func(t *testing.T) {
			ctx, exec, auth := promptBlockStreamCase(t, provider)
			result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gemini-2.5-flash",
				Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
				Stream:         true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}

			var output bytes.Buffer
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("native Gemini egress must forward the block unchanged, got error %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if !strings.Contains(output.String(), "SAFETY") {
				t.Fatalf("expected the upstream promptFeedback to reach the client; output=%q", output.String())
			}
		})
	}
}
