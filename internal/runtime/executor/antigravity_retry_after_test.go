package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const antigravityHeaderOnly429 = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)."}}`

// These modes reach each HTTP error exit, including Claude's non-streaming
// request, which uses the streaming upstream endpoint internally.
var antigravityRetryAfterModes = []string{"execute", "count", "stream", "claude_execute", "claude_stream", "stream_in_band"}

func antigravityRetryAfterRequest(mode string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	model := "gemini-2.5-flash"
	if mode == "claude_execute" || mode == "claude_stream" {
		model = "claude-sonnet-4-6"
	}
	return cliproxyexecutor.Request{Model: model, Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatAntigravity, ResponseFormat: sdktranslator.FormatAntigravity, Stream: mode == "stream" || mode == "claude_stream"}
}

func antigravityRetryAfterFixture(t *testing.T, mode, header, body string) (*AntigravityExecutor, *cliproxyauth.Auth, *atomic.Int32) {
	t.Helper()
	sends := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		if header != "" {
			w.Header().Set("Retry-After", header)
		}
		if mode == "stream_in_band" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Status: cliproxyauth.StatusActive, Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "test-token", "project_id": "test-project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "disable_cooling": false}}
	executor := NewAntigravityExecutor(&config.Config{}) // Credits fallback is disabled.
	t.Cleanup(func() { closeAntigravityAuthIdleTransports(auth) })
	return executor, auth, sends
}

func antigravityRetryAfterExecute(ctx context.Context, executor *AntigravityExecutor, auth *cliproxyauth.Auth, mode string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	switch mode {
	case "count":
		_, err := executor.CountTokens(ctx, auth, req, opts)
		return err
	case "stream", "claude_stream", "stream_in_band":
		res, err := executor.ExecuteStream(ctx, auth, req, opts)
		if err != nil {
			return err
		}
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	default:
		_, err := executor.Execute(ctx, auth, req, opts)
		return err
	}
}

func TestAntigravityRetryAfterHTTPPaths(t *testing.T) {
	oldKV := currentAntigravityKVClient
	currentAntigravityKVClient = func() (antigravityKVClient, bool, error) { return nil, false, nil }
	t.Cleanup(func() { currentAntigravityKVClient = oldKV })
	retryInfo := func(delay string) string {
		return fmt.Sprintf(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":%q}]}}`, delay)
	}
	cases := []struct {
		name, header, body string
		want               time.Duration
		absent             bool
	}{
		{name: "numeric_long", header: "86400", body: antigravityHeaderOnly429, want: 24 * time.Hour},
		{name: "http_date", header: time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat), body: antigravityHeaderOnly429, want: 24 * time.Hour},
		{name: "header_zero", header: "0", body: antigravityHeaderOnly429},
		{name: "past_date", header: "Mon, 02 Jan 2006 15:04:05 GMT", body: antigravityHeaderOnly429},
		{name: "retry_info_without_error_info_wins", header: "86400", body: retryInfo("2190s"), want: 2190 * time.Second},
		{name: "body_zero_wins", header: "86400", body: retryInfo("0s")},
		{name: "body_message_wins", header: "86400", body: `{"error":{"code":429,"message":"Please retry in 2h."}}`, want: 2 * time.Hour},
		{name: "malformed_header", header: "later", body: antigravityHeaderOnly429, absent: true},
		{name: "negative_header", header: "-1", body: antigravityHeaderOnly429, absent: true},
		{name: "no_hint", body: antigravityHeaderOnly429, absent: true},
	}
	for _, mode := range antigravityRetryAfterModes {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				executor, auth, sends := antigravityRetryAfterFixture(t, mode, tc.header, tc.body)
				req, opts := antigravityRetryAfterRequest(mode)
				before := time.Now()
				err := antigravityRetryAfterExecute(context.Background(), executor, auth, mode, req, opts)
				var status interface{ StatusCode() int }
				if !errors.As(err, &status) || status.StatusCode() != http.StatusTooManyRequests {
					t.Fatalf("error = %v, want 429", err)
				}
				if got := sends.Load(); got != 1 {
					t.Fatalf("physical sends = %d, want 1", got)
				}
				var hint interface{ RetryAfter() *time.Duration }
				if !errors.As(err, &hint) {
					t.Fatalf("error %T lacks retry hint", err)
				}
				got := hint.RetryAfter()
				if tc.absent {
					if got != nil {
						t.Fatalf("retry hint = %v, want nil", *got)
					}
					return
				}
				if got == nil {
					t.Fatal("retry hint = nil, want explicit upstream hint")
				}
				if tc.name == "http_date" {
					deadline, _ := http.ParseTime(tc.header)
					if *got < deadline.Sub(time.Now()) || *got > deadline.Sub(before) {
						t.Fatalf("HTTP-date hint = %v, outside response-time bounds", *got)
					}
				} else if *got != tc.want {
					t.Fatalf("retry hint = %v, want %v", *got, tc.want)
				}
			})
		}
	}
}

