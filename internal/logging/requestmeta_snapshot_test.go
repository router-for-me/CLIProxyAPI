package logging

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestResponseHeadersSnapshotReceiptAndIsolation(t *testing.T) {
	ctx := WithResponseHeadersHolder(context.Background())
	before := time.Now()
	SetResponseHeaders(ctx, http.Header{"X-Codex-Primary-Used-Percent": {"0"}})
	headers, at := GetResponseHeadersSnapshot(ctx)
	if at.Before(before) || at.After(time.Now()) || headers.Get("X-Codex-Primary-Used-Percent") != "0" {
		t.Fatalf("invalid received snapshot: %v %v", headers, at)
	}
	headers.Set("X-Codex-Primary-Used-Percent", "99")
	copy, sameAt := GetResponseHeadersSnapshot(ctx)
	if copy.Get("X-Codex-Primary-Used-Percent") != "0" || !sameAt.Equal(at) {
		t.Fatal("read modified or retimestamped the stored snapshot")
	}
	fresh := WithFreshResponseHeadersHolder(ctx)
	if h, stamp := GetResponseHeadersSnapshot(fresh); len(h) != 0 || !stamp.IsZero() {
		t.Fatal("retry inherited headers or receipt time")
	}

	// A websocket quota event updates receipt time; ordinary frames (no quota
	// headers) must not re-date the previous observation.
	holder := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	holder.observedAt = time.Unix(1, 0)
	before = time.Now()
	MergeResponseHeaders(ctx, http.Header{"X-Codex-Primary-Used-Percent": {"1"}})
	headers, at = GetResponseHeadersSnapshot(ctx)
	if at.Before(before) || at.After(time.Now()) || headers.Get("X-Codex-Primary-Used-Percent") != "1" {
		t.Fatal("websocket quota update did not replace receipt time")
	}
	MergeResponseHeaders(ctx, nil)
	if _, stamp := GetResponseHeadersSnapshot(ctx); !stamp.Equal(at) {
		t.Fatal("empty websocket frame changed receipt time")
	}
	SetResponseHeaders(ctx, nil)
	if h, stamp := GetResponseHeadersSnapshot(ctx); len(h) != 0 || !stamp.IsZero() {
		t.Fatal("clearing headers retained a receipt time")
	}
	for _, empty := range []context.Context{nil, context.Background()} {
		if h, stamp := GetResponseHeadersSnapshot(empty); len(h) != 0 || !stamp.IsZero() {
			t.Fatal("missing holder returned a snapshot")
		}
	}
}

func TestResponseHeadersSnapshotAtomicPair(t *testing.T) {
	ctx := WithResponseHeadersHolder(context.Background())
	holder := ctx.Value(responseHeadersKey{}).(*responseHeadersHolder)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := int64(1); i <= 1000; i++ {
			holder.mu.Lock()
			holder.headers = http.Header{"X-Sequence": {strconv.FormatInt(i, 10)}}
			holder.observedAt = time.Unix(i, 0)
			holder.mu.Unlock()
		}
	}()
	for i := 0; i < 1000; i++ {
		h, at := GetResponseHeadersSnapshot(ctx)
		if len(h) > 0 && h.Get("X-Sequence") != strconv.FormatInt(at.Unix(), 10) {
			t.Error("headers and receipt time came from different updates")
			break
		}
	}
	wg.Wait()
}
