package management

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func (h *Handler) prepareKiroAuth(ctx context.Context, auth *coreauth.Auth, proxyURL string) (*coreauth.Auth, error) {
	if auth == nil || !strings.EqualFold(auth.Provider, "kiro") {
		return auth, nil
	}
	ctx = coreexecutor.WithRequestProxyURL(ctx, proxyURL)
	preparer := executor.NewKiroExecutor(h.cfg)
	if h.authManager != nil {
		return h.authManager.PrepareRequestAuth(ctx, preparer, auth)
	}
	if preparer.ShouldPrepareRequestAuth(auth) {
		return preparer.PrepareRequestAuth(ctx, auth)
	}
	return auth, nil
}
