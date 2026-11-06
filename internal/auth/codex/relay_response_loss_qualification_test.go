package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type qualificationLostBody struct{}

func (*qualificationLostBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (*qualificationLostBody) Close() error             { return nil }

func TestRelayQualificationAmbiguousRefreshMustNotReplay(t *testing.T) {
	for _, stage := range []string{"accepted-response-read", "accepted-json-decode", "ambiguous-transport"} {
		t.Run(stage, func(t *testing.T) {
			resetCodexRefreshGroupForTest()
			defer resetCodexRefreshGroupForTest()
			var calls atomic.Int32
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if stage == "ambiguous-transport" {
					return nil, errors.New("synthetic reset after request acceptance unknown")
				}
				var body io.ReadCloser = &qualificationLostBody{}
				if stage == "accepted-json-decode" {
					body = io.NopCloser(strings.NewReader(`{"access_token":`))
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: req}, nil
			})}}
			_, err := auth.RefreshTokensWithRetry(context.Background(), "synthetic-one-use-refresh-token", 3)
			if err == nil {
				t.Fatal("expected an ambiguous refresh error")
			}
			t.Logf("OBSERVED_REFRESH_TRANSPORT_CALLS=%d", calls.Load())
			if calls.Load() != 1 {
				t.Fatalf("one-use refresh replay: got %d transport calls, want 1", calls.Load())
			}
		})
	}
}
