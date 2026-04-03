package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type cachedQuota struct {
	Data      *AccountQuota
	FetchedAt time.Time
}

type Service struct {
	mu         sync.RWMutex
	tokens     map[string]*TokenInfo   // keyed by email/login
	cache      map[string]*cachedQuota // keyed by email/login
	cacheTTL   time.Duration
	userAgent  string
	httpClient *http.Client
	tokenDir   string
	auth       *CopilotAuth
}

func NewService(cfg *config.Config) *Service {
	ttl := time.Duration(cfg.CopilotQuota.CacheTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 300 * time.Second
	}
	userAgent := strings.TrimSpace(cfg.CopilotQuota.UserAgent)
	if userAgent == "" {
		userAgent = "GitHub-Copilot-Usage-Tray"
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return &Service{
		tokens:     make(map[string]*TokenInfo),
		cache:      make(map[string]*cachedQuota),
		cacheTTL:   ttl,
		userAgent:  userAgent,
		httpClient: httpClient,
		tokenDir:   cfg.AuthDir,
		auth:       NewCopilotAuth(httpClient),
	}
}

func (s *Service) UpdateConfig(cfg *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.CopilotQuota.CacheTTLSeconds > 0 {
		s.cacheTTL = time.Duration(cfg.CopilotQuota.CacheTTLSeconds) * time.Second
	}
	if ua := strings.TrimSpace(cfg.CopilotQuota.UserAgent); ua != "" {
		s.userAgent = ua
	}
}

func (s *Service) LoadTokens() error {
	if s.tokenDir == "" {
		return nil
	}
	pattern := filepath.Join(s.tokenDir, "copilot-quota-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("copilot quota: list token files: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, path := range matches {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var token TokenInfo
		if jsonErr := json.Unmarshal(data, &token); jsonErr != nil {
			continue
		}
		if token.AccessToken != "" && token.Email != "" {
			s.tokens[token.Email] = &token
		}
	}
	return nil
}

func (s *Service) SaveToken(token *TokenInfo) error {
	if token == nil || token.Email == "" || token.AccessToken == "" {
		return fmt.Errorf("copilot quota: invalid token")
	}
	if s.tokenDir == "" {
		return fmt.Errorf("copilot quota: tokenDir not configured")
	}
	if err := os.MkdirAll(s.tokenDir, 0o700); err != nil {
		return fmt.Errorf("copilot quota: create token dir: %w", err)
	}
	sanitized := sanitizeEmail(token.Email)
	fileName := fmt.Sprintf("copilot-quota-%s.json", sanitized)
	path := filepath.Join(s.tokenDir, fileName)
	data, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("copilot quota: marshal token: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("copilot quota: write token file: %w", err)
	}
	s.mu.Lock()
	s.tokens[token.Email] = token
	s.mu.Unlock()
	return nil
}

func (s *Service) RemoveToken(email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return fmt.Errorf("copilot quota: email is empty")
	}
	s.mu.Lock()
	delete(s.tokens, email)
	delete(s.cache, email)
	s.mu.Unlock()
	if s.tokenDir != "" {
		sanitized := sanitizeEmail(email)
		fileName := fmt.Sprintf("copilot-quota-%s.json", sanitized)
		path := filepath.Join(s.tokenDir, fileName)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("copilot quota: remove token file: %w", err)
		}
	}
	return nil
}

func sanitizeEmail(email string) string {
	r := strings.NewReplacer("@", "-at-", "/", "-", "\\", "-", ":", "-")
	return r.Replace(strings.ToLower(email))
}

func (s *Service) GetAllQuotas(ctx context.Context) (*ManagementResponse, error) {
	s.mu.RLock()
	emails := make([]string, 0, len(s.tokens))
	for email := range s.tokens {
		emails = append(emails, email)
	}
	cacheTTL := s.cacheTTL
	s.mu.RUnlock()

	accounts := make([]AccountQuota, 0, len(emails))
	for _, email := range emails {
		quota, err := s.getOrFetchQuota(ctx, email, false)
		if err != nil {
			accounts = append(accounts, AccountQuota{
				AccountID: email,
				Email:     email,
				Error:     err.Error(),
				CachedAt:  time.Now(),
			})
			continue
		}
		accounts = append(accounts, *quota)
	}

	return &ManagementResponse{
		Accounts:        accounts,
		CacheTTLSeconds: int(cacheTTL.Seconds()),
	}, nil
}

