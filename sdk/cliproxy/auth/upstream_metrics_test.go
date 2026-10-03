package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestUpstreamMetricsResultPaths(t *testing.T) {
	for _, path := range []string{"local", "home", "ephemeral", "availability-neutral"} {
		t.Run(path, func(t *testing.T) {
			ResetUpstreamMetricsForTest()
			t.Cleanup(ResetUpstreamMetricsForTest)
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "metrics-auth"}
			report := func(result Result) {
				switch path {
				case "local":
					m.recordExecutionResult(context.Background(), result, auth, false)
				case "home":
					m.reportHomeResult(context.Background(), result, auth)
				case "ephemeral":
					m.recordExecutionResult(context.Background(), result, auth, true)
				case "availability-neutral":
					m.recordAvailabilityNeutralResult(context.Background(), result)
				}
			}
			report(Result{AuthID: auth.ID, Success: true})
			report(Result{AuthID: auth.ID, Error: &Error{HTTPStatus: 429}})
			report(Result{AuthID: auth.ID, Error: &Error{HTTPStatus: 503}})
			report(Result{AuthID: auth.ID})
			report(Result{Success: true}) // Invalid results must not be counted.
			success, failures := SnapshotUpstreamMetrics()
			if success != 1 || len(failures) != 3 || failures["429"] != 1 || failures["503"] != 1 || failures["unknown"] != 1 {
				t.Fatalf("metrics = %d, %v; want exactly one success and one of each error", success, failures)
			}
			if auth.Success != 0 || auth.Failed != 0 {
				t.Fatal("reporting mutated supplied auth")
			}
		})
	}
}

func TestUpstreamMetricsHomeExecution(t *testing.T) {
	for _, path := range []string{"execute", "stream", "stream-error"} {
		t.Run(path, func(t *testing.T) {
			ResetUpstreamMetricsForTest()
			t.Cleanup(ResetUpstreamMetricsForTest)
			m := NewManager(nil, nil, nil)
			m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			registry := executionregistry.New()
			m.PublishHomeDispatch(homeExecutionDispatcher{}, registry, 1)
			if path == "execute" {
				m.RegisterExecutor(&homeExecutionExecutor{})
				if _, err := m.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{}); err != nil {
					t.Fatal(err)
				}
			} else {
				chunks := make(chan cliproxyexecutor.StreamChunk, 2)
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
				if path == "stream-error" {
					chunks <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: 503, Message: "upstream failed"}}
				}
				close(chunks)
				m.RegisterExecutor(&homeExecutionStreamExecutor{chunks: chunks})
				stream, err := m.ExecuteStream(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "test"}, cliproxyexecutor.Options{})
				if err != nil {
					t.Fatal(err)
				}
				for range stream.Chunks {
				}
			}
			if err := registry.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			success, failures := SnapshotUpstreamMetrics()
			if path == "stream-error" {
				if success != 0 || len(failures) != 1 || failures["503"] != 1 {
					t.Fatalf("metrics = %d, %v; want one 503", success, failures)
				}
			} else if success != 1 || len(failures) != 0 {
				t.Fatalf("metrics = %d, %v; want one success", success, failures)
			}
		})
	}
}

type directHTTPMetricsExecutor struct {
	status int
	err    error
}

func (*directHTTPMetricsExecutor) Identifier() string { return "codex" }
func (*directHTTPMetricsExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*directHTTPMetricsExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*directHTTPMetricsExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*directHTTPMetricsExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *directHTTPMetricsExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	if e.err != nil {
		return nil, e.err
	}
	return &http.Response{
		StatusCode: e.status,
		Status:     http.StatusText(e.status),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     make(http.Header),
	}, nil
}

func TestUpstreamMetricsHttpRequest(t *testing.T) {
	ResetUpstreamMetricsForTest()
	t.Cleanup(ResetUpstreamMetricsForTest)

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "alpha-search-auth", Provider: "codex"}
	m.RegisterExecutor(&directHTTPMetricsExecutor{status: http.StatusOK})

	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/alpha/search", strings.NewReader(`{"query":"x"}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := m.HttpRequest(context.Background(), auth, req)
	if err != nil {
		t.Fatalf("HttpRequest success path: %v", err)
	}
	_ = resp.Body.Close()

	m.RegisterExecutor(&directHTTPMetricsExecutor{status: http.StatusTooManyRequests})
	resp, err = m.HttpRequest(context.Background(), auth, req)
	if err != nil {
		t.Fatalf("HttpRequest 429 path: %v", err)
	}
	_ = resp.Body.Close()

	m.RegisterExecutor(&directHTTPMetricsExecutor{err: &Error{HTTPStatus: http.StatusBadGateway, Message: "dial failed"}})
	_, err = m.HttpRequest(context.Background(), auth, req)
	if err == nil {
		t.Fatal("expected transport error")
	}

	success, failures := SnapshotUpstreamMetrics()
	if success != 1 || failures["429"] != 1 || failures["502"] != 1 || len(failures) != 2 {
		t.Fatalf("metrics = %d, %v; want 1 success, 429=1, 502=1", success, failures)
	}
}

func TestRecordDirectHttpUpstreamResultWebsocketHandshake(t *testing.T) {
	ResetUpstreamMetricsForTest()
	t.Cleanup(ResetUpstreamMetricsForTest)

	authOK := &Auth{ID: "realtime-auth", Provider: "codex"}

	// Successful upgrade (101 Switching Protocols) must count as success.
	RecordDirectHttpUpstreamResult(authOK, &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Status:     http.StatusText(http.StatusSwitchingProtocols),
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}, nil)

	// Rejected handshake: gorilla dial returns err + response; prefer HTTP status.
	RecordDirectHttpUpstreamResult(authOK, &http.Response{
		StatusCode: http.StatusUnauthorized,
		Status:     http.StatusText(http.StatusUnauthorized),
		Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
		Header:     make(http.Header),
	}, errors.New("websocket: bad handshake"))

	RecordDirectHttpUpstreamResult(authOK, &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     http.StatusText(http.StatusTooManyRequests),
		Body:       io.NopCloser(strings.NewReader(`{"error":"rate_limited"}`)),
		Header:     make(http.Header),
	}, errors.New("websocket: bad handshake"))

	// Transport-only failure with no handshake response.
	RecordDirectHttpUpstreamResult(authOK, nil, &Error{HTTPStatus: http.StatusBadGateway, Message: "dial tcp: connection refused"})

	// Empty auth ID must be ignored.
	RecordDirectHttpUpstreamResult(&Auth{ID: "  ", Provider: "codex"}, &http.Response{StatusCode: http.StatusOK}, nil)
	RecordDirectHttpUpstreamResult(nil, &http.Response{StatusCode: http.StatusOK}, nil)

	success, failures := SnapshotUpstreamMetrics()
	if success != 1 || failures["401"] != 1 || failures["429"] != 1 || failures["502"] != 1 || len(failures) != 3 {
		t.Fatalf("metrics = %d, %v; want 1 success, 401=1, 429=1, 502=1", success, failures)
	}
}
