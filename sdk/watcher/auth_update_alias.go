// Package watcher provides the public SDK watcher types.
//
// It re-exports AuthUpdate as a type alias so SDK code can use
// pkg/llmproxy/watcher.AuthUpdate types without import conflicts.
package watcher

import llmproxywatcher "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/watcher"

// AuthUpdate is a type alias for pkg/llmproxy/watcher.AuthUpdate
type AuthUpdate = llmproxywatcher.AuthUpdate

// AuthUpdateAction is a type alias for pkg/llmproxy/watcher.AuthUpdateAction
type AuthUpdateAction = llmproxywatcher.AuthUpdateAction
