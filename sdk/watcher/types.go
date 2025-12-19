// Package watcher provides file watching types for auth updates in the CLI Proxy SDK.
// This package defines types used for tracking authentication configuration changes
// without depending on internal packages.
package watcher

import (
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// AuthUpdateAction represents the type of change detected in auth sources.
type AuthUpdateAction string

const (
	// AuthUpdateActionAdd indicates a new auth entry was added.
	AuthUpdateActionAdd AuthUpdateAction = "add"
	// AuthUpdateActionModify indicates an existing auth entry was modified.
	AuthUpdateActionModify AuthUpdateAction = "modify"
	// AuthUpdateActionDelete indicates an auth entry was deleted.
	AuthUpdateActionDelete AuthUpdateAction = "delete"
)

// AuthUpdate describes an incremental change to auth configuration.
// This is used to notify the system of authentication changes detected
// by file watching or other mechanisms.
type AuthUpdate struct {
	// Action indicates the type of change (add, modify, delete).
	Action AuthUpdateAction
	// ID is the unique identifier of the auth entry being changed.
	ID string
	// Auth contains the auth entry details. May be nil for delete actions.
	Auth *auth.Auth
}

// IsAddOrModify returns true if the action is add or modify.
func (u AuthUpdate) IsAddOrModify() bool {
	return u.Action == AuthUpdateActionAdd || u.Action == AuthUpdateActionModify
}

// IsDelete returns true if the action is delete.
func (u AuthUpdate) IsDelete() bool {
	return u.Action == AuthUpdateActionDelete
}
