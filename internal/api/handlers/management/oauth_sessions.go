package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	// oauthSessionTTL must cover device-code flows (xAI ~30m, Kimi ~15m).
	oauthSessionTTL          = 30 * time.Minute
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

// ErrSIWCCallbackRequiresV8 prevents legacy callback sinks from accepting SIWC codes.
var ErrSIWCCallbackRequiresV8 = fmt.Errorf("%w: SIWC callbacks require /v8/management/oauth/callback", errOAuthSessionNotPending)

type oauthRecoveryTarget struct {
	AuthID           string
	FileName         string
	Provider         string
	Email            string
	AccountID        string
	VerifyAfterLogin bool
	VerifyModel      string
}

type oauthSession struct {
	Provider        string
	Status          string
	Source          string
	ProxyURL        string
	Metadata        map[string]any
	SIWC            *siwcBindSession
	Target          *oauthRecoveryTarget
	AuthID          string
	SavedGrantHash  string
	Saved           bool
	Adopted         bool
	BaselineExisted bool
	BaselineVersion uint64
	BaselineEpoch   uint64
	AdoptedVersion  uint64
	AdoptedEpoch    uint64
	AdoptError      string
	VerifyError     string
	Committing      bool
	SIWCCommitting  bool
	Completed       bool
	CreatedAt       time.Time
	ExpiresAt       time.Time
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
		if (session.Completed || (!session.Committing && !session.SIWCCommitting)) && !session.ExpiresAt.IsZero() && now.After(session.ExpiresAt) {
			if session.SIWC != nil {
				session.SIWC.cancel()
			}
			delete(s.sessions, state)
		}
	}
}

func (s *oauthSessionStore) Register(state, provider string) {
	s.RegisterWithProxy(state, provider, "")
}

func (s *oauthSessionStore) RegisterWithProxy(state, provider, proxyURL string) {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	proxyURL = strings.TrimSpace(proxyURL)
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
		ProxyURL:  proxyURL,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
}

func (s *oauthSessionStore) RegisterPlugin(state, provider string, metadata map[string]any) error {
	return s.RegisterPluginWithProxy(state, provider, metadata, "")
}

func (s *oauthSessionStore) RegisterPluginWithProxy(state, provider string, metadata map[string]any, proxyURL string) error {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	proxyURL = strings.TrimSpace(proxyURL)
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
		ProxyURL:  proxyURL,
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
	if !ok || session.Completed || session.Committing {
		return
	}
	session.Status = redactSessionProxyInString(message, session.ProxyURL)
	session.ProxyURL = ""
	session.SIWCCommitting = false
	session.ExpiresAt = now.Add(s.ttl)
	s.sessions[state] = session
}

func (s *oauthSessionStore) FailCommit(state, message string) {
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
	session.Status = redactSessionProxyInString(message, session.ProxyURL)
	session.ProxyURL = ""
	session.Committing = false
	session.SIWCCommitting = false
	session.ExpiresAt = now.Add(s.ttl)
	s.sessions[state] = session
}

func (s *oauthSessionStore) FailPendingPoll(state, message string) (errStatus string, completed bool, committing bool, ok bool) {
	state = strings.TrimSpace(state)
	message = strings.TrimSpace(message)
	if state == "" {
		return "", false, false, false
	}
	if message == "" {
		message = "Authentication failed"
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, exists := s.sessions[state]
	if !exists {
		return "", false, false, false
	}
	if session.Completed {
		return "", true, false, true
	}
	if session.Committing || session.SIWCCommitting {
		return "", false, true, true
	}
	if session.Status != "" {
		return session.Status, false, false, true
	}
	session.Status = redactSessionProxyInString(message, session.ProxyURL)
	session.ProxyURL = ""
	session.ExpiresAt = now.Add(s.ttl)
	s.sessions[state] = session
	return session.Status, false, false, true
}

func (s *oauthSessionStore) Complete(state string) {
	s.CompleteWithAuth(state, "")
}

func (s *oauthSessionStore) CompleteWithAuth(state, authID string) {
	s.CompleteWithOutcome(state, authID, "", strings.TrimSpace(authID) != "", strings.TrimSpace(authID) != "", false, 0, 0, 0, 0, "", "")
}

func (s *oauthSessionStore) CompleteWithOutcome(state, authID, savedGrantHash string, saved, adopted, baselineExisted bool, baselineVersion, baselineEpoch, adoptedVersion, adoptedEpoch uint64, adoptErr, verifyErr string) {
	state = strings.TrimSpace(state)
	authID = strings.TrimSpace(authID)
	if state == "" {
		return
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok || session.Completed {
		return
	}
	session.Status = ""
	session.ProxyURL = ""
	session.Metadata = nil
	session.AuthID = authID
	session.SavedGrantHash = strings.TrimSpace(savedGrantHash)
	session.Saved = saved
	session.Adopted = adopted
	session.BaselineExisted = baselineExisted
	session.BaselineVersion = baselineVersion
	session.BaselineEpoch = baselineEpoch
	session.AdoptedVersion = adoptedVersion
	session.AdoptedEpoch = adoptedEpoch
	session.AdoptError = strings.TrimSpace(adoptErr)
	session.VerifyError = strings.TrimSpace(verifyErr)
	session.Committing = false
	session.SIWCCommitting = false
	session.Completed = true
	session.ExpiresAt = now.Add(s.completedTTL)
	s.sessions[state] = session
}

func (s *oauthSessionStore) SetTarget(state string, target oauthRecoveryTarget) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[state]
	if !ok || session.Completed {
		return
	}
	copyTarget := target
	session.Target = &copyTarget
	s.sessions[state] = session
}

func (s *oauthSessionStore) MarkAdopted(state string, adoptedVersion, adoptedEpoch uint64) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[state]
	if !ok || !session.Completed {
		return
	}
	session.Adopted = true
	session.AdoptedVersion = adoptedVersion
	session.AdoptedEpoch = adoptedEpoch
	session.AdoptError = ""
	s.sessions[state] = session
}

