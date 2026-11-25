//go:build !darwin && !windows

package codex

import "context"

func siwcRefreshReadiness(context.Context) error { return nil }

func codexRefreshReadiness(context.Context, string) error { return nil }
