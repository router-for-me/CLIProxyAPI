package wsrelay

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Manager exposes a websocket endpoint that proxies Gemini requests to
// connected clients.
type Manager struct {
	path      string
	upgrader  websocket.Upgrader
	sessions  map[string]*session
	sessMutex sync.RWMutex

	providerFactory func(*http.Request) (string, error)
	onConnected     func(string)
	onDisconnected  func(string, error)

	logDebugf func(string, ...any)
	logInfof  func(string, ...any)
	logWarnf  func(string, ...any)
}

// Options configures a Manager instance.
type Options struct {
	Path            string
	ProviderFactory func(*http.Request) (string, error)
	OnConnected     func(string)
	OnDisconnected  func(string, error)
	LogDebugf       func(string, ...any)
	LogInfof        func(string, ...any)
	LogWarnf        func(string, ...any)
}

// NewManager builds a websocket relay manager with the supplied options.
func NewManager(opts Options) *Manager {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = "/v1/ws"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	mgr := &Manager{
		path:     path,
		sessions: make(map[string]*session),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		providerFactory: opts.ProviderFactory,
		onConnected:     opts.OnConnected,
		onDisconnected:  opts.OnDisconnected,
		logDebugf:       opts.LogDebugf,
		logInfof:        opts.LogInfof,
		logWarnf:        opts.LogWarnf,
	}
	if mgr.logDebugf == nil {
		mgr.logDebugf = func(string, ...any) {}
	}
	if mgr.logInfof == nil {
		mgr.logInfof = func(string, ...any) {}
	}
	if mgr.logWarnf == nil {
		mgr.logWarnf = func(s string, args ...any) { fmt.Printf(s+"\n", args...) }
	}
	return mgr
}

// Path returns the HTTP path the manager expects for websocket upgrades.
func (m *Manager) Path() string {
	if m == nil {
		return "/v1/ws"
	}
	return m.path
}

// Handler exposes an http.Handler that upgrades connections to websocket sessions.
func (m *Manager) Handler() http.Handler {
	return http.HandlerFunc(m.handleWebsocket)
}

// Stop gracefully closes all active websocket sessions.
func (m *Manager) Stop(_ context.Context) error {
	m.sessMutex.Lock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, sess := range m.sessions {
		sessions = append(sessions, sess)
	}
	m.sessions = make(map[string]*session)
	m.sessMutex.Unlock()

	for _, sess := range sessions {
		if sess != nil {
			sess.cleanup(errors.New("wsrelay: manager stopped"))
		}
	}
	return nil
}

// handleWebsocket upgrades the connection and wires the session into the pool.
func (m *Manager) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	expectedPath := m.Path()
	if expectedPath != "" && r.URL != nil && r.URL.Path != expectedPath {
		http.NotFound(w, r)
		return
	}
	if !strings.EqualFold(r.Method, http.MethodGet) {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		m.logWarnf("wsrelay: upgrade failed: %v", err)
		return
	}
	s := newSession(conn, m, randomProviderName())
	if m.providerFactory != nil {
		name, err := m.providerFactory(r)
		if err != nil {
			s.cleanup(err)
			return
		}
		if strings.TrimSpace(name) != "" {
			s.provider = strings.ToLower(name)
		}
	}
	if s.provider == "" {
		s.provider = strings.ToLower(s.id)
	}
	m.sessMutex.Lock()
	var replaced *session
	if existing, ok := m.sessions[s.provider]; ok {
		replaced = existing
	}
	m.sessions[s.provider] = s
	m.sessMutex.Unlock()

	if replaced != nil {
		replaced.cleanup(errors.New("replaced by new connection"))
	}
	if m.onConnected != nil {
		m.onConnected(s.provider)
	}

	go s.run(context.Background())
}

// Send forwards the message to the specific provider connection and returns a channel
// yielding response messages.
func (m *Manager) Send(ctx context.Context, provider string, msg Message) (<-chan Message, error) {
	s := m.session(provider)
	if s == nil {
		return nil, fmt.Errorf("wsrelay: provider %s not connected", provider)
	}
	return s.request(ctx, msg)
}

func (m *Manager) session(provider string) *session {
	key := strings.ToLower(strings.TrimSpace(provider))
	m.sessMutex.RLock()
	s := m.sessions[key]
	m.sessMutex.RUnlock()
	return s
}

func (m *Manager) handleSessionClosed(s *session, cause error) {
	if s == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(s.provider))
	m.sessMutex.Lock()
	if cur, ok := m.sessions[key]; ok && cur == s {
		delete(m.sessions, key)
	}
	m.sessMutex.Unlock()
	if m.onDisconnected != nil {
		m.onDisconnected(s.provider, cause)
	}
}

func randomProviderName() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("aistudio-%x", time.Now().UnixNano())
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return "aistudio-" + string(buf)
}
