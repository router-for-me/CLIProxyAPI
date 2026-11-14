package store

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	if errReleaseCompleted := owner.RefreshRelease(ctx, key); errReleaseCompleted == nil {
		t.Fatal("completed receipt must never be released")
	}
	releaseKey := strings.Repeat("c", 64)
	claimedRelease, errClaimRelease := journals[0].RefreshClaim(ctx, releaseKey)
	if !claimedRelease || errClaimRelease != nil {
		t.Fatalf("claim releaseKey: %v, %v", claimedRelease, errClaimRelease)
	}
	if len(journals) > 1 {
		if errForeignRelease := journals[len(journals)-1].RefreshRelease(ctx, releaseKey); errForeignRelease == nil {
			t.Fatal("non-owner store released another store's pending claim")
		}
	}
	if errRelease := journals[0].RefreshRelease(ctx, releaseKey); errRelease != nil {
		t.Fatalf("release pending claim: %v", errRelease)
	}
	reclaimed, errReclaim := journals[len(journals)-1].RefreshClaim(ctx, releaseKey)
	if !reclaimed || errReclaim != nil {
		t.Fatalf("released pre-send claim could not be reclaimed: %v, %v", reclaimed, errReclaim)
	}
	if errStaleRelease := journals[0].RefreshRelease(ctx, releaseKey); errStaleRelease == nil {
		t.Fatal("previous owner released a successor store's active claim")
	}
}