func antigravityRetryAfterManagerCall(ctx context.Context, manager *cliproxyauth.Manager, mode string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	switch mode {
	case "count":
		_, err := manager.ExecuteCount(ctx, []string{"antigravity"}, req, opts)
		return err
	case "stream", "claude_stream", "stream_in_band":
		res, err := manager.ExecuteStream(ctx, []string{"antigravity"}, req, opts)
		if err != nil {
			return err
		}
		for chunk := range res.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	default:
		_, err := manager.Execute(ctx, []string{"antigravity"}, req, opts)
		return err
	}
}

func TestAntigravityRetryAfterManagerPreservesHeader(t *testing.T) {
	oldKV := currentAntigravityKVClient
	currentAntigravityKVClient = func() (antigravityKVClient, bool, error) { return nil, false, nil }
	t.Cleanup(func() { currentAntigravityKVClient = oldKV })
	for _, mode := range antigravityRetryAfterModes {
		for _, date := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/date=%v", mode, date), func(t *testing.T) {
				header := "86400"
				if date {
					header = time.Now().Add(24 * time.Hour).UTC().Format(http.TimeFormat)
				}
				executor, auth, sends := antigravityRetryAfterFixture(t, mode, header, antigravityHeaderOnly429)
				req, opts := antigravityRetryAfterRequest(mode)
				manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
				manager.SetConfig(&config.Config{})
				// A provider deadline longer than the configured retry budget must survive
				// rather than being replaced by the conductor's default exponential backoff.
				manager.SetRetryConfig(1, time.Millisecond, 0)
				manager.RegisterExecutor(executor)
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: req.Model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				before := time.Now()
				firstErr := antigravityRetryAfterManagerCall(context.Background(), manager, mode, req, opts)
				if firstErr == nil {
					t.Fatal("first request succeeded, want 429")
				}
				if got := sends.Load(); got != 1 {
					t.Fatalf("physical sends after first request = %d, want 1", got)
				}
				saved, ok := manager.GetByID(auth.ID)
				if !ok {
					t.Fatal("registered auth disappeared")
				}
				state := saved.ModelStates[req.Model]
				if state == nil || !state.Unavailable || !state.Quota.Exceeded {
					t.Fatalf("model state = %+v, want quota cooldown", state)
				}
				delay := state.NextRetryAfter.Sub(before)
				if delay < 24*time.Hour-5*time.Second || delay > 24*time.Hour+5*time.Second {
					t.Fatalf("cooldown = %v, want explicit 24h hint", delay)
				}
				for i, err := range []error{firstErr, antigravityRetryAfterManagerCall(context.Background(), manager, mode, req, opts)} {
					if err == nil {
						t.Fatalf("request %d succeeded during cooldown", i+1)
					}
					raw := cliproxyauth.SafeResponseHeaders(err).Get("Retry-After")
					seconds, parseErr := strconv.Atoi(raw)
					if parseErr != nil || seconds < 86395 || seconds > 86405 {
						t.Fatalf("request %d trusted Retry-After = %q, want explicit 24h hint", i+1, raw)
					}
				}
				if got := sends.Load(); got != 1 {
					t.Fatalf("physical sends after cooldown request = %d, want 1", got)
				}
			})
		}
	}
}
