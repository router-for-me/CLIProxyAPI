// Package kiro provides refresh utilities for Kiro token management.
package kiro

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
)

// RefreshResult contains the result of a token refresh attempt.
type RefreshResult struct {
	TokenData    *KiroTokenData
	Error        error
	UsedFallback bool // True if we used the existing token as fallback
}

// RefreshWithGracefulDegradation attempts to refresh a token with graceful degradation.
// If refresh fails but the existing access token is still valid, it returns the existing token.
// This matches kiro-openai-gateway's behavior for better reliability.
//
// Parameters:
//   - ctx: Context for the request
//   - refreshFunc: Function to perform the actual refresh
//   - existingAccessToken: Current access token (for fallback)
//   - expiresAt: Expiration time of the existing token
//
// Returns:
//   - RefreshResult containing the new or existing token data
func RefreshWithGracefulDegradation(
	ctx context.Context,
	refreshFunc func(ctx context.Context) (*KiroTokenData, error),
	existingAccessToken string,
	expiresAt time.Time,
) RefreshResult {
	// Try to refresh the token
	newTokenData, err := refreshFunc(ctx)
	if err == nil {
		return RefreshResult{
			TokenData:    newTokenData,
			Error:        nil,
			UsedFallback: false,
		}
	}

	// Refresh failed - check if we can use the existing token
	log.Warnf("kiro: token refresh failed: %v", err)

	// Check if existing token is still valid (not expired)
	if existingAccessToken != "" && time.Now().Before(expiresAt) {
		remainingTime := time.Until(expiresAt)
		log.Warnf("kiro: using existing access token (expires in %v). Will retry refresh later.", remainingTime.Round(time.Second))

		return RefreshResult{
			TokenData: &KiroTokenData{
				AccessToken: existingAccessToken,
				ExpiresAt:   expiresAt.Format(time.RFC3339),
			},
			Error:        nil,
			UsedFallback: true,
		}
	}

	// Token is expired and refresh failed - return the error
	return RefreshResult{
		TokenData:    nil,
		Error:        fmt.Errorf("token refresh failed and existing token is expired: %w", err),
		UsedFallback: false,
	}
}

// IsTokenExpiringSoon checks if a token is expiring within the given threshold.
// Default threshold is 5 minutes if not specified.
func IsTokenExpiringSoon(expiresAt time.Time, threshold time.Duration) bool {
	if threshold == 0 {
		threshold = 5 * time.Minute
	}
	return time.Now().Add(threshold).After(expiresAt)
}

// IsTokenExpired checks if a token has already expired.
func IsTokenExpired(expiresAt time.Time) bool {
	return time.Now().After(expiresAt)
}

// ParseExpiresAt parses an expiration time string in RFC3339 format.
// Returns zero time if parsing fails.
func ParseExpiresAt(expiresAtStr string) time.Time {
	if expiresAtStr == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		log.Debugf("kiro: failed to parse expiresAt '%s': %v", expiresAtStr, err)
		return time.Time{}
	}
	return t
}

// RefreshConfig contains configuration for token refresh behavior.
type RefreshConfig struct {
	// MaxRetries is the maximum number of refresh attempts (default: 1)
	MaxRetries int
	// RetryDelay is the delay between retry attempts (default: 1 second)
	RetryDelay time.Duration
	// RefreshThreshold is how early to refresh before expiration (default: 5 minutes)
	RefreshThreshold time.Duration
	// EnableGracefulDegradation allows using existing token if refresh fails (default: true)
	EnableGracefulDegradation bool
}

// DefaultRefreshConfig returns the default refresh configuration.
func DefaultRefreshConfig() RefreshConfig {
	return RefreshConfig{
		MaxRetries:                1,
		RetryDelay:                time.Second,
		RefreshThreshold:          5 * time.Minute,
		EnableGracefulDegradation: true,
	}
}

// RefreshWithRetry attempts to refresh a token with retry logic.
func RefreshWithRetry(
	ctx context.Context,
	refreshFunc func(ctx context.Context) (*KiroTokenData, error),
	config RefreshConfig,
) (*KiroTokenData, error) {
	var lastErr error

	maxAttempts := config.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		tokenData, err := refreshFunc(ctx)
		if err == nil {
			if attempt > 1 {
				log.Infof("kiro: token refresh succeeded on attempt %d", attempt)
			}
			return tokenData, nil
		}

		lastErr = err
		log.Warnf("kiro: token refresh attempt %d/%d failed: %v", attempt, maxAttempts, err)

		// Don't sleep after the last attempt
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(config.RetryDelay):
			}
		}
	}

	return nil, fmt.Errorf("token refresh failed after %d attempts: %w", maxAttempts, lastErr)
}
