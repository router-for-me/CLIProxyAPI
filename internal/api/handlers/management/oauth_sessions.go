package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	oauthSessionTTL          = 10 * time.Minute
	oauthCompletedSessionTTL = time.Minute
	maxOAuthStateLength      = 128
)

const (
	oauthSessionSourceBuiltin = "builtin"
	oauthSessionSourcePlugin  = "plugin"
)

var (
	errInvalidOAuthState      = errors.New("invalid oauth state")
	errUnsupportedOAuthFlow   = errors.New("unsupported oauth provider")
	errOAuthSessionNotPending = errors.New("oauth session is not pending")
	errOAuthSessionExists     = errors.New("oauth session already exists")
)

type oauthSession struct {
	Provider  string
	Status    string
	Source    string
	Metadata  map[string]any
	Completed bool
	CreatedAt time.Time
	ExpiresAt time.Time
}

type oauthSessionStore struct {
	mu           sync.RWMutex
	ttl          time.Duration
	completedTTL time.Duration
	sessions     map[string]oauthSession
}

func newOAuthSessionStore(ttl time.Duration) *oauthSessionStore {
	if ttl <= 0 {
		ttl = oauthSessionTTL
	}
	completedTTL := oauthCompletedSessionTTL
	if ttl < completedTTL {
		completedTTL = ttl
	}
	return &oauthSessionStore{
		ttl:          ttl,
		completedTTL: completedTTL,
		sessions:     make(map[string]oauthSession),
	}
}

func (s *oauthSessionStore) purgeExpiredLocked(now time.Time) {
	for state, session := range s.sessions {
		if !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt) {
			delete(s.sessions, state)
		}
	}
}

func (s *oauthSessionStore) Register(state, provider string) {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if state == "" || provider == "" {
		return
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	s.sessions[state] = oauthSession{
		Provider:  provider,
		Status:    "",
		Source:    oauthSessionSourceBuiltin,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
}

func (s *oauthSessionStore) RegisterPlugin(state, provider string, metadata map[string]any) error {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if state == "" || provider == "" {
		return fmt.Errorf("%w: empty state or provider", errInvalidOAuthState)
	}
	if errState := ValidateOAuthState(state); errState != nil {
		return errState
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	if _, ok := s.sessions[state]; ok {
		return errOAuthSessionExists
	}
	s.sessions[state] = oauthSession{
		Provider:  provider,
		Status:    "",
		Source:    oauthSessionSourcePlugin,
		Metadata:  cloneOAuthSessionMetadata(metadata),
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	return nil
}

func (s *oauthSessionStore) SetError(state, message string) {
	state = strings.TrimSpace(state)
	message = strings.TrimSpace(message)
	if state == "" {
		return
	}
	if message == "" {
		message = "Authentication failed"
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok || session.Completed {
		return
	}
	session.Status = message
	session.ExpiresAt = now.Add(s.ttl)
	s.sessions[state] = session
}

func (s *oauthSessionStore) Complete(state string) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok {
		return
	}
	session.Status = ""
	session.Metadata = nil
	session.Completed = true
	session.ExpiresAt = now.Add(s.completedTTL)
	s.sessions[state] = session
}

func (s *oauthSessionStore) CompleteProvider(provider string, source string) int {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return 0
	}
	source = strings.TrimSpace(source)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	removed := 0
	for state, session := range s.sessions {
		if strings.EqualFold(session.Provider, provider) && (source == "" || session.Source == source) {
			session.Status = ""
			session.Metadata = nil
			session.Completed = true
			session.ExpiresAt = now.Add(s.completedTTL)
			s.sessions[state] = session
			removed++
		}
	}
	return removed
}

func (s *oauthSessionStore) Get(state string) (oauthSession, bool) {
	state = strings.TrimSpace(state)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	session.Metadata = cloneOAuthSessionMetadata(session.Metadata)
	return session, ok
}

func (s *oauthSessionStore) IsPending(state, provider string) bool {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok {
		return false
	}
	if session.Completed || session.Status != "" {
		return false
	}
	if provider == "" {
		return true
	}
	return strings.EqualFold(session.Provider, provider)
}

func cloneOAuthSessionMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

var oauthSessions = newOAuthSessionStore(oauthSessionTTL)

func RegisterOAuthSession(state, provider string) { oauthSessions.Register(state, provider) }

func RegisterPluginOAuthSession(state, provider string, metadata map[string]any) error {
	return oauthSessions.RegisterPlugin(state, provider, metadata)
}

func SetOAuthSessionError(state, message string) { oauthSessions.SetError(state, message) }

func CompleteOAuthSession(state string) { oauthSessions.Complete(state) }

func CompleteOAuthSessionsByProvider(provider string) int {
	return oauthSessions.CompleteProvider(provider, oauthSessionSourceBuiltin)
}

func CompletePluginOAuthSessionsByProvider(provider string) int {
	return oauthSessions.CompleteProvider(provider, oauthSessionSourcePlugin)
}

func GetOAuthSession(state string) (provider string, status string, ok bool) {
	session, ok := oauthSessions.Get(state)
	if !ok {
		return "", "", false
	}
	return session.Provider, session.Status, true
}

func GetOAuthSessionDetails(state string) (provider string, status string, isPlugin bool, metadata map[string]any, completed bool, ok bool) {
	session, ok := oauthSessions.Get(state)
	if !ok {
		return "", "", false, nil, false, false
	}
	return session.Provider, session.Status, session.Source == oauthSessionSourcePlugin, cloneOAuthSessionMetadata(session.Metadata), session.Completed, true
}

func IsOAuthSessionPending(state, provider string) bool {
	return oauthSessions.IsPending(state, provider)
}

func oauthSessionErrorWithCause(message string, cause error) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Authentication failed"
	}
	if cause == nil {
		return message
	}
	detail := strings.TrimSpace(cause.Error())
	if detail == "" {
		return message
	}
	return message + ": " + detail
}

func ValidateOAuthState(state string) error {
	trimmed := strings.TrimSpace(state)
	if trimmed == "" {
		return fmt.Errorf("%w: empty", errInvalidOAuthState)
	}
	if len(trimmed) > maxOAuthStateLength {
		return fmt.Errorf("%w: too long", errInvalidOAuthState)
	}
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return fmt.Errorf("%w: contains path separator", errInvalidOAuthState)
	}
	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("%w: contains '..'", errInvalidOAuthState)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("%w: invalid character", errInvalidOAuthState)
		}
	}
	return nil
}

