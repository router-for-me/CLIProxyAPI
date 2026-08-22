package usercontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

type requestPrincipalContextKey struct{}

type limiterState struct {
	windowStart time.Time
	requests    int
	inFlight    int
}

// Service joins persistent account data with the small amount of live state needed for rate limits.
type Service struct {
	repository Repository
	mu         sync.Mutex
	limits     map[string]*limiterState
	now        func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, limits: make(map[string]*limiterState), now: time.Now}
}

func (s *Service) Identifier() string { return "managed-users" }

func (s *Service) Repository() Repository {
	if s == nil {
		return nil
	}
	return s.repository
}

// Authenticate implements the normal access-provider contract. The quota middleware has already
// verified managed keys, so this path usually performs no second database lookup.
func (s *Service) Authenticate(ctx context.Context, request *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if principal, ok := principalFromContext(ctx); ok {
		return principal.accessResult(s.Identifier()), nil
	}
	raw := credentialFromRequest(request)
	if !strings.HasPrefix(raw, managedKeyPrefix) {
		return nil, sdkaccess.NewNotHandledError()
	}
	principal, err := s.authenticateRaw(ctx, raw)
	if err != nil {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return principal.accessResult(s.Identifier()), nil
}

// Middleware rejects exhausted managed keys before the request reaches an upstream provider.
func (s *Service) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || s.repository == nil || skipManagedAuthentication(c.Request.URL.Path) {
			c.Next()
			return
		}
		raw := credentialFromRequest(c.Request)
		if !strings.HasPrefix(raw, managedKeyPrefix) {
			c.Next()
			return
		}

		principal, err := s.authenticateRaw(c.Request.Context(), raw)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or inactive API key"})
			return
		}
		if model := requestModel(c.Request); !modelAllowed(model, principal.Key.User.Limits.AllowedModels) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "model is not allowed for this user"})
			return
		}
		release, retryAfter, errAcquire := s.acquire(principal)
		if errAcquire != nil {
			if retryAfter > 0 {
				c.Header("Retry-After", fmt.Sprintf("%d", int(retryAfter.Round(time.Second)/time.Second)))
			}
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": errAcquire.Error()})
			return
		}
		defer release()

		ctx := context.WithValue(c.Request.Context(), requestPrincipalContextKey{}, principal)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

type requestPrincipal struct {
	Key StoredAPIKey
}

func (p requestPrincipal) accessResult(provider string) *sdkaccess.Result {
	return &sdkaccess.Result{
		Provider:  provider,
		Principal: principalPrefix + p.Key.ID,
		Metadata: map[string]string{
			"user_id": p.Key.UserID,
			"key_id":  p.Key.ID,
		},
	}
}

func principalFromContext(ctx context.Context) (requestPrincipal, bool) {
	if ctx == nil {
		return requestPrincipal{}, false
	}
	principal, ok := ctx.Value(requestPrincipalContextKey{}).(requestPrincipal)
	return principal, ok
}

func (s *Service) authenticateRaw(ctx context.Context, raw string) (requestPrincipal, error) {
	keyID, secret, ok := parseManagedKey(raw)
	if !ok {
		return requestPrincipal{}, ErrInvalidCredential
	}
	stored, err := s.repository.GetStoredAPIKey(ctx, keyID)
	if err != nil {
		return requestPrincipal{}, ErrInvalidCredential
	}
	digest := sha256.Sum256([]byte(secret))
	if len(stored.SecretHash) != len(digest) || subtle.ConstantTimeCompare(stored.SecretHash, digest[:]) != 1 {
		return requestPrincipal{}, ErrInvalidCredential
	}
	now := s.now()
	if stored.Status != KeyStatusActive || stored.User.Status != UserStatusActive {
		return requestPrincipal{}, ErrDisabled
	}
	if expired(stored.ExpiresAt, now) || expired(stored.User.ExpiresAt, now) {
		return requestPrincipal{}, ErrExpired
	}
	if limit := stored.User.Limits.MonthlyTokens; limit > 0 && stored.UsedTokens >= limit {
		return requestPrincipal{}, ErrQuotaExceeded
	}
	return requestPrincipal{Key: stored}, nil
}

func (s *Service) acquire(principal requestPrincipal) (func(), time.Duration, error) {
	limits := principal.Key.User.Limits
	now := s.now()

	s.mu.Lock()
	state := s.limits[principal.Key.ID]
	if state == nil {
		state = &limiterState{windowStart: now}
		s.limits[principal.Key.ID] = state
	}
	if now.Sub(state.windowStart) >= time.Minute {
		state.windowStart = now
		state.requests = 0
	}
	if limits.RequestsPerMinute > 0 && state.requests >= limits.RequestsPerMinute {
		retry := time.Minute - now.Sub(state.windowStart)
		s.mu.Unlock()
		return func() {}, retry, ErrRateLimited
	}
	if limits.ConcurrentRequests > 0 && state.inFlight >= limits.ConcurrentRequests {
		s.mu.Unlock()
		return func() {}, time.Second, ErrRateLimited
	}
	state.requests++
	state.inFlight++
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		if current := s.limits[principal.Key.ID]; current != nil && current.inFlight > 0 {
			current.inFlight--
		}
		s.mu.Unlock()
	}, 0, nil
}

