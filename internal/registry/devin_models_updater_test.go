package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The body arrives after the headers, so client.Do returns before it does.
// Canceling the request context at that point made io.ReadAll fail with
// "context canceled".
func TestFetchDevinModelsFromRemoteReadsBodyAfterHeaders(t *testing.T) {
	const payload = `{"devin":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	previous := devinModelsURLs
	devinModelsURLs = []string{server.URL}
	defer func() { devinModelsURLs = previous }()

	body, source := fetchDevinModelsFromRemote(context.Background())
	if source != server.URL {
		t.Fatalf("source = %q, want %q (the read failed)", source, server.URL)
	}
	if string(body) != payload {
		t.Fatalf("body = %q, want %q", body, payload)
	}
}