func NormalizeOAuthProvider(provider string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "claude":
		return "anthropic", nil
	case "codex", "openai":
		return "codex", nil
	case "antigravity", "anti-gravity":
		return "antigravity", nil
	case "xai", "x-ai", "x.ai", "grok":
		return "xai", nil
	default:
		return "", errUnsupportedOAuthFlow
	}
}

func NormalizeOAuthCallbackProvider(provider string) (string, error) {
	if normalized, errNormalize := NormalizeOAuthProvider(provider); errNormalize == nil {
		return normalized, nil
	}
	return NormalizePluginOAuthCallbackProvider(provider)
}

func NormalizePluginOAuthCallbackProvider(provider string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(provider))
	if trimmed == "" {
		return "", errUnsupportedOAuthFlow
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return "", errUnsupportedOAuthFlow
		}
	}
	return trimmed, nil
}

func normalizeOAuthCallbackProviderForPendingSession(provider, state string) (string, error) {
	session, ok := oauthSessions.Get(state)
	if ok && session.Source == oauthSessionSourcePlugin {
		return NormalizePluginOAuthCallbackProvider(provider)
	}
	return NormalizeOAuthCallbackProvider(provider)
}

type oauthCallbackFilePayload struct {
	Code  string `json:"code"`
	State string `json:"state"`
	Error string `json:"error"`
}

func WriteOAuthCallbackFile(authDir, provider, state, code, errorMessage string) (string, error) {
	canonicalProvider, err := NormalizeOAuthCallbackProvider(provider)
	if err != nil {
		return "", err
	}
	return writeOAuthCallbackFile(authDir, canonicalProvider, state, code, errorMessage)
}

func writeOAuthCallbackFile(authDir, canonicalProvider, state, code, errorMessage string) (string, error) {
	if strings.TrimSpace(authDir) == "" {
		return "", fmt.Errorf("auth dir is empty")
	}
	canonicalProvider = strings.TrimSpace(canonicalProvider)
	if canonicalProvider == "" {
		return "", errUnsupportedOAuthFlow
	}
	if err := ValidateOAuthState(state); err != nil {
		return "", err
	}

	fileName := fmt.Sprintf(".oauth-%s-%s.oauth", canonicalProvider, state)
	filePath := filepath.Join(authDir, fileName)
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return "", fmt.Errorf("create oauth callback dir: %w", err)
	}
	payload := oauthCallbackFilePayload{
		Code:  strings.TrimSpace(code),
		State: strings.TrimSpace(state),
		Error: strings.TrimSpace(errorMessage),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal oauth callback payload: %w", err)
	}
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		return "", fmt.Errorf("write oauth callback file: %w", err)
	}
	return filePath, nil
}

func WriteOAuthCallbackFileForPendingSession(authDir, provider, state, code, errorMessage string) (string, error) {
	canonicalProvider, err := normalizeOAuthCallbackProviderForPendingSession(provider, state)
	if err != nil {
		return "", err
	}
	if !IsOAuthSessionPending(state, canonicalProvider) {
		return "", errOAuthSessionNotPending
	}
	return writeOAuthCallbackFile(authDir, canonicalProvider, state, code, errorMessage)
}
