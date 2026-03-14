package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func newClusterExecutorHarness(t *testing.T, handler http.HandlerFunc, apiKeys ...string) (*ClusterExecutor, *cluster.PeerBinding, *coreauth.Auth) {
	t.Helper()

	publicServer := httptest.NewServer(handler)
	t.Cleanup(publicServer.Close)
	managementServer := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(managementServer.Close)

	cfg := &internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{
			Enabled: true,
			NodeID:  "node-a",
			Nodes: []internalconfig.ClusterNode{
				{
					ID:            "node-b",
					Enabled:       true,
					ManagementURL: managementServer.URL,
					APIKeys:       apiKeys,
				},
			},
		},
	}

	binding := &cluster.PeerBinding{
		ConfiguredID: "node-b",
		NodeID:       "node-b",
		AuthID:       cluster.RuntimeAuthID("node-b"),
		Provider:     cluster.ProviderKey("node-b"),
		AdvertiseURL: publicServer.URL,
		Models:       []string{"model-a"},
	}
	auth := &coreauth.Auth{
		ID:       binding.AuthID,
		Provider: binding.Provider,
		Attributes: map[string]string{
			cluster.AttributeRuntimeOnly: "true",
		},
		Runtime: binding,
	}
	return NewClusterExecutor(binding.Provider, cfg, cluster.NewService(cfg)), binding, auth
}

func clusterRequestMetadata(path, rawQuery string, headers http.Header) map[string]any {
	if headers == nil {
		headers = make(http.Header)
	}
	return map[string]any{
		cliproxyexecutor.RequestMethodMetadataKey:   http.MethodPost,
		cliproxyexecutor.RequestPathMetadataKey:     path,
		cliproxyexecutor.RequestRawQueryMetadataKey: rawQuery,
		cliproxyexecutor.RequestHeadersMetadataKey:  headers.Clone(),
	}
}

func TestClusterExecutorOpenAIChatRewritesAuthAndHopHeaders(t *testing.T) {
	var authHeaders []string
	var hops []string

	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		hops = append(hops, r.Header.Get(cluster.HeaderHop))
		if r.Header.Get(cluster.HeaderForwardedBy) != "node-a" {
			t.Fatalf("X-Cluster-Forwarded-By = %q", r.Header.Get(cluster.HeaderForwardedBy))
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.RawQuery; got != "" {
			t.Fatalf("raw query = %q, want empty after auth stripping", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "model-a" {
			t.Fatalf("forwarded model = %q, want %q", got, "model-a")
		}

		if r.Header.Get("Authorization") == "Bearer bad-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Upstream", "yes")
		_, _ = w.Write([]byte(`{"id":"chat-1","object":"chat.completion"}`))
	}, "bad-key", "good-key")

	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: []byte(`{"model":"node-b/model-a","messages":[{"role":"user","content":"hi"}]}`),
		Metadata: clusterRequestMetadata("/v1/chat/completions", "auth_token=client-token", http.Header{
			"Authorization": []string{"Bearer client-key"},
			"Content-Type":  []string{"application/json"},
		}),
	}
	req := cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: opts.OriginalRequest,
	}

	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := string(resp.Payload), `{"id":"chat-1","object":"chat.completion"}`; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if got := resp.Headers.Get("X-Upstream"); got != "yes" {
		t.Fatalf("X-Upstream = %q", got)
	}
	if !slices.Equal(authHeaders, []string{"Bearer bad-key", "Bearer good-key"}) {
		t.Fatalf("auth headers = %v", authHeaders)
	}
	if !slices.Equal(hops, []string{"1", "1"}) {
		t.Fatalf("hop headers = %v", hops)
	}

	authHeaders = nil
	_, err = exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if len(authHeaders) == 0 || authHeaders[0] != "Bearer good-key" {
		t.Fatalf("preferred API key not reused: %v", authHeaders)
	}
}

