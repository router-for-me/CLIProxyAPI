//go:build !darwin

package claude

import "context"

func claudeRefreshReadiness(context.Context) error { return nil }
