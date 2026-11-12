//go:build !darwin

package codex

import "context"

func siwcRefreshReadiness(context.Context) error { return nil }

func codexRefreshReadiness(context.Context, string) error { return nil }
