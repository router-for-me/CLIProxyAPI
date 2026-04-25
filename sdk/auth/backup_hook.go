package auth

import (
	"context"
	"sync/atomic"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// BackupOnUnauthorizedHook wraps an existing Hook and triggers 401-bak
// file movement when execution results indicate a 401 unauthorized error.
type BackupOnUnauthorizedHook struct {
	inner   cliproxyauth.Hook
	store   *FileTokenStore
	enabled atomic.Bool
}

// NewBackupOnUnauthorizedHook creates a hook that delegates to inner and
// additionally moves auth files to 401-bak on 401 errors.
func NewBackupOnUnauthorizedHook(inner cliproxyauth.Hook, store *FileTokenStore, enabled bool) *BackupOnUnauthorizedHook {
	if inner == nil {
		inner = cliproxyauth.NoopHook{}
	}
	h := &BackupOnUnauthorizedHook{inner: inner, store: store}
	h.enabled.Store(enabled)
	return h
}

// SetEnabled dynamically toggles the 401-bak backup feature.
func (h *BackupOnUnauthorizedHook) SetEnabled(v bool) {
	h.enabled.Store(v)
}

func (h *BackupOnUnauthorizedHook) OnAuthRegistered(ctx context.Context, auth *cliproxyauth.Auth) {
	h.inner.OnAuthRegistered(ctx, auth)
}

func (h *BackupOnUnauthorizedHook) OnAuthUpdated(ctx context.Context, auth *cliproxyauth.Auth) {
	h.inner.OnAuthUpdated(ctx, auth)
}

func (h *BackupOnUnauthorizedHook) OnResult(ctx context.Context, result cliproxyauth.Result) {
	h.inner.OnResult(ctx, result)

	if !h.enabled.Load() {
		return
	}
	if result.Error == nil || result.Success {
		return
	}
	if result.Error.HTTPStatus != 401 {
		return
	}
	if h.store == nil || result.AuthID == "" {
		return
	}

	auth := &cliproxyauth.Auth{
		ID:       result.AuthID,
		FileName: result.AuthID,
	}
	if err := h.store.MoveToBackup(ctx, auth); err != nil {
		log.Warnf("401-bak hook: move failed for %s: %v", maskSensitiveID(result.AuthID), err)
	}
}