func TestClusterExecutorOpenAICompletionsConvertBackToChatShape(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "model-a" {
			t.Fatalf("forwarded model = %q, want %q", got, "model-a")
		}
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"text_completion","created":1,"model":"model-a","choices":[{"text":"hello","index":0,"finish_reason":"stop"}]}`))
	}, "peer-key")

	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: []byte(`{"model":"node-b/model-a","messages":[{"role":"user","content":"hi"}]}`),
		Metadata: func() map[string]any {
			meta := clusterRequestMetadata("/v1/completions", "", http.Header{"Content-Type": []string{"application/json"}})
			meta[cliproxyexecutor.RequestBodyOverrideMetadataKey] = []byte(`{"model":"node-b/model-a","prompt":"hi"}`)
			return meta
		}(),
	}
	req := cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: opts.OriginalRequest,
	}

	resp, err := exec.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "object").String(); got != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", got)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("message content = %q, want %q", got, "hello")
	}
}

func TestClusterExecutorOpenAICompletionsStreamConvertBackToChatChunks(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"cmpl-1\",\"object\":\"text_completion\",\"created\":1,\"model\":\"model-a\",\"choices\":[{\"text\":\"hello\",\"index\":0}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}, "peer-key")

	opts := cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: []byte(`{"model":"node-b/model-a","messages":[{"role":"user","content":"hi"}]}`),
		Metadata: func() map[string]any {
			meta := clusterRequestMetadata("/v1/completions", "", http.Header{"Content-Type": []string{"application/json"}})
			meta[cliproxyexecutor.RequestBodyOverrideMetadataKey] = []byte(`{"model":"node-b/model-a","prompt":"hi","stream":true}`)
			return meta
		}(),
	}
	req := cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: opts.OriginalRequest,
	}

	streamResult, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var chunks [][]byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		chunks = append(chunks, chunk.Payload)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunk count = %d, want 1", len(chunks))
	}
	if got := gjson.GetBytes(chunks[0], "object").String(); got != "chat.completion.chunk" {
		t.Fatalf("chunk object = %q, want chat.completion.chunk", got)
	}
	if got := gjson.GetBytes(chunks[0], "choices.0.delta.content").String(); got != "hello" {
		t.Fatalf("delta content = %q, want %q", got, "hello")
	}
}

func TestClusterExecutorResponsesCompactUsesCompactPath(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/compact" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer peer-key" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":"resp-1","output_text":"ok"}`))
	}, "peer-key")

	body := []byte(`{"model":"node-b/model-a","input":"hi"}`)
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: body,
	}, cliproxyexecutor.Options{
		Alt:             "responses/compact",
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: body,
		Metadata:        clusterRequestMetadata("/v1/responses/compact", "", http.Header{"Content-Type": []string{"application/json"}}),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := string(resp.Payload), `{"id":"resp-1","output_text":"ok"}`; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestClusterExecutorResponsesUsesResponsesPath(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer peer-key" {
			t.Fatalf("Authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		if got := gjson.GetBytes(body, "model").String(); got != "model-a" {
			t.Fatalf("forwarded model = %q, want %q", got, "model-a")
		}
		w.Header().Set("X-Upstream", "responses")
		_, _ = w.Write([]byte(`{"id":"resp-1","status":"completed"}`))
	}, "peer-key")

	body := []byte(`{"model":"node-b/model-a","input":"hi"}`)
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: body,
		Metadata:        clusterRequestMetadata("/v1/responses", "auth_token=client-token", http.Header{"Content-Type": []string{"application/json"}}),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := resp.Headers.Get("X-Upstream"); got != "responses" {
		t.Fatalf("X-Upstream = %q", got)
	}
	if got, want := string(resp.Payload), `{"id":"resp-1","status":"completed"}`; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestClusterExecutorClaudeMessagesAndCountTokensUseXAPIKey(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(*ClusterExecutor, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	}{
		{
			name: "messages",
			path: "/v1/messages",
			call: func(exec *ClusterExecutor, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return exec.Execute(context.Background(), auth, req, opts)
			},
		},
		{
			name: "count_tokens",
			path: "/v1/messages/count_tokens",
			call: func(exec *ClusterExecutor, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return exec.CountTokens(context.Background(), auth, req, opts)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Fatalf("path = %q, want %q", r.URL.Path, tc.path)
				}
				if got := r.Header.Get("X-Api-Key"); got != "peer-key" {
					t.Fatalf("X-Api-Key = %q", got)
				}
				if got := r.Header.Get("Authorization"); got != "" {
					t.Fatalf("Authorization should be empty, got %q", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("io.ReadAll() error = %v", err)
				}
				if got := gjson.GetBytes(body, "model").String(); got != "model-a" {
					t.Fatalf("forwarded model = %q, want %q", got, "model-a")
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}, "peer-key")

			body := []byte(`{"model":"node-b/model-a"}`)
			resp, err := tc.call(exec, auth, cliproxyexecutor.Request{
				Model:   "node-b/model-a",
				Payload: body,
			}, cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FromString("claude"),
				OriginalRequest: body,
				Metadata:        clusterRequestMetadata(tc.path, "", http.Header{"Content-Type": []string{"application/json"}}),
			})
			if err != nil {
				t.Fatalf("call error = %v", err)
			}
			if got, want := string(resp.Payload), `{"ok":true}`; got != want {
				t.Fatalf("payload = %q, want %q", got, want)
			}
		})
	}
}

