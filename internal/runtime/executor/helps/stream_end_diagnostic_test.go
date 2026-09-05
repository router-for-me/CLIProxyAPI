package helps

import (
	"context"
	"fmt"
	"io"
	"testing"

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