func (s *Service) CreateUser(ctx context.Context, input CreateUserInput) (User, error) {
	now := s.now().UTC()
	status := normalizeStatus(input.Status, UserStatusActive)
	user := User{ID: uuid.NewString(), Name: strings.TrimSpace(input.Name), Email: strings.TrimSpace(input.Email), Status: status, Limits: normalizeLimits(input.Limits), ExpiresAt: input.ExpiresAt, CreatedAt: now, UpdatedAt: now}
	if user.Name == "" {
		return User{}, fmt.Errorf("name is required")
	}
	return s.repository.CreateUser(ctx, user)
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) { return s.repository.ListUsers(ctx) }
func (s *Service) GetUser(ctx context.Context, id string) (User, error) {
	return s.repository.GetUser(ctx, id)
}

func (s *Service) UpdateUser(ctx context.Context, id string, input UpdateUserInput) (User, error) {
	current, err := s.repository.GetUser(ctx, id)
	if err != nil {
		return User{}, err
	}
	current.Name = strings.TrimSpace(input.Name)
	current.Email = strings.TrimSpace(input.Email)
	current.Status = normalizeStatus(input.Status, current.Status)
	current.Limits = normalizeLimits(input.Limits)
	current.ExpiresAt = input.ExpiresAt
	current.UpdatedAt = s.now().UTC()
	if current.Name == "" {
		return User{}, fmt.Errorf("name is required")
	}
	return s.repository.UpdateUser(ctx, current)
}

func (s *Service) DeleteUser(ctx context.Context, id string) error {
	return s.repository.DeleteUser(ctx, id)
}

func (s *Service) CreateAPIKey(ctx context.Context, userID string, input CreateAPIKeyInput) (CreatedAPIKey, error) {
	if _, err := s.repository.GetUser(ctx, userID); err != nil {
		return CreatedAPIKey{}, err
	}
	keyID := uuid.NewString()
	secret, err := randomToken(32)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	digest := sha256.Sum256([]byte(secret))
	prefix := managedKeyPrefix + keyID + "_"
	key := APIKey{ID: keyID, UserID: userID, Name: strings.TrimSpace(input.Name), Prefix: prefix, Status: KeyStatusActive, ExpiresAt: input.ExpiresAt, CreatedAt: s.now().UTC()}
	if key.Name == "" {
		key.Name = "Default key"
	}
	stored, err := s.repository.CreateAPIKey(ctx, key, digest[:])
	if err != nil {
		return CreatedAPIKey{}, err
	}
	return CreatedAPIKey{APIKey: stored, Secret: prefix + secret}, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	return s.repository.ListAPIKeys(ctx, userID)
}

func (s *Service) RevokeAPIKey(ctx context.Context, id string) error {
	return s.repository.RevokeAPIKey(ctx, id)
}

func (s *Service) GetUsage(ctx context.Context, userID string) (UsageSummary, error) {
	return s.repository.GetUsage(ctx, userID, monthStart(s.now()))
}

// HandleUsage receives final token counts asynchronously. APIKey contains our stable key ID,
// never the secret that the user copied during key creation.
func (s *Service) HandleUsage(ctx context.Context, record coreusage.Record) {
	if s == nil || s.repository == nil || !strings.HasPrefix(record.APIKey, principalPrefix) {
		return
	}
	keyID := strings.TrimPrefix(record.APIKey, principalPrefix)
	delta := UsageDelta{KeyID: keyID, Failed: record.Failed, InputTokens: record.Detail.InputTokens, OutputTokens: record.Detail.OutputTokens, TotalTokens: record.Detail.TotalTokens}
	if delta.TotalTokens == 0 {
		delta.TotalTokens = delta.InputTokens + delta.OutputTokens
	}
	usageContext := context.Background()
	if ctx != nil {
		usageContext = context.WithoutCancel(ctx)
	}
	if err := s.repository.RecordUsage(usageContext, delta); err != nil {
		log.WithError(err).WithField("key_id", keyID).Warn("failed to record managed user usage")
	}
}

func (s *Service) CreateOAuthInvite(ctx context.Context, input CreateOAuthInviteInput) (CreatedOAuthInvite, error) {
	providers, err := normalizeProviders(input.Providers)
	if err != nil {
		return CreatedOAuthInvite{}, err
	}
	token, err := randomToken(32)
	if err != nil {
		return CreatedOAuthInvite{}, err
	}
	digest := sha256.Sum256([]byte(token))
	maxUses := input.MaxUses
	if maxUses <= 0 {
		maxUses = 1
	}
	invite := OAuthInvite{ID: uuid.NewString(), Label: strings.TrimSpace(input.Label), Providers: providers, MaxUses: maxUses, Active: true, ExpiresAt: input.ExpiresAt, CreatedAt: s.now().UTC()}
	if invite.Label == "" {
		invite.Label = "OAuth invitation"
	}
	stored, err := s.repository.CreateOAuthInvite(ctx, invite, digest[:])
	if err != nil {
		return CreatedOAuthInvite{}, err
	}
	return CreatedOAuthInvite{OAuthInvite: stored, Token: token}, nil
}

