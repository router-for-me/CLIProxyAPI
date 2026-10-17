package helps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestAntigravityConnectionTrace(t *testing.T) {
	logger := log.StandardLogger()
	oldLevel := logger.GetLevel()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	oldOutput := logger.Out
	logger.SetOutput(io.Discard)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() {
		logger.SetLevel(oldLevel)
		logger.ReplaceHooks(oldHooks)
		logger.SetOutput(oldOutput)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	auth := &cliproxyauth.Auth{Index: "safe-index", ID: "secret-identity", Metadata: map[string]any{"access_token": "secret-token"}}

	logger.SetLevel(log.InfoLevel)
	req, errReq := http.NewRequest(http.MethodGet, server.URL, nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	if got := WithAntigravityConnectionTrace(req, auth, "generate"); got != req {
		t.Fatal("info mode must leave request unchanged")
	}
	if WithAntigravityConnectionTrace(nil, nil, "generate") != nil {
		t.Fatal("nil request must remain nil")
	}

	logger.SetLevel(log.DebugLevel)
	invalidReq := &http.Request{Header: make(http.Header)}
	if WithAntigravityConnectionTrace(invalidReq, auth, "generate") != invalidReq {
		t.Fatal("nil URL request must remain unchanged")
	}
	if _, errDo := client.Do(WithAntigravityConnectionTrace(invalidReq, auth, "generate")); errDo == nil {
		t.Fatal("nil URL must return an HTTP client error")
	}
	existingCalls := 0
	ctx := logging.WithRequestID(context.Background(), "test-request-id")
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { existingCalls++ }})
	for i := 0; i < 2; i++ {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/secret-path?token=secret-query", nil)
		if errReq != nil {
			t.Fatal(errReq)
		}
		req.Header.Set("Authorization", "Bearer secret-header")
		resp, errDo := client.Do(WithAntigravityConnectionTrace(req, auth, "generate"))
		if errDo != nil {
			t.Fatal(errDo)
		}
		_, errRead := io.Copy(io.Discard, resp.Body)
		errClose := resp.Body.Close()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errClose != nil {
			t.Fatal(errClose)
		}
	}
	if existingCalls != 2 {
		t.Fatalf("existing trace calls = %d, want 2", existingCalls)
	}
	entries := hook.AllEntries()
	if len(entries) != 2 {
		t.Fatalf("log entries = %d, want 2", len(entries))
	}
	for i, entry := range entries {
		if entry.Level != log.DebugLevel || entry.Message != "antigravity upstream connection acquired" {
			t.Fatalf("unexpected entry: %+v", entry)
		}
		if entry.Data["reused"] != (i == 1) || entry.Data["was_idle"] != (i == 1) {
			t.Fatalf("connection %d fields: %v", i, entry.Data)
		}
		if entry.Data["auth_index"] != "safe-index" || entry.Data["request_id"] != "test-request-id" || entry.Data["upstream_host"] != "127.0.0.1" {
			t.Fatalf("unexpected fields: %v", entry.Data)
		}
		if _, ok := entry.Data["idle_time"].(string); !ok {
			t.Fatal("missing idle_time")
		}
		formattedBytes, errString := (&logging.LogFormatter{}).Format(entry)
		formatted := string(formattedBytes)
		if errString != nil {
			t.Fatal(errString)
		}
		for _, field := range []string{`auth_index="safe-index"`, `upstream_host="127.0.0.1"`, `operation="generate"`, "reused=", "was_idle=", "idle_time=", "test-request-id"[len("test-request-id")-8:]} {
			if !strings.Contains(formatted, field) {
				t.Fatalf("formatted log missing %q: %s", field, formatted)
			}
		}
		if strings.Contains(formatted, "secret") {
			t.Fatalf("secret leaked in log: %s", formatted)
		}
	}

	// Credential-discovery helpers create their own requests using the supplied client.
	hook.Reset()
	tracedClient := WithAntigravityHTTPClientTrace(client, auth, "project_discovery")
	if client.Transport != transport || tracedClient == client {
		t.Fatal("tracing must not modify the original client or pool")
	}
	for _, path := range []string{"/v1internal:loadCodeAssist", "/v1internal:onboardUser"} {
		discoveryReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, nil)
		if errReq != nil {
			t.Fatal(errReq)
		}
		resp, errDo := tracedClient.Do(discoveryReq)
		if errDo != nil {
			t.Fatal(errDo)
		}
		_, errRead := io.Copy(io.Discard, resp.Body)
		errClose := resp.Body.Close()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if errClose != nil {
			t.Fatal(errClose)
		}
	}
	entries = hook.AllEntries()
	if len(entries) != 2 || existingCalls != 4 {
		t.Fatalf("discovery entries=%d trace calls=%d", len(entries), existingCalls)
	}
	for _, entry := range entries {
		if entry.Data["operation"] != "project_discovery" || entry.Data["request_id"] != "test-request-id" || entry.Data["reused"] != true {
			t.Fatalf("discovery trace fields: %v", entry.Data)
		}
	}
	if WithAntigravityHTTPClientTrace(nil, nil, "project_discovery") != nil {
		t.Fatal("nil client must remain nil")
	}
	defaultClient := &http.Client{}
	tracedDefault := WithAntigravityHTTPClientTrace(defaultClient, nil, "project_discovery")
	if tracedDefault.Transport.(*antigravityConnectionTraceTransport).base != http.DefaultTransport || defaultClient.Transport != nil {
		t.Fatal("default transport must be preserved")
	}
	hook.Reset()
	req = WithAntigravityConnectionTrace(req, nil, "credits_query")
	httptrace.ContextClientTrace(req.Context()).GotConn(httptrace.GotConnInfo{})
	if hook.LastEntry().Data["request_id"] != "" || hook.LastEntry().Data["operation"] != "credits_query" {
		t.Fatal("background query must retain its operation without inventing a request ID")
	}
	if hook.LastEntry().Data["auth_index"] != "" {
		t.Fatal("nil auth should have empty index")
	}
	logger.SetLevel(log.InfoLevel)
	hook.Reset()
	httptrace.ContextClientTrace(req.Context()).GotConn(httptrace.GotConnInfo{})
	if len(hook.AllEntries()) != 0 {
		t.Fatal("trace should respect runtime log-level changes")
	}
}
