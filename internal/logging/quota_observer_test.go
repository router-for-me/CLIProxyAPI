package logging

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestQuotaObserverWorksWithoutResponseHeaderHolder(t *testing.T) {
	ObserveQuota(nil, http.Header{"X-Codex-Primary-Used-Percent": {"10"}}, "api_response_headers")
	ObserveQuota(WithQuotaObserver(nil, nil), http.Header{"X-Codex-Primary-Used-Percent": {"10"}}, "api_response_headers")
	var calls int
	ctx := WithQuotaObserver(context.Background(), func(headers http.Header, at time.Time, source string) {
		calls++
		if headers.Get("X-Codex-Primary-Used-Percent") != "10" || at.IsZero() || source != "api_response_headers" {
			t.Fatal("incorrect observer input")
		}
	})
	SetResponseHeaders(ctx, http.Header{"X-Codex-Primary-Used-Percent": {"10"}})
	if calls != 1 {
		t.Fatal("observer depends on request logging or response holder")
	}
}
