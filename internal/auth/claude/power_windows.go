//go:build windows

package claude

import (
	"context"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

// ReadClaudeWindowsPowerState reads the Windows power state used by Claude OAuth renewal.
var ReadClaudeWindowsPowerState = func(ctx context.Context) (baseauth.WindowsPowerState, error) {
	return baseauth.ReadWindowsPowerState(ctx)
}

func claudeRefreshReadiness(ctx context.Context) error {
	return baseauth.CheckWindowsRefreshReadiness(ctx, "Claude", ReadClaudeWindowsPowerState)
}