func TestClusterExecutorGeminiRouteMappingAndQuerySanitization(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/model-a:streamGenerateContent" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.RawQuery; got != "alt=sse" {
			t.Fatalf("raw query = %q, want %q", got, "alt=sse")
		}
		if got := r.Header.Get("X-Goog-Api-Key"); got != "peer-key" {
			t.Fatalf("X-Goog-Api-Key = %q", got)
		}
		if got := r.Header.Get(cluster.HeaderHop); got != "1" {
			t.Fatalf("X-Cluster-Hop = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidate\":\"ok\"}\n\n"))
	}, "peer-key")

	opts := cliproxyexecutor.Options{
		Stream:          true,
		Alt:             "sse",
		SourceFormat:    sdktranslator.FromString("gemini"),
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		Metadata: clusterRequestMetadata("/v1beta/models/node-b/model-a:streamGenerateContent", "alt=sse&auth_token=client-token", http.Header{
			"Content-Type": []string{"application/json"},
		}),
	}
	req := cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: opts.OriginalRequest,
	}

	streamResult, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var payloads []string
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payloads = append(payloads, string(chunk.Payload))
	}
	if !slices.Equal(payloads, []string{`{"candidate":"ok"}`}) {
		t.Fatalf("payloads = %v", payloads)
	}
}

func TestClusterExecutorRejectsAlreadyForwardedRequests(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("peer should not be called when request is already forwarded")
	}, "peer-key")

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: []byte(`{"model":"node-b/model-a"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: []byte(`{"model":"node-b/model-a"}`),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey:      "/v1/chat/completions",
			cliproxyexecutor.RequestMethodMetadataKey:    http.MethodPost,
			cliproxyexecutor.ClusterForwardedMetadataKey: true,
		},
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want forwarded-request rejection")
	}
	if !strings.Contains(err.Error(), "cannot be forwarded again") {
		t.Fatalf("error = %v", err)
	}
}

func TestClusterExecutorHttpRequestUsesAdvertiseURLAndRewritesGeminiPath(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/model-a:generateContent" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.URL.RawQuery; got != "alt=sse" {
			t.Fatalf("raw query = %q", got)
		}
		if got := r.Header.Get("X-Goog-Api-Key"); got != "peer-key" {
			t.Fatalf("X-Goog-Api-Key = %q", got)
		}
		if got := r.Header.Get(cluster.HeaderHop); got != "1" {
			t.Fatalf("X-Cluster-Hop = %q", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}, "peer-key")

	req := httptest.NewRequest(http.MethodPost, "http://client.example.com/v1beta/models/node-b/model-a:generateContent?alt=sse&key=client-token", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", "client-key")

	resp, err := exec.HttpRequest(context.Background(), auth, req)
	if err != nil {
		t.Fatalf("HttpRequest() error = %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestClusterExecutorHttpRequestRejectsResponsesWebsocketGET(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("peer should not be called for GET /v1/responses")
	}, "peer-key")

	req := httptest.NewRequest(http.MethodGet, "http://client.example.com/v1/responses", nil)
	_, err := exec.HttpRequest(context.Background(), auth, req)
	if err == nil {
		t.Fatal("HttpRequest() error = nil, want websocket rejection")
	}
	if !strings.Contains(err.Error(), "does not support websocket forwarding") {
		t.Fatalf("error = %v", err)
	}
}

func TestClusterExecutorCountTokensFallbackUsesCountTokensPath(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"input_tokens":12}`))
	}, "peer-key")

	body := []byte(`{"model":"node-b/model-a","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if got, want := string(resp.Payload), `{"input_tokens":12}`; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestClusterExecutorOpenAIFallbackUsesCompletionsPathWhenPromptPresent(t *testing.T) {
	exec, _, auth := newClusterExecutorHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"cmpl-2","object":"text_completion","created":1,"model":"model-a","choices":[{"text":"done","index":0}]}`))
	}, "peer-key")

	body := []byte(`{"model":"node-b/model-a","prompt":"finish this"}`)
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "node-b/model-a",
		Payload: body,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "object").String(); got != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", got)
	}
}
