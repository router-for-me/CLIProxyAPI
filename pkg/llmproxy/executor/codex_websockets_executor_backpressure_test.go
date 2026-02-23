package executor

import (
	"context"
	"errors"
	"testing"
)

func TestEnqueueCodexWebsocketReadPrioritizesErrorUnderBackpressure(t *testing.T) {
	ch := make(chan codexWebsocketRead, 1)
	ch <- codexWebsocketRead{msgType: 1, payload: []byte("stale")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wantErr := errors.New("upstream disconnected")
	enqueueCodexWebsocketRead(ch, ctx.Done(), codexWebsocketRead{err: wantErr})

	got := <-ch
	if !errors.Is(got.err, wantErr) {
		t.Fatalf("expected buffered error to be preserved, got err=%v payload=%q", got.err, string(got.payload))
	}
}

func TestEnqueueCodexWebsocketReadDoneClosedSkipsEnqueue(t *testing.T) {
	ch := make(chan codexWebsocketRead, 1)
	stale := codexWebsocketRead{msgType: 1, payload: []byte("stale")}
	ch <- stale

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	enqueueCodexWebsocketRead(ch, ctx.Done(), codexWebsocketRead{err: errors.New("should not enqueue")})

	got := <-ch
	if string(got.payload) != string(stale.payload) || got.msgType != stale.msgType || got.err != nil {
		t.Fatalf("expected channel state unchanged when done closed, got %+v", got)
	}
}
