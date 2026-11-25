//go:build !darwin && !windows

package claude

import "context"

func claudeRefreshReadiness(context.Context) error { return nil }
