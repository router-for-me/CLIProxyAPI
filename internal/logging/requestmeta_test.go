package logging

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestWithFreshResponseHeadersHolderIsolatesAttempts(t *testing.T) {
	requestCtx := WithResponseHeadersHolder(context.Background())
	SetResponseHeaders(requestCtx, http.Header{"X-Upstream-Attempt": []string{"request"}})

	firstAttemptCtx := WithFreshResponseHeadersHolder(requestCtx)
	if headers := GetResponseHeaders(firstAttemptCtx); len(headers) != 0 {
		t.Fatalf("fresh attempt inherited request headers: %#v", headers)
	}
	SetResponseHeaders(firstAttemptCtx, http.Header{"X-Upstream-Attempt": []string{"first"}})

	secondAttemptCtx := WithFreshResponseHeadersHolder(firstAttemptCtx)
	if headers := GetResponseHeaders(secondAttemptCtx); len(headers) != 0 {
		t.Fatalf("second attempt inherited first attempt headers: %#v", headers)
	}
	SetResponseHeaders(secondAttemptCtx, http.Header{"X-Upstream-Attempt": []string{"second"}})

	if got := GetResponseHeaders(requestCtx).Get("X-Upstream-Attempt"); got != "request" {
		t.Fatalf("request holder = %q, want request", got)
	}
	if got := GetResponseHeaders(firstAttemptCtx).Get("X-Upstream-Attempt"); got != "first" {
		t.Fatalf("first attempt holder = %q, want first", got)
	}
	if got := GetResponseHeaders(secondAttemptCtx).Get("X-Upstream-Attempt"); got != "second" {
		t.Fatalf("second attempt holder = %q, want second", got)
	}
}

func TestResponseHeadersReceivedAtTracksSetAndMerge(t *testing.T) {
	first := time.Unix(1790000000, 0)
	later := first.Add(10 * time.Minute)

	ctx := WithResponseHeadersHolder(context.Background())
	if got := GetResponseHeadersReceivedAt(ctx); !got.IsZero() {
		t.Fatalf("received at before headers = %v, want zero", got)
	}

	SetResponseHeadersAt(ctx, http.Header{"X-Codex-Primary-Reset-After-Seconds": []string{"3600"}}, first)
	if got := GetResponseHeadersReceivedAt(ctx); !got.Equal(first) {
		t.Fatalf("received at after set = %v, want %v", got, first)
	}

	MergeResponseHeadersAt(ctx, http.Header{"X-Codex-Primary-Used-Percent": []string{"12"}}, later)
	if got := GetResponseHeadersReceivedAt(ctx); !got.Equal(later) {
		t.Fatalf("received at after merge = %v, want %v", got, later)
	}
	// A partial merge keeps the receipt time of headers it did not carry.
	if got := GetResponseHeaderReceivedAt(ctx, "x-codex-primary-reset-after-seconds"); !got.Equal(first) {
		t.Fatalf("unmerged header received at = %v, want %v", got, first)
	}
	if got := GetResponseHeaderReceivedAt(ctx, "X-Codex-Primary-Used-Percent"); !got.Equal(later) {
		t.Fatalf("merged header received at = %v, want %v", got, later)
	}

	if got := GetResponseHeadersReceivedAt(context.Background()); !got.IsZero() {
		t.Fatalf("received at without holder = %v, want zero", got)
	}
}
