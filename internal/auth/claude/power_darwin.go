//go:build darwin

package claude

import (
	"context"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

// ReadClaudeSystemState reads the macOS power capabilities used by Claude OAuth renewal.
var ReadClaudeSystemState = func(ctx context.Context) ([]byte, error) {
	return baseauth.ReadMacOSSystemState(ctx)
}

func claudeRefreshReadiness(ctx context.Context) error {
	return baseauth.CheckMacOSRefreshReadiness(ctx, "Claude", ReadClaudeSystemState)
}
