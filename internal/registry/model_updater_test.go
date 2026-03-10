package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestStartModelsUpdaterReturnsImmediatelyAndInvokesCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write(embeddedModelsJSON)
	}))
	defer server.Close()

	oldURLs := modelsURLs
	oldOnce := updaterOnce
	modelsURLs = []string{server.URL}
	updaterOnce = sync.Once{}
	t.Cleanup(func() {
		modelsURLs = oldURLs
		updaterOnce = oldOnce
	})

	updated := make(chan struct{}, 1)
	start := time.Now()
	StartModelsUpdater(context.Background(), func() {
		select {
		case updated <- struct{}{}:
		default:
		}
	})
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expected StartModelsUpdater to return quickly, took %s", elapsed)
	}

	select {
	case <-updated:
	case <-time.After(2 * time.Second):
		t.Fatal("expected startup models refresh callback to fire")
	}
}

func TestStartModelsUpdaterInvokesCallbackOnRefreshFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	oldURLs := modelsURLs
	oldOnce := updaterOnce
	modelsURLs = []string{server.URL}
	updaterOnce = sync.Once{}
	t.Cleanup(func() {
		modelsURLs = oldURLs
		updaterOnce = oldOnce
	})

	updated := make(chan struct{}, 1)
	StartModelsUpdater(context.Background(), func() {
		select {
		case updated <- struct{}{}:
		default:
		}
	})

	select {
	case <-updated:
	case <-time.After(2 * time.Second):
		t.Fatal("expected callback even when startup refresh falls back to embedded models")
	}
}
