package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"golang.org/x/net/http2"
)

func TestStreamEndKind(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		err  error
		want string
	}{
		{nil, "clean_eof_before_terminal"},
		{fmt.Errorf("body: %w", io.ErrUnexpectedEOF), "unexpected_eof"},
		{http2.StreamError{Code: http2.ErrCodeCancel}, "http2_CANCEL"},
		{fmt.Errorf("read failed"), "read_error"},
	} {
		if got := streamEndKind(ctx, tt.err); got != tt.want {
			t.Fatalf("got %q, want %q", got, tt.want)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := streamEndKind(cancelled, nil); got != "downstream_cancelled" {
		t.Fatal(got)
	}
}

func TestIncompleteResponseStreamWarningsRespectDownstreamCancellation(t *testing.T) {
	logger := logrus.StandardLogger()
	previousLevel := logger.GetLevel()
	previousHooks := logger.ReplaceHooks(make(logrus.LevelHooks))
	logger.SetLevel(logrus.WarnLevel)
	hook := test.NewLocal(logger)
	t.Cleanup(func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
	})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer expire()
	response := &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/2.0", Header: http.Header{"X-Request-Id": {"upstream-request"}}}
	for _, tt := range []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{name: "downstream cancelled", ctx: cancelled, err: context.Canceled},
		{name: "downstream deadline expired", ctx: expired, err: context.DeadlineExceeded},
		{name: "cancelled during clean EOF", ctx: cancelled},
		{name: "upstream clean EOF", ctx: context.Background(), want: "clean_eof_before_terminal"},
		{name: "upstream truncated body", ctx: context.Background(), err: io.ErrUnexpectedEOF, want: "unexpected_eof"},
		{name: "upstream HTTP2 cancellation", ctx: context.Background(), err: http2.StreamError{Code: http2.ErrCodeCancel}, want: "http2_CANCEL"},
		{name: "upstream cancellation with active downstream", ctx: context.Background(), err: context.Canceled, want: "read_error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hook.Reset()
			LogIncompleteResponseStream(tt.ctx, response, tt.err, time.Now())
			entries := hook.AllEntries()
			if tt.want == "" {
				if len(entries) != 0 {
					t.Fatalf("downstream cancellation emitted an upstream warning: %+v", entries)
				}
				return
			}
			if len(entries) != 1 || entries[0].Level != logrus.WarnLevel || entries[0].Data["stream_end"] != tt.want {
				t.Fatalf("expected one %q upstream warning, got %+v", tt.want, entries)
			}
			if entries[0].Data["upstream_request_id"] != "upstream-request" {
				t.Fatalf("upstream warning lost its request ID: %+v", entries[0].Data)
			}
		})
	}
}
