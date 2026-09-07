package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestUpstreamMetricsResultPaths(t *testing.T) {
	for _, path := range []string{"local", "home", "ephemeral"} {
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
