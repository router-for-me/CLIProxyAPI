package wsrelay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newTestWebsocketPair(t *testing.T) (*session, *websocket.Conn, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	var serverConn *websocket.Conn
	var serverSess *session
	ready := make(chan struct{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade error: %v", err)
			return
		}
		serverConn = conn
		serverSess = &session{
			conn:   conn,
			closed: make(chan struct{}),
		}
		close(ready)
	}))

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	clientConn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		ts.Close()
		t.Fatalf("dial error: %v", errDial)
	}
	<-ready

	cleanup := func() {
		_ = clientConn.Close()
		if serverSess != nil {
			serverSess.cleanup(errClosed)
		}
		if serverConn != nil {
			_ = serverConn.Close()
		}
		ts.Close()
	}

	return serverSess, clientConn, cleanup
}

func TestPendingRequest_ConcurrentSendAndClose(t *testing.T) {
	for iter := 0; iter < 1000; iter++ {
		s := &session{
			closed: make(chan struct{}),
		}
		reqID := fmt.Sprintf("req-%d", iter)
		req := newPendingRequest(context.Background())
		s.pending.Store(reqID, req)

		var wg sync.WaitGroup
		wg.Add(3)

		// Producer
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				s.dispatch(Message{
					ID:   reqID,
					Type: MessageTypeStreamChunk,
					Payload: map[string]any{
						"seq": i,
					},
				})
			}
		}()

		// Consumer
		go func() {
			defer wg.Done()
			for range req.ch {
			}
		}()

		// Deleter / closer
		go func() {
			defer wg.Done()
			if actual, loaded := s.pending.LoadAndDelete(reqID); loaded {
				actual.(*pendingRequest).close()
			}
		}()

		wg.Wait()
	}
}

func TestSession_Cleanup_RaceSyncMap(t *testing.T) {
	s := &session{
		closed: make(chan struct{}),
	}
	var stop int32
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&stop) == 0 {
			req := newPendingRequest(context.Background())
			s.pending.Store("key", req)
			s.pending.Load("key")
			s.pending.Delete("key")
			req.cancel()
		}
	}()

	for i := 0; i < 50; i++ {
		s.cleanup(errClosed)
	}

	atomic.StoreInt32(&stop, 1)
	wg.Wait()
}

func TestSession_Request_NoGoroutineLeakOnCompletion(t *testing.T) {
	sess, clientConn, cleanup := newTestWebsocketPair(t)
	defer cleanup()

	// Consume messages on client side and immediately reply with a terminal response.
	go func() {
		for {
			var msg Message
			if err := clientConn.ReadJSON(&msg); err != nil {
				return
			}
			sess.dispatch(Message{
				ID:   msg.ID,
				Type: MessageTypeHTTPResp,
			})
		}
	}()

	const count = 30
	for i := 0; i < count; i++ {
		reqID := fmt.Sprintf("req-leak-%d", i)
		ch, errReq := sess.request(context.Background(), Message{ID: reqID, Type: MessageTypeHTTPReq})
		if errReq != nil {
			t.Fatalf("request error: %v", errReq)
		}
		// Drain channel completely until closed.
		for range ch {
		}
	}
}

func TestSession_Dispatch_NoSilentFrameLoss(t *testing.T) {
	s := &session{
		closed: make(chan struct{}),
	}
	reqID := "stream-slow-consumer"
	req := newPendingRequest(context.Background())
	s.pending.Store(reqID, req)

	// Send 10 chunks followed by terminal message
	for i := 0; i < 10; i++ {
		s.dispatch(Message{
			ID:   reqID,
			Type: MessageTypeStreamChunk,
			Payload: map[string]any{
				"chunk": i,
			},
		})
	}
	s.dispatch(Message{
		ID:   reqID,
		Type: MessageTypeStreamEnd,
	})

	var received []Message
	for msg := range req.ch {
		received = append(received, msg)
	}

	if len(received) != 11 {
		t.Fatalf("frame loss: expected 11 frames, got %d", len(received))
	}
}

