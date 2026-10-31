package management

import (
	"context"
	"errors"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type codexDeviceRecovery struct {
	target  oauthRecoveryTarget
	existed bool
	version uint64
	epoch   uint64
}

func (h *Handler) prepareCodexDeviceRecovery(state string, record *coreauth.Auth) (*codexDeviceRecovery, error) {
	target, ok := GetOAuthSessionTarget(state)
	if !ok || target.AuthID == "" {
		return nil, nil
	}
	var accountID string
	if record.Metadata != nil {
		accountID, _ = record.Metadata["account_id"].(string)
	}
	if accountID == "" {
		if storage, okStorage := record.Storage.(*codex.CodexTokenStorage); okStorage && storage != nil {
			accountID = storage.AccountID
		}
	}
	if !matchesOAuthRecoveryTarget(target, authEmail(record), accountID, record.FileName) {
		return nil, errors.New("Authenticated account does not match target credential")
	}
	applyOAuthRecoveryTargetLocation(record, target)
	recovery := &codexDeviceRecovery{target: target}
	if h.authManager != nil {
		if existing, okExisting := h.authManager.GetByID(record.ID); okExisting && existing != nil {
			recovery.existed = true
			recovery.version = existing.CredentialVersion
			recovery.epoch = existing.RegistrationEpoch
		}
	}
	return recovery, nil
}

func (h *Handler) finishCodexDeviceRecovery(ctx context.Context, state string, record *coreauth.Auth, target oauthRecoveryTarget) {
	if h.authManager == nil {
		return
	}
	runtimeAuth, ok := h.authManager.GetByID(record.ID)
	if !ok || runtimeAuth == nil || authGrantFingerprint(runtimeAuth) != authGrantFingerprint(record) {
		return
	}
	oauthSessions.MarkAdopted(state, runtimeAuth.CredentialVersion, runtimeAuth.RegistrationEpoch)
	if !target.VerifyAfterLogin {
		return
	}
	if _, errVerify := h.authManager.VerifyInference(ctx, record.ID, target.VerifyModel); errVerify != nil {
		oauthSessions.mu.Lock()
		defer oauthSessions.mu.Unlock()
		if session, exists := oauthSessions.sessions[state]; exists && session.Completed {
			session.VerifyError = strings.TrimSpace(coreauth.SanitizeRenewalMessage(errVerify.Error()))
			oauthSessions.sessions[state] = session
		}
	}
}