func (s *oauthSessionStore) GetTarget(state string) (oauthRecoveryTarget, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return oauthRecoveryTarget{}, false
	}
	session, ok := s.Get(state)
	if !ok || session.Target == nil {
		return oauthRecoveryTarget{}, false
	}
	return *session.Target, true
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
		if session.SIWC == nil && !session.Completed && !session.Committing && !session.SIWCCommitting && strings.EqualFold(session.Provider, provider) && (source == "" || session.Source == source) {
			session.Status = ""
			session.ProxyURL = ""
			session.Metadata = nil
			session.Committing = false
			session.SIWCCommitting = false
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

func (s *oauthSessionStore) claimCommit(state, provider string) bool {
	state = strings.TrimSpace(state)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if state == "" {
		return false
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok || session.Completed || session.Status != "" || session.Committing || session.SIWCCommitting {
		return false
	}
	if provider != "" && !strings.EqualFold(session.Provider, provider) {
		return false
	}
	session.Committing = true
	s.sessions[state] = session
	return true
}

// Cancel removes a pending OAuth session so background waiters exit without saving credentials.
// Returns true when a pending session was cancelled.
func (s *oauthSessionStore) Cancel(state string) bool {
	state = strings.TrimSpace(state)
	if state == "" {
		return false
	}
	// SIWC completion and cancellation share a per-session gate. A cancellation
	// that wins this gate prevents persistence; after a committed bind it is a no-op.
	if session, ok := s.Get(state); ok && session.SIWC != nil {
		session.SIWC.mu.Lock()
		defer session.SIWC.mu.Unlock()
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	session, ok := s.sessions[state]
	if !ok || session.Completed || session.Status != "" || session.Committing || session.SIWCCommitting {
		return false
	}
	if session.SIWC != nil {
		session.SIWC.cancel()
		session.SIWC.pkce = nil
	}
	delete(s.sessions, state)
	return true
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

func RegisterOAuthSessionWithProxy(state, provider, proxyURL string) {
	oauthSessions.RegisterWithProxy(state, provider, proxyURL)
}

func RegisterPluginOAuthSession(state, provider string, metadata map[string]any) error {
	return oauthSessions.RegisterPlugin(state, provider, metadata)
}

func RegisterPluginOAuthSessionWithProxy(state, provider string, metadata map[string]any, proxyURL string) error {
	return oauthSessions.RegisterPluginWithProxy(state, provider, metadata, proxyURL)
}

func GetOAuthSessionProxyURL(state string) string {
	session, ok := oauthSessions.Get(state)
	if !ok || session.Completed || session.Status != "" {
		return ""
	}
	return strings.TrimSpace(session.ProxyURL)
}

func redactSessionProxyInString(text, proxyURL string) string {
	proxyURL = strings.TrimSpace(proxyURL)
	if text == "" || proxyURL == "" {
		return text
	}
	redacted := proxyutil.Redact(proxyURL)
	if redacted != "" && redacted != proxyURL {
		text = strings.ReplaceAll(text, proxyURL, redacted)
	}
	if parsed, errParse := url.Parse(proxyURL); errParse == nil && parsed != nil && parsed.User != nil {
		if userInfo := parsed.User.String(); userInfo != "" {
			text = strings.ReplaceAll(text, userInfo, "redacted")
		}
		if password, hasPass := parsed.User.Password(); hasPass && password != "" {
			text = strings.ReplaceAll(text, password, "redacted")
		}
	}
	return text
}

func SetOAuthSessionError(state, message string) { oauthSessions.SetError(state, message) }

func FailOAuthSessionCommit(state, message string) { oauthSessions.FailCommit(state, message) }

func CompleteOAuthSession(state string) { oauthSessions.Complete(state) }

func CompleteOAuthSessionWithAuth(state, authID string) {
	oauthSessions.CompleteWithAuth(state, authID)
}

func CompleteOAuthSessionWithOutcome(state, authID, savedGrantHash string, saved, adopted, baselineExisted bool, baselineVersion, baselineEpoch, adoptedVersion, adoptedEpoch uint64, adoptErr, verifyErr string) {
	oauthSessions.CompleteWithOutcome(state, authID, savedGrantHash, saved, adopted, baselineExisted, baselineVersion, baselineEpoch, adoptedVersion, adoptedEpoch, adoptErr, verifyErr)
}

func SetOAuthSessionTarget(state string, target oauthRecoveryTarget) {
	oauthSessions.SetTarget(state, target)
}

func GetOAuthSessionTarget(state string) (oauthRecoveryTarget, bool) {
	return oauthSessions.GetTarget(state)
}

func CompleteOAuthSessionsByProvider(provider string) int {
	return oauthSessions.CompleteProvider(provider, oauthSessionSourceBuiltin)
}

func CompletePluginOAuthSessionsByProvider(provider string) int {
	return oauthSessions.CompleteProvider(provider, oauthSessionSourcePlugin)
}

func GetOAuthSession(state string) (provider string, status string, ok bool) {
	session, ok := oauthSessions.Get(state)
	if !ok || session.Completed {
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

// guardOAuthSessionPendingForSave returns errOAuthSessionNotPending when the session
// is no longer pending (cancelled, completed, errored, or expired).
// Call immediately before persisting credentials so a cancel that races with token
// exchange or metadata fetch cannot save credentials for a cancelled flow.
func guardOAuthSessionPendingForSave(state, provider string) error {
	if IsOAuthSessionPending(state, provider) {
		return nil
	}
	return errOAuthSessionNotPending
}

// claimOAuthSessionCommit atomically verifies that the session is still pending
// and transitions it into the committing state so a concurrent CancelOAuthSession
// cannot report true while credentials are being persisted.
func claimOAuthSessionCommit(state, provider string) error {
	if oauthSessions.claimCommit(state, provider) {
		return nil
	}
	return errOAuthSessionNotPending
}

// CancelOAuthSession cancels a pending OAuth session by state.
// Background callback and device-code waiters observe IsOAuthSessionPending as false and exit without saving credentials.
func CancelOAuthSession(state string) bool {
	return oauthSessions.Cancel(state)
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
	case "devin", "cognition":
		return "devin", nil
	case "meta", "muse":
		return "meta", nil
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
	// Publish complete JSON atomically so background waiters cannot read a partial callback.
	tmp, errCreate := os.CreateTemp(authDir, ".oauth-callback-*")
	if errCreate != nil {
		return "", fmt.Errorf("create oauth callback file: %w", errCreate)
	}
	tmpPath := tmp.Name()
	defer func() {
		if errRemove := os.Remove(tmpPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warn("failed to remove temporary OAuth callback file")
		}
	}()
	_, errWrite := tmp.Write(data)
	errClose := tmp.Close()
	if errWrite != nil {
		return "", fmt.Errorf("write oauth callback file: %w", errWrite)
	}
	if errClose != nil {
		return "", fmt.Errorf("close oauth callback file: %w", errClose)
	}
	if errRename := os.Rename(tmpPath, filePath); errRename != nil {
		return "", fmt.Errorf("publish oauth callback file: %w", errRename)
	}
	return filePath, nil
}

func WriteOAuthCallbackFileForPendingSession(authDir, provider, state, code, errorMessage string) (string, error) {
	canonicalProvider, err := normalizeOAuthCallbackProviderForPendingSession(provider, state)
	if err != nil {
		return "", err
	}
	if session, ok := oauthSessions.Get(state); ok && session.SIWC != nil {
		return "", ErrSIWCCallbackRequiresV8
	}
	if !IsOAuthSessionPending(state, canonicalProvider) {
		return "", errOAuthSessionNotPending
	}
	return writeOAuthCallbackFile(authDir, canonicalProvider, state, code, errorMessage)
}
