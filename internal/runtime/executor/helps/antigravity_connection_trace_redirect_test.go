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

func TestAntigravityConnectionTraceRedirect(t *testing.T) {
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
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	targetURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL+"/secret-path?token=secret-query", http.StatusFound)
	}))
	defer source.Close()
	for _, operation := range []string{"generate_stream", "project_discovery"} {
		t.Run(operation, func(t *testing.T) {
			hook.Reset()
			transport := &http.Transport{}
			client := &http.Client{Transport: transport}
			defer transport.CloseIdleConnections()
			redirectCalls := 0
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				redirectCalls++
				if req.URL.Hostname() != "localhost" {
					t.Fatalf("unexpected redirect target: %s", req.URL.Hostname())
				}
				return nil
			}
			tracedClient := WithAntigravityHTTPClientTrace(client, &cliproxyauth.Auth{Index: "redirect-index"}, operation)
			existingCalls := 0
			ctx := logging.WithRequestID(context.Background(), "redirect-request-id")
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { existingCalls++ }})
			for attempt := 0; attempt < 2; attempt++ {
				req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
				if errReq != nil {
					t.Fatal(errReq)
				}
				resp, errDo := tracedClient.Do(req)
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
				if resp.Request.URL.Hostname() != "localhost" {
					t.Fatal("redirect did not reach target")
				}
			}
			entries := hook.AllEntries()
			if len(entries) != 4 || existingCalls != 4 || redirectCalls != 2 {
				t.Fatalf("entries=%d existing hooks=%d redirects=%d", len(entries), existingCalls, redirectCalls)
			}
			for i, entry := range entries {
				host := "127.0.0.1"
				if i%2 == 1 {
					host = "localhost"
				}
				fields := entry.Data
				if fields["upstream_host"] != host || fields["request_id"] != "redirect-request-id" || fields["auth_index"] != "redirect-index" || fields["operation"] != operation || fields["reused"] != (i >= 2) {
					t.Fatalf("connection %d has incorrect fields: %v", i, fields)
				}
				formatted, errFormat := (&logging.LogFormatter{}).Format(entry)
				if errFormat != nil {
					t.Fatal(errFormat)
				}
				if strings.Contains(string(formatted), "secret") {
					t.Fatalf("secret leaked: %s", formatted)
				}
			}
			// The wrapper must not attach its trace to the original request context.
			if trace := httptrace.ContextClientTrace(ctx); trace == nil {
				t.Fatal("original trace lost")
			}
		})
	}
}

func TestAntigravityTraceTransportCloseIdleConnections(t *testing.T) {
	base := &antigravityTraceCleanupTransport{}
	client := WithAntigravityHTTPClientTrace(&http.Client{Transport: base}, nil, "http_request")
	client.CloseIdleConnections()
	if !base.closed {
		t.Fatal("idle connection cleanup was not forwarded")
	}
}

type antigravityTraceCleanupTransport struct{ closed bool }

func (t *antigravityTraceCleanupTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func (t *antigravityTraceCleanupTransport) CloseIdleConnections() { t.closed = true }
