package store

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func TestObjectRefreshJournalConditionalCreation(t *testing.T) {
	for _, ignoreCondition := range []bool{false, true} {
		t.Run(strconv.FormatBool(ignoreCondition), func(t *testing.T) {
			var mu sync.Mutex
			objects := map[string][]byte{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case http.MethodPut:
					if r.Header.Get("If-None-Match") != "*" {
						t.Error("claim did not send the HTTP wildcard condition")
					}
					if _, exists := objects[r.URL.Path]; exists && !ignoreCondition {
						w.WriteHeader(http.StatusPreconditionFailed)
						return
					}
					data, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						w.WriteHeader(500)
						return
					}
					objects[r.URL.Path] = data
					w.Header().Set("ETag", `"synthetic-etag"`)
					w.WriteHeader(http.StatusOK)
				case http.MethodGet, http.MethodHead:
					data, exists := objects[r.URL.Path]
					if !exists {
						w.WriteHeader(404)
						return
					}
					w.Header().Set("ETag", `"synthetic-etag"`)
					w.Header().Set("Content-Length", strconv.Itoa(len(data)))
					w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
					_, _ = w.Write(data)
				case http.MethodDelete:
					delete(objects, r.URL.Path)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			var journals []codex.RefreshJournal
			for i := 0; i < 2; i++ {
				store, errNew := NewObjectTokenStore(ObjectStoreConfig{Endpoint: strings.TrimPrefix(server.URL, "http://"), Bucket: "test-bucket", AccessKey: "synthetic", SecretKey: "synthetic", Region: "us-east-1", PathStyle: true, LocalRoot: t.TempDir()})
				if errNew != nil {
					t.Fatal(errNew)
				}
				journals = append(journals, store)
			}
			if ignoreCondition {
				claimed, errClaim := journals[0].RefreshClaim(context.Background(), strings.Repeat("b", 64))
				if errClaim == nil || claimed {
					t.Fatalf("unsupported condition must fail closed: %v, %v", claimed, errClaim)
				}
				return
			}
			exerciseSharedRefreshJournal(t, journals)
		})
	}
}

func exerciseSharedRefreshJournal(t *testing.T, journals []codex.RefreshJournal) {
	t.Helper()
	ctx := context.Background()
	key := strings.Repeat("b", 64)
	var mu sync.Mutex
	winners := 0
	var owner codex.RefreshJournal
	var wg sync.WaitGroup
	for _, journal := range journals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, errClaim := journal.RefreshClaim(ctx, key)
			if errClaim != nil {
				t.Errorf("claim: %v", errClaim)
				return
			}
			if claimed {
				mu.Lock()
				winners++
				owner = journal
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("claim winners = %d, want 1", winners)
	}
	for _, journal := range journals {
		claimed, errClaim := journal.RefreshClaim(ctx, key)
		if errClaim != nil || claimed {
			t.Fatalf("unresolved claim was reclaimed: %v, %v", claimed, errClaim)
		}
		pending, errRead := journal.RefreshRead(ctx, key)
		if errRead != nil || string(pending) != codex.PendingRefreshReceipt {
			t.Fatalf("pending receipt = %s, error %v", pending, errRead)
		}
	}
	receipt := []byte(`{"stage":"response-read"}`)
	if errComplete := owner.RefreshComplete(ctx, key, receipt); errComplete != nil {
		t.Fatal(errComplete)
	}
	for _, journal := range journals {
		data, errRead := journal.RefreshRead(ctx, key)
		if errRead != nil || string(data) != string(receipt) {
			t.Fatalf("shared receipt = %s, error %v", data, errRead)
		}
		claimed, errClaim := journal.RefreshClaim(ctx, key)
		if claimed || errClaim != nil {
			t.Fatalf("completed unknown outcome was reclaimed: %v, %v", claimed, errClaim)
		}
	}
}
