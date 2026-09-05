package wsrelay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const (
	readTimeout          = 60 * time.Second
	writeTimeout         = 10 * time.Second
	maxInboundMessageLen = 64 << 20 // 64 MiB
	heartbeatInterval    = 30 * time.Second
	pendingChannelBuffer = 64
)

var errClosed = errors.New("websocket session closed")

type pendingRequest struct {
	mu         sync.Mutex
	ch         chan Message
	closed     bool
	terminal   bool
	done       chan struct{}
	reqCtx     context.Context
	stopCancel func() bool
}

func newPendingRequest(ctx context.Context) *pendingRequest {
	return &pendingRequest{
		ch:     make(chan Message, pendingChannelBuffer),
		done:   make(chan struct{}),
		reqCtx: ctx,
	}
}

func (pr *pendingRequest) ensureInitializedLocked(ctx context.Context) {
	if pr.done == nil {
		pr.done = make(chan struct{})
	}
	if pr.ch == nil {
		pr.ch = make(chan Message, pendingChannelBuffer)
	}
	if pr.reqCtx == nil && ctx != nil {
		pr.reqCtx = ctx
	}
}

func (pr *pendingRequest) deliver(sessClosed <-chan struct{}, msg Message) bool {
	if pr == nil {
		return false
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.ensureInitializedLocked(nil)
	if pr.closed || pr.terminal {
		return false
	}

	var ctxDone <-chan struct{}
	if pr.reqCtx != nil {
		ctxDone = pr.reqCtx.Done()
	}

	select {
	case <-pr.done:
		return false
	case <-sessClosed:
		return false
	case <-ctxDone:
		return false
	case pr.ch <- msg:
		return true
	}
}

func (pr *pendingRequest) deliverTerminal(sessClosed <-chan struct{}, msg Message) bool {
	if pr == nil {
		return false
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.ensureInitializedLocked(nil)
	if pr.closed {
		return false
	}
	pr.terminal = true

	select {
	case pr.ch <- msg:
		return true
	default:
		select {
		case <-pr.ch:
		default:
		}
		select {
		case pr.ch <- msg:
			return true
		default:
			return false
		}
	}
}

func (pr *pendingRequest) setStopCancel(stop func() bool) {
	if pr == nil {
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.stopCancel = stop
}

func (pr *pendingRequest) cancel() {
	if pr == nil {
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.ensureInitializedLocked(nil)
	if pr.closed {
		return
	}
	pr.closed = true
	if pr.stopCancel != nil {
		pr.stopCancel()
	}
	select {
	case <-pr.done:
	default:
		close(pr.done)
	}
	close(pr.ch)
}

func (pr *pendingRequest) cancelWithError(cause error) {
	if pr == nil {
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.ensureInitializedLocked(nil)
	if pr.closed {
		return
	}
	pr.closed = true
	if pr.stopCancel != nil {
		pr.stopCancel()
	}
	select {
	case <-pr.done:
	default:
		close(pr.done)
	}
	if !pr.terminal && cause != nil {
		msg := Message{Type: MessageTypeError, Payload: map[string]any{"error": cause.Error()}}
		select {
		case pr.ch <- msg:
		default:
			select {
			case <-pr.ch:
			default:
			}
			select {
			case pr.ch <- msg:
			default:
			}
		}
	}
	close(pr.ch)
}

func (pr *pendingRequest) close() {
	if pr == nil {
		return
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.ensureInitializedLocked(nil)
	if pr.closed {
		return
	}
	pr.closed = true
	if pr.stopCancel != nil {
		pr.stopCancel()
	}
	close(pr.ch)
}

type session struct {
	conn       *websocket.Conn
	manager    *Manager
	provider   string
	id         string
	closed     chan struct{}
	closeOnce  sync.Once
	writeMutex sync.Mutex
	pending    sync.Map // map[string]*pendingRequest
}

func newSession(conn *websocket.Conn, mgr *Manager, id string) *session {
	s := &session{
		conn:     conn,
		manager:  mgr,
		provider: "",
		id:       id,
		closed:   make(chan struct{}),
	}
	conn.SetReadLimit(maxInboundMessageLen)
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})
	s.startHeartbeat()
	return s
}

func (s *session) startHeartbeat() {
	if s == nil || s.conn == nil {
		return
	}
	ticker := time.NewTicker(heartbeatInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-s.closed:
				return
			case <-ticker.C:
				s.writeMutex.Lock()
				err := s.conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(writeTimeout))
				s.writeMutex.Unlock()
				if err != nil {
					s.cleanup(err)
					return
				}
			}
		}
	}()
}

func (s *session) run(ctx context.Context) {
	defer s.cleanup(errClosed)
	for {
		var msg Message
		if err := s.conn.ReadJSON(&msg); err != nil {
			s.cleanup(err)
			return
		}
		s.dispatch(msg)
	}
}

func (s *session) dispatch(msg Message) {
	if msg.Type == MessageTypePing {
		_ = s.send(context.Background(), Message{ID: msg.ID, Type: MessageTypePong})
		return
	}
	if value, ok := s.pending.Load(msg.ID); ok {
		req := value.(*pendingRequest)
		isTerminal := msg.Type == MessageTypeHTTPResp || msg.Type == MessageTypeError || msg.Type == MessageTypeStreamEnd
		if isTerminal {
			req.deliverTerminal(s.closed, msg)
			if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
				actualReq := actual.(*pendingRequest)
				actualReq.close()
			}
		} else {
			req.deliver(s.closed, msg)
		}
		return
	}
	if msg.Type == MessageTypeHTTPResp || msg.Type == MessageTypeError || msg.Type == MessageTypeStreamEnd {
		if s.manager != nil {
			s.manager.logDebugf("wsrelay: received terminal message for unknown id %s (provider=%s)", msg.ID, s.provider)
		}
	}
}

func (s *session) send(ctx context.Context, msg Message) error {
	select {
	case <-s.closed:
		return errClosed
	default:
	}
	s.writeMutex.Lock()
	defer s.writeMutex.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if err := s.conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

func (s *session) request(ctx context.Context, msg Message) (<-chan Message, error) {
	if msg.ID == "" {
		return nil, fmt.Errorf("wsrelay: message id is required")
	}
	req := newPendingRequest(ctx)
	if _, loaded := s.pending.LoadOrStore(msg.ID, req); loaded {
		req.close()
		return nil, fmt.Errorf("wsrelay: duplicate message id %s", msg.ID)
	}
	if ctx != nil {
		stop := context.AfterFunc(ctx, func() {
			if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
				actual.(*pendingRequest).cancel()
			}
		})
		req.setStopCancel(stop)
	}
	if err := s.send(ctx, msg); err != nil {
		if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
			req := actual.(*pendingRequest)
			req.close()
		}
		return nil, err
	}
	return req.ch, nil
}

func (s *session) cleanup(cause error) {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.pending.Range(func(key, value any) bool {
			if actual, loaded := s.pending.LoadAndDelete(key); loaded {
				req := actual.(*pendingRequest)
				req.cancelWithError(cause)
			}
			return true
		})
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.manager != nil {
			s.manager.handleSessionClosed(s, cause)
		}
	})
}