func (s *Service) ListOAuthInvites(ctx context.Context) ([]OAuthInvite, error) {
	return s.repository.ListOAuthInvites(ctx)
}

func (s *Service) RevokeOAuthInvite(ctx context.Context, id string) error {
	return s.repository.RevokeOAuthInvite(ctx, id)
}

func (s *Service) GetOAuthInvite(ctx context.Context, token string) (OAuthInvite, error) {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	invite, err := s.repository.GetOAuthInviteByTokenHash(ctx, digest[:])
	if err != nil {
		return OAuthInvite{}, err
	}
	if !inviteAvailable(invite, s.now()) {
		return OAuthInvite{}, ErrInviteUnavailable
	}
	return invite, nil
}

func (s *Service) ReserveOAuthInvite(ctx context.Context, token, state, provider string) (OAuthInvite, error) {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return s.repository.ReserveOAuthInvite(ctx, digest[:], strings.TrimSpace(state), strings.ToLower(strings.TrimSpace(provider)))
}

func (s *Service) OAuthInviteOwnsSession(ctx context.Context, token, state string) (bool, error) {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return s.repository.OAuthInviteOwnsSession(ctx, digest[:], strings.TrimSpace(state))
}

func (s *Service) CompleteOAuthInvite(ctx context.Context, state, authID, email string) error {
	return s.repository.CompleteOAuthInvite(ctx, strings.TrimSpace(state), strings.TrimSpace(authID), strings.TrimSpace(email))
}

func parseManagedKey(raw string) (string, string, bool) {
	parts := strings.SplitN(strings.TrimSpace(raw), "_", 3)
	if len(parts) != 3 || parts[0] != "cpa" || strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" {
		return "", "", false
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func credentialFromRequest(request *http.Request) string {
	if request == nil {
		return ""
	}
	if authorization := strings.TrimSpace(request.Header.Get("Authorization")); authorization != "" {
		parts := strings.SplitN(authorization, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
		return authorization
	}
	for _, value := range []string{request.Header.Get("X-Goog-Api-Key"), request.Header.Get("X-Api-Key")} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	if request.URL != nil {
		if value := strings.TrimSpace(request.URL.Query().Get("key")); value != "" {
			return value
		}
		return strings.TrimSpace(request.URL.Query().Get("auth_token"))
	}
	return ""
}

func requestModel(request *http.Request) string {
	if request == nil || request.Body == nil {
		return ""
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return ""
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &payload)
	return strings.TrimSpace(payload.Model)
}

func modelAllowed(model string, allowed []string) bool {
	if len(allowed) == 0 || strings.TrimSpace(model) == "" {
		return true
	}
	for _, pattern := range allowed {
		pattern = strings.TrimSpace(pattern)
		if pattern == "*" || strings.EqualFold(pattern, model) {
			return true
		}
		if matched, _ := path.Match(strings.ToLower(pattern), strings.ToLower(model)); matched {
			return true
		}
	}
	return false
}

func skipManagedAuthentication(requestPath string) bool {
	return strings.HasPrefix(requestPath, "/v0/management") || strings.HasPrefix(requestPath, "/v0/oauth") || strings.HasPrefix(requestPath, "/oauth/invite") || requestPath == "/healthz"
}

func normalizeLimits(limits Limits) Limits {
	if limits.RequestsPerMinute < 0 {
		limits.RequestsPerMinute = 0
	}
	if limits.ConcurrentRequests < 0 {
		limits.ConcurrentRequests = 0
	}
	if limits.MonthlyTokens < 0 {
		limits.MonthlyTokens = 0
	}
	seen := make(map[string]struct{})
	models := make([]string, 0, len(limits.AllowedModels))
	for _, model := range limits.AllowedModels {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, model)
	}
	limits.AllowedModels = models
	return limits
}

func normalizeStatus(status, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case UserStatusActive:
		return UserStatusActive
	case UserStatusDisabled:
		return UserStatusDisabled
	default:
		return fallback
	}
}

func normalizeProviders(providers []string) ([]string, error) {
	allowed := map[string]struct{}{"anthropic": {}, "codex": {}, "antigravity": {}, "kimi": {}, "xai": {}}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if _, ok := allowed[provider]; !ok {
			return nil, fmt.Errorf("unsupported OAuth provider %q", provider)
		}
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		out = append(out, provider)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one OAuth provider is required")
	}
	return out, nil
}

func inviteAvailable(invite OAuthInvite, now time.Time) bool {
	return invite.Active && !expired(invite.ExpiresAt, now) && invite.UsedUses+invite.ReservedUses < invite.MaxUses
}

func expired(value *time.Time, now time.Time) bool { return value != nil && !value.After(now) }

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func monthStart(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}
