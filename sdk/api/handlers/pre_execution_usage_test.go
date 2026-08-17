package handlers

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// captureUsagePlugin is a usage-plugin sink that records every delivered
// record so tests can assert on what was published.
type captureUsagePlugin struct {
	mu      sync.Mutex
	records []coreusage.Record
}

// HandleUsage implements coreusage.Plugin.
func (p *captureUsagePlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, record)
}

func (p *captureUsagePlugin) find(predicate func(coreusage.Record) bool) (coreusage.Record, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, record := range p.records {
		if predicate(record) {
			return record, true
		}
	}
	return coreusage.Record{}, false
}

// waitFor polls until condition is true or the deadline elapses. The global
// usage manager dispatches on a background goroutine, so assertions on the
// captured sink must be deferred through the poller.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadlineAt := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadlineAt) {
			t.Fatal("condition not met within timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForAbsent gives the dispatcher a short settle window and then asserts
// the absence held the whole way — used to prove a record was NOT published.
func waitForAbsent(t *testing.T, settle time.Duration, present func() bool) {
	t.Helper()
	deadlineAt := time.Now().Add(settle)
	for time.Now().Before(deadlineAt) {
		if present() {
			t.Fatal("unexpectedly found a record that should have been suppressed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// withCaptureUsage registers a named capture plugin and starts the default
// usage manager for the test's lifetime. Tests run sequentially within this
// package by default, so the shared manager is reused safely.
func withCaptureUsage(t *testing.T, name string) *captureUsagePlugin {
	t.Helper()
	capture := &captureUsagePlugin{}
	coreusage.RegisterNamedPlugin(name, capture)
	coreusage.StartDefault(context.Background())
	t.Cleanup(func() {
		// Replace with a no-op sink so the test-local capture never leaks
		// into records of other tests sharing the default manager.
		coreusage.RegisterNamedPlugin(name, &captureUsagePlugin{})
	})
	return capture
}

// TestPreExecutionRoutingFailurePublishesUsageError verifies that a request
// rejected before any provider executor runs (model unknown to every
// provider) is published as a failed usage record. Without this, such 400s
// never reached usage_errors and were invisible in the dashboard Errors feed.
func TestPreExecutionRoutingFailurePublishesUsageError(t *testing.T) {
	capture := withCaptureUsage(t, "test-pre-exec-routing")

	h := &BaseAPIHandler{} // no AuthManager, no routers: pure pre-execution path

	model := "definitely-not-registered-model-xyz"
	_, _, errMsg := h.ExecuteWithAuthManager(context.Background(), "openai", model,
		[]byte(`{"model":"`+model+`","messages":[]}`), "")
	if errMsg == nil {
		t.Fatal("expected an error for an unregistered model, got nil")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", errMsg.StatusCode)
	}

	var found bool
	var record coreusage.Record
	waitFor(t, 3*time.Second, func() bool {
		record, found = capture.find(func(r coreusage.Record) bool {
			return r.Failed && r.Fail.StatusCode == http.StatusBadRequest && r.Model == model
		})
		return found
	})
	if !found {
		t.Fatal("no failed usage record published for pre-executor routing failure")
	}
	if !record.Failed {
		t.Error("published record must be marked Failed")
	}
	if record.Model != model || record.RouteModel != model || record.Alias != model {
		t.Errorf("model attribution mismatch: model=%q route=%q alias=%q",
			record.Model, record.RouteModel, record.Alias)
	}
	if record.Fail.StatusCode != http.StatusBadRequest {
		t.Errorf("Fail.StatusCode = %d, want 400", record.Fail.StatusCode)
	}
	if record.Fail.Body == "" {
		t.Error("expected a non-empty failure body for diagnostic value")
	}
}

// TestPreExecutionStreamRoutingFailurePublishesUsageError is the streaming
// mirror of the non-stream test above.
func TestPreExecutionStreamRoutingFailurePublishesUsageError(t *testing.T) {
	capture := withCaptureUsage(t, "test-pre-exec-stream-routing")

	h := &BaseAPIHandler{}

	model := "definitely-not-registered-model-xyz"
	_, _, errChan := h.ExecuteStreamWithAuthManager(context.Background(), "openai", model,
		[]byte(`{"model":"`+model+`","messages":[],"stream":true}`), "")
	defer func() {
		for range errChan {
		}
	}()
	waitFor(t, 3*time.Second, func() bool {
		_, found := capture.find(func(r coreusage.Record) bool {
			return r.Failed && r.Fail.StatusCode == http.StatusBadRequest && r.Model == model
		})
		return found
	})
}

// TestRecordAuthManagerFailureOnlyRecordsPreExecution verifies the
// double-count guard: conductor failures that are certain to have happened
// before any executor ran are recorded, while generic errors (which the
// executor's own TrackFailure already recorded) are not.
func TestRecordAuthManagerFailureOnlyRecordsPreExecution(t *testing.T) {
	capture := withCaptureUsage(t, "test-pre-exec-auth-failure")
	ctx := context.Background()

	recordAuthManagerFailure(ctx, "openai-compat-test", "pre-exec-model-a", "alias-a",
		&coreauth.Error{Code: "auth_not_found", Message: "no auth available", HTTPStatus: http.StatusServiceUnavailable})

	var preexec coreusage.Record
	waitFor(t, 3*time.Second, func() bool {
		preexec, _ = capture.find(func(r coreusage.Record) bool {
			return r.Failed && r.Model == "pre-exec-model-a"
		})
		return preexec.Model != ""
	})
	if preexec.Fail.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Fail.StatusCode = %d, want 503", preexec.Fail.StatusCode)
	}

	execModel := "executor-already-recorded-model"
	recordAuthManagerFailure(ctx, "openai-compat-test", execModel, execModel, context.DeadlineExceeded)
	waitForAbsent(t, 300*time.Millisecond, func() bool {
		_, found := capture.find(func(r coreusage.Record) bool {
			return r.Model == execModel
		})
		return found
	})
}
