package management

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// prepareProviderAuth rotates expiring tokens for providers whose management
// calls use the stored access token directly (Kiro, Qoder).
func (h *Handler) prepareProviderAuth(ctx context.Context, auth *coreauth.Auth, proxyURL string) (*coreauth.Auth, error) {
	if auth == nil {
		return auth, nil
	}
	var preparer coreauth.RequestAuthPreparer
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "kiro":
		preparer = executor.NewKiroExecutor(h.cfg)
	case "qoder", "qoder-cn":
		preparer = executor.NewQoderExecutor(h.cfg)
	default:
		return auth, nil
	}
	ctx = coreexecutor.WithRequestProxyURL(ctx, proxyURL)
	if h.authManager != nil {
		return h.authManager.PrepareRequestAuth(ctx, preparer, auth)
	}
	if preparer.ShouldPrepareRequestAuth(auth) {
		return preparer.PrepareRequestAuth(ctx, auth)
	}
	return auth, nil
}
