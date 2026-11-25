//go:build windows

package codex

import (
	"context"
	"errors"
	"fmt"

	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
)

// CheckCodexWindowsReadiness checks whether Windows is ready for Codex/SIWC OAuth renewal.
var CheckCodexWindowsReadiness = func(ctx context.Context, label string) error {
	return baseauth.CheckWindowsRefreshReadiness(ctx, label)
}

func siwcRefreshReadiness(ctx context.Context) error {
	return codexRefreshReadiness(ctx, "SIWC")
}

func codexRefreshReadiness(ctx context.Context, label string) error {
	if errReadiness := CheckCodexWindowsReadiness(ctx, label); errReadiness != nil {
		if errors.Is(errReadiness, ErrSIWCRefreshDeferred) {
			return errReadiness
		}
		return fmt.Errorf("%w: %w", errReadiness, ErrSIWCRefreshDeferred)
	}
	return nil
}
