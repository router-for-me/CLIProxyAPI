// Package ws provides an enhanced WebSocket handler for cliproxy++.
//
// Features:
//   - Bidirectional streaming (full-duplex)
//   - Message-based framing (JSON)
//   - Connection pooling
//   - Auto-reconnect support
//   - Per-message compression (optional)
package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

const (
	// Endpoint is the default WebSocket endpoint
	Endpoint = "/ws"

	// Message types
	TypeChat       = "chat"
	TypeStream     = "stream"
	TypeStreamChunk = "stream_chunk"
	TypeStreamEnd   = "stream_end"
	TypeError       = "error"
	TypePing        = "ping"
	TypePong        = "pong"
	TypeStatus      = "status"
)

// Message represents a WebSocket message
type Message struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// ChatPayload represents a chat completion request
type ChatPayload struct {
	Model       string              `json:"model"`
	Messages    []map[string]string `json:"messages"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Temperature float64             `json:"temperature,omitempty"`
	Stream      bool                `json:"stream,omitempty"`
}

// StreamChunk represents a streaming response chunk
type StreamChunk struct {
	Content string `json:"content,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Error   string `json:"error,omitempty"`
}

// HandlerConfig holds WebSocket handler configuration
type HandlerConfig struct {
	ReadBufferSize    int           `yaml:"read_buffer_size" json:"read_buffer_size"`
	WriteBufferSize   int           `yaml:"write_buffer_size" json:"write_buffer_size"`
	PingInterval      time.Duration `yaml:"ping_interval" json:"ping_interval"`
	PongWait          time.Duration `yaml:"pong_wait" json:"pong_wait"`
	MaxMessageSize    int64         `yaml:"max_message_size" json:"max_message_size"`
	Compression       bool          `yaml:"compression" json:"compression"`
}

// DefaultHandlerConfig returns default configuration
func DefaultHandlerConfig() HandlerConfig {
	return HandlerConfig{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		PingInterval:    20 * time.Second,
		PongWait:        60 * time.Second,
		MaxMessageSize:  10 * 1024 * 1024, // 10MB
		Compression:     false,
	}
}

// Upgrader creates a WebSocket upgrader with the given config
func Upgrader(cfg HandlerConfig) *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  cfg.ReadBufferSize,
		WriteBufferSize: cfg.WriteBufferSize,
		CheckOrigin: func(r *http.Request) bool {
			// Allow all origins for development
			// In production, implement proper origin checking
			return true
		},
		EnableCompression: cfg.Compression,
	}
}

// Session represents an active WebSocket connection
type Session struct {
	ID        string
	conn      *websocket.Conn
	config    HandlerConfig
	mu        sync.Mutex
	lastPong  time.Time
	connected atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewSession creates a new WebSocket session
func NewSession(id string, conn *websocket.Conn, cfg HandlerConfig) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:       id,
		conn:     conn,
		config:   cfg,
		lastPong: time.Now(),
		ctx:      ctx,
		cancel:   cancel,
	}
	s.connected.Store(true)
	return s
}

// Send sends a message to the client
func (s *Session) Send(msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.connected.Load() {
		return fmt.Errorf("session disconnected")
	}

	return s.conn.WriteJSON(msg)
}

// SendRaw sends raw bytes to the client
func (s *Session) SendRaw(data []byte, messageType int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.connected.Load() {
		return fmt.Errorf("session disconnected")
	}

	return s.conn.WriteMessage(messageType, data)
}

// Receive receives a message from the client
func (s *Session) Receive() (Message, error) {
	var msg Message
	err := s.conn.ReadJSON(&msg)
	if err != nil {
		return msg, err
	}
	return msg, nil
}

// Close closes the session
func (s *Session) Close() error {
	s.connected.Store(false)
	s.cancel()
	return s.conn.Close()
}

// IsConnected returns whether the session is connected
func (s *Session) IsConnected() bool {
	return s.connected.Load()
}

// Handler handles WebSocket connections
type Handler struct {
	config    HandlerConfig
	upgrader  *websocket.Upgrader
	sessions  sync.Map // map[string]*Session
	processor MessageProcessor
}

// MessageProcessor processes incoming WebSocket messages
type MessageProcessor interface {
	ProcessChat(ctx context.Context, session *Session, payload ChatPayload) error
	ProcessStream(ctx context.Context, session *Session, payload ChatPayload) (<-chan StreamChunk, error)
}

