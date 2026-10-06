package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// captureOpenAICompatUsagePlugin captures usage records emitted for
// openai-compatibility providers so tests can assert classification (Failed
// true/false) without a full Postgres wiring.
type captureOpenAICompatUsagePlugin struct {
	records chan usage.Record
}

func (p *captureOpenAICompatUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || record.Provider != "openai-compatibility" {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

// TestOpenAICompatStreamCancelPublishesFailure guards the Class A fix: an
// upstream SSE stream that is cancelled mid-stream (client disconnect before
// any [DONE]) must be recorded as a failed attempt (record.Failed == true),
// so the flusher routes it to usage_errors instead of usage_events.
//
// Prior to the fix, the streaming goroutine's unconditional deferred success
// publish (streamUsage.Publish / EnsurePublished) could claim
// UsageReporter.once with Failed=false before the cancellation was recorded,
// sending the aborted request to Recent Events instead of Errors.
func TestOpenAICompatStreamCancelPublishesFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("httptest server does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Emit one usage-less chunk, then hang until the request context is
		// cancelled — emulating an upstream that never sends [DONE].
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	plugin := &captureOpenAICompatUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)

	executorObj := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, errExecute := executorObj.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: []byte(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
		Metadata:     map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:usage-cancel"},
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}

	// Cancel the request context shortly after the stream begins, emulating a
	// client disconnect mid-stream (before any [DONE]).
	time.AfterFunc(50*time.Millisecond, cancel)

	for chunk := range result.Chunks {
		// A context-cancellation error chunk is expected on this path; any
		// other error is a real failure.
		if chunk.Err != nil && !errors.Is(chunk.Err, context.Canceled) {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatalf("stream record Failed = false; want true (cancelled mid-stream): %+v", record)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

// TestOpenAICompatStreamHTTP200DataErrorPublishesFailure guards the fix for
// OpenAI Responses API delivering an {"error":{...}} object inside a still-200
// SSE data line: the attempt must be recorded as Failed and surfaced as an
// error chunk, rather than forwarded as a successful stream.
func TestOpenAICompatStreamHTTP200DataErrorPublishesFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("httptest server does not support Flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"API Malformed and missing params\",\"type\":\"invalid_request_error\",\"code\":400}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	plugin := &captureOpenAICompatUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)

	executorObj := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, errExecute := executorObj.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: []byte(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}

	sawErrChunk := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawErrChunk = true
		}
	}
	if !sawErrChunk {
		t.Fatal("stream did not surface an error chunk for the HTTP 200 data error")
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatalf("stream record Failed = false; want true for upstream error-in-200: %+v", record)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

// TestStatusErrErrorEnrichesEmptyMessage pins the fallback text used when an
// upstream error body is empty: the code alone ("status 502") carried no
// context in the Errors feed, so the HTTP status text is included.
func TestStatusErrErrorEnrichesEmptyMessage(t *testing.T) {
	cases := []struct {
		name string
		err  statusErr
		want string
	}{
		{"message wins", statusErr{code: http.StatusBadGateway, msg: "upstream said no"}, "upstream said no"},
		{"empty message keeps status text", statusErr{code: http.StatusBadGateway}, "upstream status 502 (Bad Gateway)"},
		{"empty message and code", statusErr{}, "status 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOpenAICompatEmptyErrorBodyPublishesStatusText guards the Errors-feed
// enrichment: an upstream that answers a non-2xx with an empty body used to
// record just "status 502", which reads as a diagnosis but carries none. The
// fallback now includes the HTTP status text so the row is self-explanatory.
func TestOpenAICompatEmptyErrorBodyPublishesStatusText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	plugin := &captureOpenAICompatUsagePlugin{records: make(chan usage.Record, 8)}
	usage.RegisterPlugin(plugin)

	executorObj := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	_, errExecute := executorObj.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: []byte(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if errExecute == nil {
		t.Fatal("expected an error for a 502 response")
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatalf("record Failed = false; want true: %+v", record)
		}
		if want := "upstream status 502 (Bad Gateway)"; record.Fail.Body != want {
			t.Fatalf("Fail.Body = %q, want %q", record.Fail.Body, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}
