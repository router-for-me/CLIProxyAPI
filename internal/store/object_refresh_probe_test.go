package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRefreshProbeRecoversFromTransientOutage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if n == 3 {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, errNew := NewObjectTokenStore(ObjectStoreConfig{Endpoint: strings.TrimPrefix(server.URL, "http://"), Bucket: "test-bucket", AccessKey: "synthetic", SecretKey: "synthetic", Region: "us-east-1", PathStyle: true, LocalRoot: t.TempDir()})
	if errNew != nil {
		t.Fatal(errNew)
	}
	ctx := context.Background()
	if claimed, errClaim := store.RefreshClaim(ctx, strings.Repeat("a", 64)); errClaim == nil || claimed {
		t.Fatal("expected first transient failure to fail closed")
	}
	claimed, errClaim := store.RefreshClaim(ctx, strings.Repeat("b", 64))
	if errClaim != nil || !claimed {
		t.Fatalf("healthy backend cannot recover even with new authorization: claimed=%v error=%v HTTP calls=%d", claimed, errClaim, calls.Load())
	}
}
