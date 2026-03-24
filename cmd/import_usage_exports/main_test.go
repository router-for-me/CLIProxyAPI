package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func TestImportSnapshotPostsExpectedPayloadAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/usage/import" {
			t.Fatalf("path = %s, want /v0/management/usage/import", r.URL.Path)
		}
		if got := r.Header.Get("X-Management-Key"); got != "secret" {
			t.Fatalf("management key = %q, want secret", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content type = %q, want application/json", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) == 0 {
			t.Fatal("expected request body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"added":2,"skipped":1,"total_requests":5,"failed_requests":1}`))
	}))
	defer server.Close()

	resp, err := importSnapshot(server.URL, "secret", 5*time.Second, usage.StatisticsSnapshot{TotalRequests: 2})
	if err != nil {
		t.Fatalf("importSnapshot failed: %v", err)
	}
	if resp.Added != 2 || resp.Skipped != 1 || resp.TotalRequests != 5 || resp.FailedRequests != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestImportSnapshotSurfacesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"invalid management key"}`))
	}))
	defer server.Close()

	_, err := importSnapshot(server.URL, "bad", 5*time.Second, usage.StatisticsSnapshot{})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "server returned 403: invalid management key" {
		t.Fatalf("unexpected error: %v", err)
	}
}
