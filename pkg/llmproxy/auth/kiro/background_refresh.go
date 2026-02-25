package kiro

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"golang.org/x/sync/semaphore"
)

type Token struct {
	ID           string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	LastVerified time.Time
	ClientID     string
	ClientSecret string
	AuthMethod   string
	Provider     string
	StartURL     string
	Region       string
}

type TokenRepository interface {
	FindOldestUnverified(limit int) []*Token
	UpdateToken(token *Token) error
}

type RefresherOption func(*BackgroundRefresher)

func WithInterval(interval time.Duration) RefresherOption {
	return func(r *BackgroundRefresher) {
		r.interval = interval
	}
}

func WithBatchSize(size int) RefresherOption {
	return func(r *BackgroundRefresher) {
		r.batchSize = size
	}
}

func WithConcurrency(concurrency int) RefresherOption {
	return func(r *BackgroundRefresher) {
		r.concurrency = concurrency
	}
}

type BackgroundRefresher struct {
	interval         time.Duration
	batchSize        int
	concurrency      int
	tokenRepo        TokenRepository
	stopCh           chan struct{}
	wg               sync.WaitGroup
	oauth            *KiroOAuth
	ssoClient        *SSOOIDCClient
	callbackMu       sync.RWMutex                                   // 保护回调函数的并发访问
	onTokenRefreshed func(tokenID string, tokenData *KiroTokenData) // 刷新成功回调
}

func NewBackgroundRefresher(repo TokenRepository, opts ...RefresherOption) *BackgroundRefresher {
	r := &BackgroundRefresher{
		interval:    time.Minute,
		batchSize:   50,
		concurrency: 10,
		tokenRepo:   repo,
		stopCh:      make(chan struct{}),
		oauth:       nil, // Lazy init - will be set when config available
		ssoClient:   nil, // Lazy init - will be set when config available
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// WithConfig sets the configuration for OAuth and SSO clients.
func WithConfig(cfg *config.Config) RefresherOption {
	return func(r *BackgroundRefresher) {
		r.oauth = NewKiroOAuth(cfg)
		r.ssoClient = NewSSOOIDCClient(cfg)
	}
}

// WithOnTokenRefreshed sets the callback function to be called when a token is successfully refreshed.
// The callback receives the token ID (filename) and the new token data.
// This allows external components (e.g., Watcher) to be notified of token updates.
func WithOnTokenRefreshed(callback func(tokenID string, tokenData *KiroTokenData)) RefresherOption {
	return func(r *BackgroundRefresher) {
		r.callbackMu.Lock()
		r.onTokenRefreshed = callback
		r.callbackMu.Unlock()
	}
}

func (r *BackgroundRefresher) Start(ctx context.Context) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		r.refreshBatch(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-ticker.C:
				r.refreshBatch(ctx)
			}
		}
	}()
}

func (r *BackgroundRefresher) Stop() {
	close(r.stopCh)
	r.wg.Wait()
}

func (r *BackgroundRefresher) refreshBatch(ctx context.Context) {
	tokens := r.tokenRepo.FindOldestUnverified(r.batchSize)
	if len(tokens) == 0 {
		return
	}

	sem := semaphore.NewWeighted(int64(r.concurrency))
	var wg sync.WaitGroup

	for i, token := range tokens {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-r.stopCh:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}

		if err := sem.Acquire(ctx, 1); err != nil {
			return
		}

		wg.Add(1)
		go func(t *Token) {
			defer wg.Done()
			defer sem.Release(1)
			r.refreshSingle(ctx, t)
		}(token)
	}

	wg.Wait()
}

func (r *BackgroundRefresher) refreshSingle(ctx context.Context, token *Token) {
	// Normalize auth method to lowercase for case-insensitive matching
	authMethod := strings.ToLower(token.AuthMethod)

	// Create refresh function based on auth method
	refreshFunc := func(ctx context.Context) (*KiroTokenData, error) {
		switch authMethod {
		case "idc":
			return r.ssoClient.RefreshTokenWithRegion(
				ctx,
				token.ClientID,
				token.ClientSecret,
				token.RefreshToken,
				token.Region,
				token.StartURL,
			)
		case "builder-id":
			return r.ssoClient.RefreshToken(
				ctx,
				token.ClientID,
				token.ClientSecret,
				token.RefreshToken,
			)
		default:
			return r.oauth.RefreshTokenWithFingerprint(ctx, token.RefreshToken, token.ID)
		}
	}

	// Use graceful degradation for better reliability
	result := RefreshWithGracefulDegradation(
		ctx,
		refreshFunc,
		token.AccessToken,
		token.ExpiresAt,
	)

	if result.Error != nil {
		log.Printf("failed to refresh token %s: %v", token.ID, result.Error)
		return
	}

	newTokenData := result.TokenData
	if result.UsedFallback {
		log.Printf("token %s: using existing token as fallback (refresh failed but token still valid)", token.ID)
		// Don't update the token file if we're using fallback
		// Just update LastVerified to prevent immediate re-check
		token.LastVerified = time.Now()
		return
	}

	token.AccessToken = newTokenData.AccessToken
	if newTokenData.RefreshToken != "" {
		token.RefreshToken = newTokenData.RefreshToken
	}
	token.LastVerified = time.Now()

	if newTokenData.ExpiresAt != "" {
		if expTime, parseErr := time.Parse(time.RFC3339, newTokenData.ExpiresAt); parseErr == nil {
			token.ExpiresAt = expTime
		}
	}

	if err := r.tokenRepo.UpdateToken(token); err != nil {
		log.Printf("failed to update token %s: %v", token.ID, err)
		return
	}

	// 方案 A: 刷新成功后触发回调，通知 Watcher 更新内存中的 Auth 对象
	r.callbackMu.RLock()
	callback := r.onTokenRefreshed
	r.callbackMu.RUnlock()

	if callback != nil {
		// 使用 defer recover 隔离回调 panic，防止崩溃整个进程
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("background refresh: callback panic for token %s: %v", token.ID, rec)
				}
			}()
			log.Printf("background refresh: notifying token refresh callback for %s", token.ID)
			callback(token.ID, newTokenData)
		}()
	}
}
