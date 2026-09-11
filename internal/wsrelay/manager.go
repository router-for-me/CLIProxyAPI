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
	// keyLocks serializes auth emissions per provider key: an install's
	// onConnected Add and an owner disconnect's onDisconnected Delete each
	// validate ownership and emit while holding the key's lock, so a stale
	// emission can never land after a newer session's Delete.
	keyLocks map[string]*sync.Mutex

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
		keyLocks: make(map[string]*sync.Mutex),
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
	// The key lock serializes this install's ownership check and Add emission
	// against the previous session's disconnect Delete and any competing
	// install, so the last emission for a key always belongs to its live
	// session (#5392, #5520 review).
	lock := m.keyLock(s.provider)
	lock.Lock()
	m.sessMutex.Lock()
	var replaced *session
	if existing, ok := m.sessions[s.provider]; ok {
		replaced = existing
	}
	m.sessions[s.provider] = s
	m.sessMutex.Unlock()

	// Suppress a stale connect: if a newer session already replaced this one,
	// its own onConnected is authoritative and this Add must not fire. The
	// check is under the key lock, so the emission is atomic with ownership.
	if m.onConnected != nil && m.session(s.provider) == s {
		m.onConnected(s.provider)
	}
	lock.Unlock()

	if replaced != nil {
		replaced.cleanup(errors.New("replaced by new connection"))
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

// keyLock returns the per-provider mutex that serializes ownership validation
// with auth-emission callbacks, keeping Add/Delete ordering deterministic per
// provider key without holding the global session lock during callbacks.
func (m *Manager) keyLock(provider string) *sync.Mutex {
	key := strings.ToLower(strings.TrimSpace(provider))
	m.sessMutex.Lock()
	if m.keyLocks == nil {
		m.keyLocks = make(map[string]*sync.Mutex)
	}
	l := m.keyLocks[key]
	if l == nil {
		l = &sync.Mutex{}
		m.keyLocks[key] = l
	}
	m.sessMutex.Unlock()
	return l
}

func (m *Manager) handleSessionClosed(s *session, cause error) {
	if s == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(s.provider))
	// A session that no longer owns the key was replaced by a live one: the
	// replacement's own emissions are authoritative and this callback emits
	// nothing, so it needs no per-key serialization.
	m.sessMutex.Lock()
	cur, owned := m.sessions[key]
	owner := owned && cur == s
	if owner {
		delete(m.sessions, key)
	} else if owned {
		cause = errors.New("replaced by new connection")
	}
	m.sessMutex.Unlock()
	if !owner {
		if m.onDisconnected != nil {
			m.onDisconnected(s.provider, cause)
		}
		return
	}
	// Owner disconnect: validate ownership and emit the Delete under the key
	// lock so it can never follow a newer session's Add (#5392, #5520 review).
	// The callback runs while holding the per-key lock, not the global session
	// lock, so same-key manager calls from the callback must not be made.
	lock := m.keyLock(key)
	lock.Lock()
	m.sessMutex.Lock()
	if cur, ok := m.sessions[key]; ok && cur == s {
		delete(m.sessions, key)
	} else if ok {
		cause = errors.New("replaced by new connection")
	}
	m.sessMutex.Unlock()
	if m.onDisconnected != nil {
		m.onDisconnected(s.provider, cause)
	}
	lock.Unlock()
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