// NewHandler creates a new WebSocket handler
func NewHandler(config HandlerConfig, processor MessageProcessor) *Handler {
	return &Handler{
		config:    config,
		upgrader:  Upgrader(config),
		processor: processor,
	}
}

// ServeHTTP handles HTTP requests (WebSocket upgrade)
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Upgrade to WebSocket
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Debug("WebSocket upgrade failed")
		return
	}

	// Create session
	sessionID := generateSessionID()
	session := NewSession(sessionID, conn, h.config)
	h.sessions.Store(sessionID, session)
	defer func() {
		h.sessions.Delete(sessionID)
		session.Close()
	}()

	log.WithField("session", sessionID).Info("WebSocket session started")

	// Start ping/pong handler
	go h.pingPongHandler(session)

	// Message loop
	for {
		// Set read deadline
		conn.SetReadDeadline(time.Now().Add(h.config.PongWait))

		// Read message
		msg, err := session.Receive()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.WithError(err).Debug("WebSocket read error")
			}
			break
		}

		// Handle message
		if err := h.handleMessage(session, msg); err != nil {
			log.WithError(err).WithField("type", msg.Type).Error("Failed to handle message")
			_ = session.Send(Message{
				ID:    msg.ID,
				Type:  TypeError,
				Error: err.Error(),
			})
		}
	}

	log.WithField("session", sessionID).Info("WebSocket session ended")
}

// handleMessage routes messages to appropriate handlers
func (h *Handler) handleMessage(session *Session, msg Message) error {
	switch msg.Type {
	case TypePing:
		return session.Send(Message{ID: msg.ID, Type: TypePong})

	case TypeChat, TypeStream:
		var payload ChatPayload
		if len(msg.Payload) > 0 {
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				return fmt.Errorf("invalid payload: %w", err)
			}
		}

		if msg.Type == TypeStream || payload.Stream {
			return h.handleStream(session, msg, payload)
		}
		return h.handleChat(session, msg, payload)

	case TypePong:
		session.lastPong = time.Now()
		return nil

	default:
		return fmt.Errorf("unknown message type: %s", msg.Type)
	}
}

// handleChat handles non-streaming chat requests
func (h *Handler) handleChat(session *Session, msg Message, payload ChatPayload) error {
	if h.processor == nil {
		return fmt.Errorf("no message processor configured")
	}

	ctx, cancel := context.WithTimeout(session.ctx, 60*time.Second)
	defer cancel()

	if err := h.processor.ProcessChat(ctx, session, payload); err != nil {
		return err
	}

	return nil
}

// handleStream handles streaming chat requests
func (h *Handler) handleStream(session *Session, msg Message, payload ChatPayload) error {
	if h.processor == nil {
		return fmt.Errorf("no message processor configured")
	}

	ctx, cancel := context.WithTimeout(session.ctx, 120*time.Second)
	defer cancel()

	chunkCh, err := h.processor.ProcessStream(ctx, session, payload)
	if err != nil {
		return err
	}

	// Stream chunks to client
	for chunk := range chunkCh {
		resp := Message{
			ID:   msg.ID,
			Type: TypeStreamChunk,
		}
		if chunk.Error != "" {
			resp.Type = TypeError
			resp.Error = chunk.Error
		}
		payload, _ := json.Marshal(chunk)
		resp.Payload = payload

		if err := session.Send(resp); err != nil {
			return err
		}

		if chunk.Done {
			break
		}
	}

	return nil
}

// pingPongHandler sends periodic pings to keep connection alive
func (h *Handler) pingPongHandler(session *Session) {
	ticker := time.NewTicker(h.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-session.ctx.Done():
			return
		case <-ticker.C:
			if !session.IsConnected() {
				return
			}

			// Check for stale connection
			if time.Since(session.lastPong) > h.config.PongWait {
				log.WithField("session", session.ID).Warn("WebSocket connection stale, closing")
				_ = session.Close()
				return
			}

			// Send ping
			if err := session.Send(Message{Type: TypePing}); err != nil {
				return
			}
		}
	}
}

// generateSessionID generates a unique session ID
func generateSessionID() string {
	return fmt.Sprintf("ws-%d", time.Now().UnixNano())
}

// GetSessionCount returns the number of active sessions
func (h *Handler) GetSessionCount() int {
	count := 0
	h.sessions.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}
