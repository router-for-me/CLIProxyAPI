package wsrelay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	readTimeout          = 60 * time.Second
	writeTimeout         = 10 * time.Second
	maxInboundMessageLen = 64 << 20 // 64 MiB
	heartbeatInterval    = 30 * time.Second
)

var errClosed = errors.New("websocket session closed")

type pendingRequest struct {
	ch        chan Message
	closeOnce sync.Once
}

func (pr *pendingRequest) close() {
	if pr == nil {
		return
	}
	pr.closeOnce.Do(func() {
		close(pr.ch)
	})
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
		select {
		case req.ch <- msg:
		default:
		}
		if msg.Type == MessageTypeHTTPResp || msg.Type == MessageTypeError || msg.Type == MessageTypeStreamEnd {
			if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
				actual.(*pendingRequest).close()
			}
		}
		return
	}
	if msg.Type == MessageTypeHTTPResp || msg.Type == MessageTypeError || msg.Type == MessageTypeStreamEnd {
		s.manager.logDebugf("wsrelay: received terminal message for unknown id %s (provider=%s)", msg.ID, s.provider)
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
	if err := s.conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

func (s *session) request(ctx context.Context, msg Message) (<-chan Message, error) {
	if msg.ID == "" {
		return nil, fmt.Errorf("wsrelay: message id is required")
	}
	if _, loaded := s.pending.LoadOrStore(msg.ID, &pendingRequest{ch: make(chan Message, 8)}); loaded {
		return nil, fmt.Errorf("wsrelay: duplicate message id %s", msg.ID)
	}
	value, _ := s.pending.Load(msg.ID)
	req := value.(*pendingRequest)
	if err := s.send(ctx, msg); err != nil {
		if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
			req := actual.(*pendingRequest)
			req.close()
		}
		return nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
			if actual, loaded := s.pending.LoadAndDelete(msg.ID); loaded {
				actual.(*pendingRequest).close()
			}
		case <-s.closed:
		}
	}()
	return req.ch, nil
}

func (s *session) cleanup(cause error) {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.pending.Range(func(key, value any) bool {
			req := value.(*pendingRequest)
			msg := Message{ID: key.(string), Type: MessageTypeError, Payload: map[string]any{"error": cause.Error()}}
			select {
			case req.ch <- msg:
			default:
			}
			req.close()
			return true
		})
		s.pending = sync.Map{}
		_ = s.conn.Close()
		if s.manager != nil {
			s.manager.handleSessionClosed(s, cause)
		}
	})
}
