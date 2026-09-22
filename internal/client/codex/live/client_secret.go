package live

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	ClientSecretSessionContextKey   = "codexLiveClientSecretSession"
	ClientSecretPrincipalContextKey = "codexLiveClientSecretPrincipal"
	clientSecretPrefix              = "ek_"
	clientSecretDefaultLifetime     = 10 * time.Minute
	clientSecretMinimumLifetime     = 10 * time.Second
	clientSecretMaximumLifetime     = 2 * time.Hour
	clientSecretMaxBodySize         = 64 << 10
	clientSecretMaxEntries          = 1024
	clientSecretMaxEntriesPerIssuer = 64
)

var (
	errInvalidClientSecret    = errors.New("Realtime client secret is invalid or expired")
	errClientSecretCapacity   = errors.New("Realtime client secret capacity exhausted")
	errUnsupportedSessionType = errors.New("Realtime session type is not supported")
)

// ClientSecretAuthorization contains the local session configuration associated with an ephemeral key.
type ClientSecretAuthorization struct {
	Principal       string
	IssuerPrincipal string
	IssuerProvider  string
	Session         json.RawMessage
}

type clientSecretEntry struct {
	authorization ClientSecretAuthorization
	expiresAt     time.Time
}

type clientSecretStore struct {
	mu      sync.Mutex
	entries map[string]clientSecretEntry
	now     func() time.Time
}

type clientSecretCreateRequest struct {
	Session      json.RawMessage `json:"session"`
	ExpiresAfter *struct {
		Anchor  string `json:"anchor"`
		Seconds int64  `json:"seconds"`
	} `json:"expires_after,omitempty"`
}

type clientSecretCreateResponse struct {
	Value     string          `json:"value"`
	ExpiresAt int64           `json:"expires_at"`
	Session   json.RawMessage `json:"session"`
}

func newClientSecretStore() *clientSecretStore {
	return &clientSecretStore{
		entries: make(map[string]clientSecretEntry),
		now:     time.Now,
	}
}

func (s *clientSecretStore) create(session json.RawMessage, lifetime time.Duration, issuerPrincipal, issuerProvider string) (string, ClientSecretAuthorization, time.Time, error) {
	if s == nil {
		return "", ClientSecretAuthorization{}, time.Time{}, errors.New("Realtime client secret store unavailable")
	}
	token, errToken := randomRealtimeID(clientSecretPrefix, 32)
	if errToken != nil {
		return "", ClientSecretAuthorization{}, time.Time{}, errToken
	}
	sessionID, errSessionID := randomRealtimeID("sess_", 18)
	if errSessionID != nil {
		return "", ClientSecretAuthorization{}, time.Time{}, errSessionID
	}
	authorization := ClientSecretAuthorization{
		Principal:       sessionID,
		IssuerPrincipal: strings.TrimSpace(issuerPrincipal),
		IssuerProvider:  strings.TrimSpace(issuerProvider),
		Session:         append(json.RawMessage(nil), session...),
	}
	now := s.currentTime()
	expiresAt := now.Add(lifetime)
	s.mu.Lock()
	s.removeExpiredLocked(now)
	if len(s.entries) >= clientSecretMaxEntries {
		s.mu.Unlock()
		return "", ClientSecretAuthorization{}, time.Time{}, errClientSecretCapacity
	}
	if authorization.IssuerPrincipal != "" {
		issuerEntries := 0
		for _, entry := range s.entries {
			if entry.authorization.IssuerPrincipal == authorization.IssuerPrincipal && entry.authorization.IssuerProvider == authorization.IssuerProvider {
				issuerEntries++
			}
		}
		if issuerEntries >= clientSecretMaxEntriesPerIssuer {
			s.mu.Unlock()
			return "", ClientSecretAuthorization{}, time.Time{}, errClientSecretCapacity
		}
	}
	s.entries[token] = clientSecretEntry{authorization: authorization, expiresAt: expiresAt}
	s.mu.Unlock()
	return token, authorization, expiresAt, nil
}

func (s *clientSecretStore) authenticate(token string) (ClientSecretAuthorization, error) {
	if s == nil || !strings.HasPrefix(token, clientSecretPrefix) {
		return ClientSecretAuthorization{}, errInvalidClientSecret
	}
	now := s.currentTime()
	s.mu.Lock()
	entry, ok := s.entries[token]
	if !ok || !entry.expiresAt.After(now) {
		delete(s.entries, token)
		s.mu.Unlock()
		return ClientSecretAuthorization{}, errInvalidClientSecret
	}
	s.mu.Unlock()
	entry.authorization.Session = append(json.RawMessage(nil), entry.authorization.Session...)
	return entry.authorization, nil
}

func (s *clientSecretStore) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.entries)
	s.mu.Unlock()
}

func (s *clientSecretStore) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *clientSecretStore) removeExpiredLocked(now time.Time) {
	for token, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, token)
		}
	}
}

func readClientSecretBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	payload, errRead := io.ReadAll(io.LimitReader(body, clientSecretMaxBodySize+1))
	if errRead != nil {
		return nil, fmt.Errorf("failed to read Realtime client secret request: %w", errRead)
	}
	if len(payload) > clientSecretMaxBodySize {
		return nil, errBodyTooLarge
	}
	return payload, nil
}

func randomRealtimeID(prefix string, size int) (string, error) {
	payload := make([]byte, size)
	if _, errRead := rand.Read(payload); errRead != nil {
		return "", fmt.Errorf("generate Realtime identifier: %w", errRead)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// AuthenticateClientSecret validates a local ephemeral key when the request carries one.
func (h *Handler) AuthenticateClientSecret(request *http.Request) (ClientSecretAuthorization, bool, error) {
	token := bearerToken(request)
	if !strings.HasPrefix(token, clientSecretPrefix) {
		return ClientSecretAuthorization{}, false, nil
	}
	if h == nil || h.clientSecrets == nil {
		return ClientSecretAuthorization{}, true, errInvalidClientSecret
	}
	authorization, errAuthenticate := h.clientSecrets.authenticate(token)
	return authorization, true, errAuthenticate
}

func bearerToken(request *http.Request) string {
	if request == nil {
		return ""
	}
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	const bearerPrefix = "Bearer "
	if len(authorization) < len(bearerPrefix) || !strings.EqualFold(authorization[:len(bearerPrefix)], bearerPrefix) {
		return ""
	}
	return strings.TrimSpace(authorization[len(bearerPrefix):])
}

// CreateClientSecret creates a short-lived credential scoped to this proxy.
func (h *Handler) CreateClientSecret(c *gin.Context) {
	if h == nil || h.clientSecrets == nil {
		writeRealtimeError(c, http.StatusServiceUnavailable, "Realtime client secret service unavailable", "server_error", "realtime_client_secret_unavailable")
		return
	}
	body, errRead := readClientSecretBody(c.Request.Body)
	if errRead != nil {
		status := http.StatusBadRequest
		if errors.Is(errRead, errBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeRealtimeError(c, status, errRead.Error(), "invalid_request_error", "invalid_request")
		return
	}
	var request clientSecretCreateRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if errUnmarshal := json.Unmarshal(body, &request); errUnmarshal != nil {
			writeRealtimeError(c, http.StatusBadRequest, "Invalid Realtime client secret request", "invalid_request_error", "invalid_request")
			return
		}
	}
	h.createClientSecret(c, request.Session, request.ExpiresAfter, false)
}

// CreateLegacySession implements the deprecated Realtime session credential endpoint.
func (h *Handler) CreateLegacySession(c *gin.Context) {
	if h == nil || h.clientSecrets == nil {
		writeRealtimeError(c, http.StatusServiceUnavailable, "Realtime client secret service unavailable", "server_error", "realtime_client_secret_unavailable")
		return
	}
	body, errRead := readClientSecretBody(c.Request.Body)
	if errRead != nil {
		status := http.StatusBadRequest
		if errors.Is(errRead, errBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeRealtimeError(c, status, errRead.Error(), "invalid_request_error", "invalid_request")
		return
	}
	h.createClientSecret(c, json.RawMessage(body), nil, true)
}

func (h *Handler) createClientSecret(c *gin.Context, session json.RawMessage, expiresAfter *struct {
	Anchor  string `json:"anchor"`
	Seconds int64  `json:"seconds"`
}, legacy bool) {
	lifetime, errLifetime := clientSecretLifetime(expiresAfter)
	if errLifetime != nil {
		writeRealtimeError(c, http.StatusBadRequest, errLifetime.Error(), "invalid_request_error", "invalid_expires_after")
		return
	}
	clientSession, upstreamSession, errSession := normalizeClientSecretSession(session)
	if errSession != nil {
		if errors.Is(errSession, errUnsupportedSessionType) {
			writeRealtimeError(c, http.StatusNotImplemented, errSession.Error(), "not_supported_error", "realtime_capability_not_supported")
			return
		}
		writeRealtimeError(c, http.StatusBadRequest, errSession.Error(), "invalid_request_error", "invalid_session")
		return
	}
	issuerPrincipal, _ := c.Get("userApiKey")
	issuerProvider, _ := c.Get("accessProvider")
	issuerPrincipalValue, _ := issuerPrincipal.(string)
	issuerProviderValue, _ := issuerProvider.(string)
	token, authorization, expiresAt, errCreate := h.clientSecrets.create(upstreamSession, lifetime, issuerPrincipalValue, issuerProviderValue)
	if errCreate != nil {
		if errors.Is(errCreate, errClientSecretCapacity) {
			c.Header("Retry-After", "1")
			writeRealtimeError(c, http.StatusTooManyRequests, errCreate.Error(), "rate_limit_error", "realtime_client_secret_capacity_exhausted")
			return
		}
		writeRealtimeError(c, http.StatusInternalServerError, "Failed to create Realtime client secret", "server_error", "realtime_client_secret_failed")
		return
	}
	responseSession, errResponse := realtimeSessionResponse(clientSession, authorization.Principal, expiresAt)
	if errResponse != nil {
		writeRealtimeError(c, http.StatusInternalServerError, "Failed to encode Realtime session", "server_error", "realtime_session_failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	if legacy {
		var response map[string]any
		if errUnmarshal := json.Unmarshal(responseSession, &response); errUnmarshal != nil {
			writeRealtimeError(c, http.StatusInternalServerError, "Failed to encode Realtime session", "server_error", "realtime_session_failed")
			return
		}
		response["client_secret"] = gin.H{"value": token, "expires_at": expiresAt.Unix()}
		c.JSON(http.StatusOK, response)
		return
	}
	c.JSON(http.StatusOK, clientSecretCreateResponse{
		Value:     token,
		ExpiresAt: expiresAt.Unix(),
		Session:   responseSession,
	})
}

func clientSecretLifetime(expiresAfter *struct {
	Anchor  string `json:"anchor"`
	Seconds int64  `json:"seconds"`
}) (time.Duration, error) {
	if expiresAfter == nil {
		return clientSecretDefaultLifetime, nil
	}
	if expiresAfter.Anchor != "" && expiresAfter.Anchor != "created_at" {
		return 0, errors.New("expires_after.anchor must be created_at")
	}
	minimumSeconds := int64(clientSecretMinimumLifetime / time.Second)
	maximumSeconds := int64(clientSecretMaximumLifetime / time.Second)
	if expiresAfter.Seconds < minimumSeconds || expiresAfter.Seconds > maximumSeconds {
		return 0, fmt.Errorf("expires_after.seconds must be between %d and %d", minimumSeconds, maximumSeconds)
	}
	return time.Duration(expiresAfter.Seconds) * time.Second, nil
}

func normalizeClientSecretSession(session json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	trimmedSession := strings.TrimSpace(string(session))
	if trimmedSession == "" || trimmedSession == "null" {
		session = json.RawMessage(`{"type":"realtime","model":"gpt-realtime"}`)
	}
	var clientSession map[string]any
	if errUnmarshal := json.Unmarshal(session, &clientSession); errUnmarshal != nil || clientSession == nil {
		return nil, nil, errors.New("session must be a valid JSON object")
	}
	sessionType, _ := clientSession["type"].(string)
	if strings.TrimSpace(sessionType) == "" {
		sessionType = "realtime"
		clientSession["type"] = sessionType
	}
	if sessionType != "realtime" {
		return nil, nil, fmt.Errorf("%w by the Codex OAuth upstream: %q", errUnsupportedSessionType, sessionType)
	}
	model, _ := clientSession["model"].(string)
	if strings.TrimSpace(model) == "" {
		model = "gpt-realtime"
		clientSession["model"] = model
	}
	clientEncoded, errMarshal := json.Marshal(clientSession)
	if errMarshal != nil {
		return nil, nil, fmt.Errorf("encode Realtime session: %w", errMarshal)
	}
	clientSession["model"] = codexRealtimeModel(model)
	upstreamEncoded, errMarshal := json.Marshal(clientSession)
	if errMarshal != nil {
		return nil, nil, fmt.Errorf("encode Codex Realtime session: %w", errMarshal)
	}
	return clientEncoded, upstreamEncoded, nil
}

func realtimeSessionResponse(session json.RawMessage, sessionID string, expiresAt time.Time) (json.RawMessage, error) {
	var response map[string]any
	if errUnmarshal := json.Unmarshal(session, &response); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	response["id"] = sessionID
	response["object"] = "realtime.session"
	response["expires_at"] = expiresAt.Unix()
	return json.Marshal(response)
}

func codexRealtimeModel(model string) string {
	trimmed := strings.TrimSpace(model)
	lower := strings.ToLower(trimmed)
	if lower == "" || lower == "gpt-realtime" || strings.HasPrefix(lower, "gpt-realtime-") || strings.Contains(lower, "realtime-preview") {
		return defaultLiveModel
	}
	return trimmed
}

func liveSelectionHeaders(c *gin.Context) http.Header {
	if c == nil || c.Request == nil {
		return make(http.Header)
	}
	headers := c.Request.Header.Clone()
	if _, ok := c.Get(ClientSecretPrincipalContextKey); ok {
		headers.Del("Authorization")
		headers.Del("Proxy-Authorization")
	}
	return headers
}

func requestOwner(c *gin.Context) (string, string) {
	if c == nil {
		return "", ""
	}
	principalValue, _ := c.Get("userApiKey")
	providerValue, _ := c.Get("accessProvider")
	principal, _ := principalValue.(string)
	provider, _ := providerValue.(string)
	return strings.TrimSpace(principal), strings.TrimSpace(provider)
}

func clientSecretSession(c *gin.Context) json.RawMessage {
	if c == nil {
		return nil
	}
	value, ok := c.Get(ClientSecretSessionContextKey)
	if !ok {
		return nil
	}
	session, _ := value.(json.RawMessage)
	return append(json.RawMessage(nil), session...)
}

func writeRealtimeError(c *gin.Context, status int, message, errorType, code string) {
	c.JSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    errorType,
		"param":   nil,
		"code":    code,
	}})
}