func TestObjectRefreshReleaseDoesNotRetryOrDeleteSuccessorClaim(t *testing.T) {
	ctx := context.Background()
	key := strings.Repeat("d", 64)
	var mu sync.Mutex
	objects := map[string][]byte{}
	deleteCalls := 0
	var storeB, storeC *ObjectTokenStore
	successorClaimed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			data, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Error(errRead)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			mu.Lock()
			_, exists := objects[r.URL.Path]
			if exists {
				mu.Unlock()
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			objects[r.URL.Path] = data
			mu.Unlock()
			w.Header().Set("ETag", `"synthetic-etag"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet, http.MethodHead:
			mu.Lock()
			data, exists := objects[r.URL.Path]
			mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", `"synthetic-etag"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
		case http.MethodDelete:
			if strings.HasSuffix(r.URL.Path, "/"+key+"/claim") {
				mu.Lock()
				deleteCalls++
				callNum := deleteCalls
				delete(objects, r.URL.Path)
				mu.Unlock()
				if callNum == 1 {
					// Simulate S3 applying the delete while the gateway returns a retryable 503,
					// and a successor instance acquiring the claim before any client retry.
					claimedB, errClaimB := storeB.RefreshClaim(ctx, key)
					if errClaimB != nil || !claimedB {
						t.Errorf("successor claim after applied delete failed: claimed=%v, err=%v", claimedB, errClaimB)
					} else {
						mu.Lock()
						successorClaimed = true
						mu.Unlock()
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
			} else {
				mu.Lock()
				delete(objects, r.URL.Path)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	newStore := func() *ObjectTokenStore {
		t.Helper()
		s, errNew := NewObjectTokenStore(ObjectStoreConfig{
			Endpoint:  strings.TrimPrefix(server.URL, "http://"),
			Bucket:    "test-bucket",
			AccessKey: "synthetic",
			SecretKey: "synthetic",
			Region:    "us-east-1",
			PathStyle: true,
			LocalRoot: t.TempDir(),
		})
		if errNew != nil {
			t.Fatal(errNew)
		}
		return s
	}
	storeA := newStore()
	storeB = newStore()
	storeC = newStore()

	claimedA, errClaimA := storeA.RefreshClaim(ctx, key)
	if errClaimA != nil || !claimedA {
		t.Fatalf("storeA initial claim: claimed=%v, err=%v", claimedA, errClaimA)
	}
	if errReleaseB := storeB.RefreshRelease(ctx, key); errReleaseB == nil {
		t.Fatal("non-owner storeB must not be allowed to release storeA's claim")
	}

	errReleaseA := storeA.RefreshRelease(ctx, key)
	if errReleaseA == nil {
		t.Fatal("uncertain 503 DELETE response must fail closed on storeA")
	}

	mu.Lock()
	gotDeletes := deleteCalls
	gotSuccessor := successorClaimed
	mu.Unlock()
	if !gotSuccessor {
		t.Fatal("successor storeB did not acquire claim")
	}
	if gotDeletes != 1 {
		t.Fatalf("claim DELETE calls = %d, want 1 (must not retry and delete successor claim)", gotDeletes)
	}

	claimedC, errClaimC := storeC.RefreshClaim(ctx, key)
	if errClaimC != nil || claimedC {
		t.Fatalf("storeB's active claim was erased (storeC claimed=%v, err=%v)", claimedC, errClaimC)
	}
}

func TestObjectRefreshJournalHonorsEnvironmentProxy(t *testing.T) {
	if os.Getenv("CLIPROXY_TEST_OBJECT_PROXY") == "1" {
		ctx := context.Background()
		store, errNew := NewObjectTokenStore(ObjectStoreConfig{
			Endpoint:  "s3-proxy-only.invalid:80",
			Bucket:    "proxy-bucket",
			AccessKey: "synthetic",
			SecretKey: "synthetic",
			Region:    "us-east-1",
			PathStyle: true,
			LocalRoot: t.TempDir(),
		})
		if errNew != nil {
			t.Fatal(errNew)
		}
		key := strings.Repeat("e", 64)
		claimed, errClaim := store.RefreshClaim(ctx, key)
		if errClaim != nil || !claimed {
			t.Fatalf("RefreshClaim via HTTP_PROXY failed: claimed=%v, err=%v", claimed, errClaim)
		}
		if errRelease := store.RefreshRelease(ctx, key); errRelease != nil {
			t.Fatalf("RefreshRelease via HTTP_PROXY failed: %v", errRelease)
		}
		reclaimed, errReclaim := store.RefreshClaim(ctx, key)
		if errReclaim != nil || !reclaimed {
			t.Fatalf("reclaim via HTTP_PROXY failed: claimed=%v, err=%v", reclaimed, errReclaim)
		}
		if errComplete := store.RefreshComplete(ctx, key, []byte(`{"value":{"access_token":"ok"}}`)); errComplete != nil {
			t.Fatalf("RefreshComplete via HTTP_PROXY failed: %v", errComplete)
		}
		return
	}

	var mu sync.Mutex
	objects := map[string][]byte{}
	proxiedRequests := 0

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if _, exists := objects[r.URL.Path]; exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			data, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			objects[r.URL.Path] = data
			w.Header().Set("ETag", `"proxy-etag"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodHead, http.MethodGet:
			data, exists := objects[r.URL.Path]
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("ETag", `"proxy-etag"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Last-Modified", "Thu, 01 Jan 2026 00:00:00 GMT")
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer backend.Close()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxiedRequests++
		mu.Unlock()
		outReq, errReq := http.NewRequestWithContext(r.Context(), r.Method, backend.URL+r.URL.RequestURI(), r.Body)
		if errReq != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		outReq.Header = r.Header.Clone()
		resp, errDo := backend.Client().Do(outReq)
		if errDo != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				t.Errorf("close backend response: %v", errClose)
			}
		}()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxyServer.Close()

	binary, errExecutable := os.Executable()
	if errExecutable != nil {
		t.Fatal(errExecutable)
	}
	cmd := exec.Command(binary, "-test.run=^TestObjectRefreshJournalHonorsEnvironmentProxy$")
	var filteredEnv []string
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "HTTP_PROXY=") || strings.HasPrefix(upper, "HTTPS_PROXY=") || strings.HasPrefix(upper, "NO_PROXY=") {
			continue
		}
		filteredEnv = append(filteredEnv, entry)
	}
	cmd.Env = append(filteredEnv,
		"CLIPROXY_TEST_OBJECT_PROXY=1",
		"HTTP_PROXY="+proxyServer.URL,
		"HTTPS_PROXY="+proxyServer.URL,
		"NO_PROXY=",
	)
	if output, errRun := cmd.CombinedOutput(); errRun != nil {
		t.Fatalf("subprocess proxy test failed: %v\n%s", errRun, output)
	}
	mu.Lock()
	gotProxied := proxiedRequests
	mu.Unlock()
	if gotProxied == 0 {
		t.Fatal("expected refresh journal requests to traverse HTTP_PROXY")
	}
}
