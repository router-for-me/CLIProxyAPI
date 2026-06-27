package executor

import (
	"context"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/executor/helps"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func newUsageReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *helps.UsageReporter {
	return helps.NewUsageReporter(ctx, provider, model, auth)
}
