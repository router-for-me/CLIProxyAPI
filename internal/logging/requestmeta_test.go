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

func TestResponseHeaderCaptureTimesSurvivePartialEvents(t *testing.T) {
	ctx := WithResponseHeadersHolder(context.Background())
	initial := time.Unix(1800000000, 0)
	event := initial.Add(time.Hour)
	setResponseHeadersAt(ctx, http.Header{"X-Codex-Primary-Used-Percent": []string{"80"}, "X-Codex-Primary-Reset-After-Seconds": []string{"60"}}, initial)
	mergeResponseHeadersAt(ctx, http.Header{"X-Request-Id": []string{"later"}, "X-Codex-Secondary-Used-Percent": []string{"10"}}, event)
	headers, times := GetResponseHeadersWithCaptureTimes(ctx)
	if headers.Get("X-Codex-Primary-Used-Percent") != "80" || !times["X-Codex-Primary-Used-Percent"].Equal(initial) || !times["X-Codex-Primary-Reset-After-Seconds"].Equal(initial) || !times["X-Codex-Secondary-Used-Percent"].Equal(event) {
		t.Fatalf("capture times lost: %v", times)
	}
	times["X-Codex-Primary-Used-Percent"] = event
	headers.Set("X-Codex-Primary-Used-Percent", "0")
	nextHeaders, nextTimes := GetResponseHeadersWithCaptureTimes(ctx)
	if nextHeaders.Get("X-Codex-Primary-Used-Percent") != "80" || !nextTimes["X-Codex-Primary-Used-Percent"].Equal(initial) {
		t.Fatal("snapshot shares mutable storage")
	}
	fresh := WithFreshResponseHeadersHolder(ctx)
	if headers, times := GetResponseHeadersWithCaptureTimes(fresh); len(headers) != 0 || len(times) != 0 {
		t.Fatal("capture times leaked into next attempt")
	}
	setResponseHeadersAt(ctx, http.Header{"X-Codex-Primary-Used-Percent": []string{"20"}}, event)
	_, times = GetResponseHeadersWithCaptureTimes(ctx)
	if len(times) != 1 || !times["X-Codex-Primary-Used-Percent"].Equal(event) {
		t.Fatal("replacement kept old capture times")
	}
}
