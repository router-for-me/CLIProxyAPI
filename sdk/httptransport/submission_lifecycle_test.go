package httptransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type terminalCloseBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *terminalCloseBody) Close() error { b.closed.Store(true); return nil }

func TestSubmissionGuardTerminalClosesNextBody(t *testing.T) {
	ctx := WithSubmissionTracking(context.Background())
	state, _ := ctx.Value(submissionKey{}).(*submissionState)
	state.fail(errors.New("accepted disconnect"))
	body := &terminalCloseBody{Reader: strings.NewReader("fallback")}
	request, errNew := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid", body)
	if errNew != nil {
		t.Fatal(errNew)
	}
	transport := &submissionTransport{state: state}
	_, errRoundTrip := transport.RoundTrip(request)
	if !IsOutcomeUnknown(errRoundTrip) || !body.closed.Load() {
		t.Fatalf("error=%v bodyClosed=%t", errRoundTrip, body.closed.Load())
	}
}