func TestSession_Cleanup_DeliversErrorFrameWhenBufferFull(t *testing.T) {
	s := &session{
		closed: make(chan struct{}),
	}
	reqID := "req-err-cleanup-full"
	req := newPendingRequest(context.Background())
	s.pending.Store(reqID, req)

	// Fill channel to buffer capacity
	for i := 0; i < pendingChannelBuffer; i++ {
		s.dispatch(Message{
			ID:   reqID,
			Type: MessageTypeStreamChunk,
			Payload: map[string]any{
				"seq": i,
			},
		})
	}

	s.cleanup(errClosed)

	var msgs []Message
	for msg := range req.ch {
		msgs = append(msgs, msg)
	}

	if len(msgs) == 0 {
		t.Fatalf("expected messages on cleanup, got 0")
	}
	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Type != MessageTypeError {
		t.Fatalf("expected last message to be MessageTypeError, got %s", lastMsg.Type)
	}
}

func TestSession_Dispatch_TerminalDeliveredWhenBufferFullAndSessionClosing(t *testing.T) {
	s := &session{
		closed: make(chan struct{}),
	}
	reqID := "req-terminal-full"
	req := newPendingRequest(context.Background())
	s.pending.Store(reqID, req)

	// Fill channel to buffer capacity
	for i := 0; i < pendingChannelBuffer; i++ {
		s.dispatch(Message{
			ID:   reqID,
			Type: MessageTypeStreamChunk,
			Payload: map[string]any{
				"seq": i,
			},
		})
	}

	// Dispatch terminal message
	s.dispatch(Message{
		ID:   reqID,
		Type: MessageTypeStreamEnd,
	})

	var msgs []Message
	for msg := range req.ch {
		msgs = append(msgs, msg)
	}

	if len(msgs) == 0 {
		t.Fatalf("expected messages, got 0")
	}
	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Type != MessageTypeStreamEnd {
		t.Fatalf("expected last message to be MessageTypeStreamEnd, got %s", lastMsg.Type)
	}
}

func TestSession_SlowConsumer_UnblocksOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{
		closed: make(chan struct{}),
	}
	reqID := "req-ctx-cancel"
	req := newPendingRequest(ctx)
	sess.pending.Store(reqID, req)

	// Fill channel to buffer capacity
	for i := 0; i < pendingChannelBuffer; i++ {
		sess.dispatch(Message{ID: reqID, Type: MessageTypeStreamChunk})
	}

	enteredDispatch := make(chan struct{})
	doneDispatch := make(chan struct{})
	go func() {
		close(enteredDispatch)
		sess.dispatch(Message{ID: reqID, Type: MessageTypeStreamChunk})
		close(doneDispatch)
	}()

	<-enteredDispatch
	// Small yield to ensure the goroutine is in deliver's select
	time.Sleep(10 * time.Millisecond)

	// Cancel context to unblock saturated dispatch
	cancel()

	select {
	case <-doneDispatch:
		// Succeeded in unblocking
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("dispatch remained blocked after context cancellation")
	}
}

func TestSession_SlowConsumer_UnblocksOnSessionClose(t *testing.T) {
	sess := &session{
		closed: make(chan struct{}),
	}
	reqID := "req-sess-close"
	req := newPendingRequest(context.Background())
	sess.pending.Store(reqID, req)

	// Fill channel to buffer capacity
	for i := 0; i < pendingChannelBuffer; i++ {
		sess.dispatch(Message{ID: reqID, Type: MessageTypeStreamChunk})
	}

	enteredDispatch := make(chan struct{})
	doneDispatch := make(chan struct{})
	go func() {
		close(enteredDispatch)
		sess.dispatch(Message{ID: reqID, Type: MessageTypeStreamChunk})
		close(doneDispatch)
	}()

	<-enteredDispatch
	// Small yield to ensure the goroutine is in deliver's select
	time.Sleep(10 * time.Millisecond)

	// Close session to unblock saturated dispatch
	sess.cleanup(errClosed)

	select {
	case <-doneDispatch:
		// Succeeded in unblocking
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("dispatch remained blocked after session cleanup")
	}
}