func (s *Service) GetQuotaForAccount(ctx context.Context, email string) (*AccountQuota, error) {
	return s.getOrFetchQuota(ctx, email, false)
}

func (s *Service) RefreshAll(ctx context.Context) (*ManagementResponse, error) {
	s.mu.RLock()
	emails := make([]string, 0, len(s.tokens))
	for email := range s.tokens {
		emails = append(emails, email)
	}
	cacheTTL := s.cacheTTL
	s.mu.RUnlock()

	accounts := make([]AccountQuota, 0, len(emails))
	for _, email := range emails {
		quota, err := s.getOrFetchQuota(ctx, email, true) // bypass cache
		if err != nil {
			accounts = append(accounts, AccountQuota{
				AccountID: email,
				Email:     email,
				Error:     err.Error(),
				CachedAt:  time.Now(),
			})
			continue
		}
		accounts = append(accounts, *quota)
	}

	return &ManagementResponse{
		Accounts:        accounts,
		CacheTTLSeconds: int(cacheTTL.Seconds()),
	}, nil
}

func (s *Service) getOrFetchQuota(ctx context.Context, email string, forceRefresh bool) (*AccountQuota, error) {
	s.mu.RLock()
	token, tokenOk := s.tokens[email]
	cached, cacheOk := s.cache[email]
	cacheTTL := s.cacheTTL
	userAgent := s.userAgent
	s.mu.RUnlock()

	if !tokenOk || token == nil {
		return nil, ErrNoToken
	}

	if !forceRefresh && cacheOk && cached != nil && time.Since(cached.FetchedAt) < cacheTTL {
		return cached.Data, nil
	}

	quotaResp, err := FetchQuota(ctx, s.httpClient, token.AccessToken, userAgent)
	if err != nil {
		return nil, err
	}

	enriched := EnrichQuotaResponse(quotaResp)
	resetDate := ""
	if quotaResp != nil {
		resetDate = quotaResp.QuotaResetDate
	}

	account := &AccountQuota{
		AccountID:      email,
		Email:          email,
		QuotaSnapshots: enriched,
		ResetDate:      resetDate,
		CachedAt:       time.Now(),
	}

	s.mu.Lock()
	s.cache[email] = &cachedQuota{Data: account, FetchedAt: time.Now()}
	s.mu.Unlock()

	return account, nil
}

func (s *Service) ListAccounts() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	accounts := make([]string, 0, len(s.tokens))
	for email := range s.tokens {
		accounts = append(accounts, email)
	}
	return accounts
}

func (s *Service) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	return s.auth.RequestDeviceCode(ctx)
}

func (s *Service) CompleteDeviceFlow(ctx context.Context, deviceCode string, interval time.Duration) (*TokenInfo, error) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	accessToken, err := s.auth.PollForToken(ctx, deviceCode, interval)
	if err != nil {
		return nil, fmt.Errorf("copilot quota: poll for token: %w", err)
	}
	email, err := s.auth.FetchUserEmail(ctx, accessToken)
	if err != nil {
		return nil, fmt.Errorf("copilot quota: fetch user email: %w", err)
	}
	token := &TokenInfo{
		AccessToken: accessToken,
		Email:       email,
		CreatedAt:   time.Now(),
	}
	if err := s.SaveToken(token); err != nil {
		return nil, fmt.Errorf("copilot quota: save token: %w", err)
	}
	return token, nil
}

// TryCompleteDeviceFlow makes a single attempt to exchange a device code for an access token.
// If the user has authorized, it fetches their email, saves the token, and returns the TokenInfo.
// If authorization is still pending, it returns an error with the GitHub error string
// (e.g., "authorization_pending").
func (s *Service) TryCompleteDeviceFlow(ctx context.Context, deviceCode string) (*TokenInfo, error) {
	accessToken, err := s.auth.TryExchangeToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}
	email, err := s.auth.FetchUserEmail(ctx, accessToken)
	if err != nil {
		return nil, fmt.Errorf("copilot quota: fetch user email: %w", err)
	}
	token := &TokenInfo{
		AccessToken: accessToken,
		Email:       email,
		CreatedAt:   time.Now(),
	}
	if err := s.SaveToken(token); err != nil {
		return nil, fmt.Errorf("copilot quota: save token: %w", err)
	}
	return token, nil
}
