// Package watcher provides type aliases to the internal implementation.
// This allows both "internal/watcher" and "pkg/llmproxy/watcher" import paths to work seamlessly.
package watcher

import internalwatcher "github.com/kooshapari/cliproxyapi-plusplus/v6/internal/watcher"

// Type aliases for exported types
type Watcher = internalwatcher.Watcher
type AuthUpdateAction = internalwatcher.AuthUpdateAction
type AuthUpdate = internalwatcher.AuthUpdate

// Re-export constants
const (
	AuthUpdateActionAdd    AuthUpdateAction = internalwatcher.AuthUpdateActionAdd
	AuthUpdateActionModify AuthUpdateAction = internalwatcher.AuthUpdateActionModify
	AuthUpdateActionDelete AuthUpdateAction = internalwatcher.AuthUpdateActionDelete
)

// Function aliases for exported constructors
var NewWatcher = internalwatcher.NewWatcher
