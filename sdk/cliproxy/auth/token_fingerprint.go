package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// AccessTokenSHA256 returns the normalized OAuth access-token fingerprint used
// to fence asynchronous Home execution results without exposing the token.
func AccessTokenSHA256(auth *Auth) string {
	accessToken := accessTokenForFingerprint(auth)
	if accessToken == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(accessToken))
	return hex.EncodeToString(digest[:])
}

type accessTokenFingerprintObserverContextKey struct{}

func withAccessTokenFingerprintObserver(ctx context.Context, observer func(*Auth)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, accessTokenFingerprintObserverContextKey{}, observer)
}

// NotifyAccessTokenFingerprint reports the auth snapshot actually used by an
// executor that may refresh its local token before sending upstream. The
// observer derives the fingerprint and can reuse that snapshot for recovery.
func NotifyAccessTokenFingerprint(ctx context.Context, auth *Auth) {
	if ctx == nil || auth == nil || AccessTokenSHA256(auth) == "" {
		return
	}
	observer, _ := ctx.Value(accessTokenFingerprintObserverContextKey{}).(func(*Auth))
	if observer != nil {
		observer(auth.Clone())
	}
}

func accessTokenForFingerprint(auth *Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	for _, key := range []string{"access_token", "accessToken"} {
		if value, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	for _, key := range []string{"token", "Token"} {
		switch token := auth.Metadata[key].(type) {
		case map[string]any:
			for _, tokenKey := range []string{"access_token", "accessToken"} {
				if value, ok := token[tokenKey].(string); ok && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
		case map[string]string:
			for _, tokenKey := range []string{"access_token", "accessToken"} {
				if value := strings.TrimSpace(token[tokenKey]); value != "" {
					return value
				}
			}
		}
	}
	return ""
}
