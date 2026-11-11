package httpfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetBytesReturnsBodyAndSendsHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "agent" || r.Header.Get("Accept") != "application/json" {
			http.Error(w, "missing headers", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(server.Close)

	data, errGet := GetBytes(context.Background(), server.Client(), server.URL, map[string]string{
		"User-Agent": "agent",
		"Accept":     "application/json",
	}, 0)
	if errGet != nil {
		t.Fatalf("GetBytes() error = %v", errGet)
	}
	if string(data) != "payload" {
		t.Fatalf("GetBytes() = %q, want payload", data)
	}
}

func TestGetBytesRejectsErrorStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	_, errGet := GetBytes(context.Background(), server.Client(), server.URL, nil, 0)
	if errGet == nil {
		t.Fatal("GetBytes() error = nil")
	}
	if !strings.Contains(errGet.Error(), "unexpected status 404") {
		t.Fatalf("GetBytes() error = %v, want status 404", errGet)
	}
}

func TestGetBytesEnforcesMaxSize(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	}))
	t.Cleanup(server.Close)

	_, errGet := GetBytes(context.Background(), server.Client(), server.URL, nil, 4)
	if errGet == nil {
		t.Fatal("GetBytes() error = nil")
	}
	if !strings.Contains(errGet.Error(), "maximum allowed size") {
		t.Fatalf("GetBytes() error = %v, want size limit error", errGet)
	}
}
