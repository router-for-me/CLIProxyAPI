package cliproxy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

type fakeErrorEventStore struct {
	inserted    []*mongostate.ErrorEventRecord
	insertErr   error
	lastQuery   mongostate.ErrorEventQuery
	queryResult mongostate.ErrorEventQueryResult
	queryErr    error
}

func (f *fakeErrorEventStore) Insert(_ context.Context, record *mongostate.ErrorEventRecord) error {
	if record != nil {
		cloned := *record
		cloned.UpstreamRequestIDs = append([]string(nil), record.UpstreamRequestIDs...)
		f.inserted = append(f.inserted, &cloned)
	}
	return f.insertErr
}

func (f *fakeErrorEventStore) Query(_ context.Context, query mongostate.ErrorEventQuery) (mongostate.ErrorEventQueryResult, error) {
	f.lastQuery = query
	return f.queryResult, f.queryErr
}

func TestErrorEventPluginHandleUsageWritesFailedRecord(t *testing.T) {
	store := &fakeErrorEventStore{}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	plugin := NewErrorEventPlugin()
	record := coreusage.Record{
		Provider:           "gemini",
		Model:              "gpt-5(high)",
		Source:             "api_key",
		AuthID:             "auth-1",
		AuthIndex:          "1",
		RequestID:          "req-1",
		RequestLogRef:      "req-1",
		AttemptCount:       3,
		UpstreamRequestIDs: []string{"up-1", "up-2"},
		RequestedAt:        time.Date(2026, 4, 27, 10, 30, 0, 0, time.UTC),
		Failed:             true,
		FailureStage:       "request_execution",
		ErrorCode:          "upstream_error",
		ErrorMessage:       "invalid_request_error: token=abc123 authorization: bearer very-secret",
		StatusCode:         400,
	}
	plugin.HandleUsage(context.Background(), record)

	if len(store.inserted) != 1 {
		t.Fatalf("insert count = %d, want 1", len(store.inserted))
	}
	inserted := store.inserted[0]
	if inserted.NormalizedModel != "gpt-5" {
		t.Fatalf("normalized model = %q, want %q", inserted.NormalizedModel, "gpt-5")
	}
	if inserted.CircuitCountable {
		t.Fatalf("circuit_countable = true, want false")
	}
	if inserted.CircuitSkipReason != "invalid_request" {
		t.Fatalf("circuit_skip_reason = %q, want %q", inserted.CircuitSkipReason, "invalid_request")
	}
	if inserted.ErrorMessageHash == "" {
		t.Fatal("error_message_hash should not be empty")
	}
	if strings.Contains(inserted.ErrorMessageMasked, "abc123") || strings.Contains(inserted.ErrorMessageMasked, "very-secret") {
		t.Fatalf("masked message should hide secrets, got %q", inserted.ErrorMessageMasked)
	}
	if inserted.AttemptCount != 3 {
		t.Fatalf("attempt_count = %d, want 3", inserted.AttemptCount)
	}
	if len(inserted.UpstreamRequestIDs) != 2 {
		t.Fatalf("upstream_request_ids len = %d, want 2", len(inserted.UpstreamRequestIDs))
	}
}

func TestErrorEventPluginHandleUsageSkipsSuccessfulRecord(t *testing.T) {
	store := &fakeErrorEventStore{}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	plugin := NewErrorEventPlugin()
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider: "gemini",
		Model:    "gpt-5",
		Failed:   false,
	})

	if len(store.inserted) != 0 {
		t.Fatalf("insert count = %d, want 0", len(store.inserted))
	}
}

func TestErrorEventPluginHandleUsageBestEffortOnStoreFailure(t *testing.T) {
	store := &fakeErrorEventStore{insertErr: errors.New("write failed")}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	before := errorEventPluginWriteFailures.Load()
	plugin := NewErrorEventPlugin()
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider:     "gemini",
		Model:        "gpt-5",
		Failed:       true,
		StatusCode:   503,
		ErrorMessage: strings.Repeat("x", 1500),
	})

	if len(store.inserted) != 1 {
		t.Fatalf("insert count = %d, want 1", len(store.inserted))
	}
	if got := len([]rune(store.inserted[0].ErrorMessageMasked)); got != 1024 {
		t.Fatalf("masked message rune length = %d, want 1024", got)
	}
	after := errorEventPluginWriteFailures.Load()
	if after != before+1 {
		t.Fatalf("failure counter = %d, want %d", after, before+1)
	}
}
