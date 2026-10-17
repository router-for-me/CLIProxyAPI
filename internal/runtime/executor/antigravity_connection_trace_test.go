package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestAntigravityProjectDiscoveryConnectionTrace(t *testing.T) {
	logger := log.StandardLogger()
	oldLevel := logger.GetLevel()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	oldOutput := logger.Out
	logger.SetLevel(log.DebugLevel)
	logger.SetOutput(io.Discard)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() {
		logger.SetLevel(oldLevel)
		logger.ReplaceHooks(oldHooks)
		logger.SetOutput(oldOutput)
	})
	auth := &cliproxyauth.Auth{Index: "discovery-index"}
	requests := 0
	ctx := logging.WithRequestID(context.Background(), "discovery-request-id")
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		trace := httptrace.ContextClientTrace(req.Context())
		if trace == nil || trace.GotConn == nil {
			t.Fatal("discovery request missing connection trace")
		}
		trace.GotConn(httptrace.GotConnInfo{})
		var body string
		switch req.URL.Path {
		case "/v1internal:loadCodeAssist":
			body = `{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`
		case "/v1internal:onboardUser":
			body = `{"done":true,"response":{"cloudaicompanionProject":{"id":"trace-project"}}}`
		default:
			t.Fatalf("unexpected discovery request: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	executor := &AntigravityExecutor{}
	projectID, errFetch := executor.fetchAntigravityProjectID(ctx, auth, "secret-token")
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	if projectID != "trace-project" || requests != 2 {
		t.Fatalf("project=%q requests=%d", projectID, requests)
	}
	var entries []*log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "antigravity upstream connection acquired" {
			entries = append(entries, entry)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("connection entries=%d, want 2", len(entries))
	}
	for i, host := range []string{"cloudcode-pa.googleapis.com", "daily-cloudcode-pa.googleapis.com"} {
		fields := entries[i].Data
		if fields["operation"] != "project_discovery" || fields["request_id"] != "discovery-request-id" || fields["auth_index"] != "discovery-index" || fields["upstream_host"] != host {
			t.Fatalf("unexpected trace fields: %v", fields)
		}
	}
}
