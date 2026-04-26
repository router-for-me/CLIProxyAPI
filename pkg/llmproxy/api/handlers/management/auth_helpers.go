package management

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Shared constants, types, and helpers for OAuth callback handling are declared in
// auth_files.go (consolidated in adef5c2f squash; kept canonical per kiro_websearch
// dedupe precedent). This file only retains symbols unique to auth_helpers.go.

// waitForOAuthCallback polls the auth directory for an OAuth callback file written by the
// callback route handler. The file name follows the convention:
//
//	.oauth-<provider>-<state>.oauth
//
// It polls every 500 ms until timeout elapses or the OAuth session is no longer pending.
// On success it returns the decoded key/value pairs from the JSON file and removes the file.
func (h *Handler) waitForOAuthCallback(state, provider string, timeout time.Duration) (map[string]string, error) {
	waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-%s-%s.oauth", provider, state))
	deadline := time.Now().Add(timeout)
	for {
		if !IsOAuthSessionPending(state, provider) {
			return nil, errOAuthSessionNotPending
		}
		if time.Now().After(deadline) {
			SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
			return nil, fmt.Errorf("timeout waiting for OAuth callback")
		}
		data, errRead := os.ReadFile(waitFile)
		if errRead == nil {
			var m map[string]string
			_ = json.Unmarshal(data, &m)
			_ = os.Remove(waitFile)
			return m, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// setupCallbackForwarder starts a callback forwarder when the request comes from the
// web UI. It returns a cleanup function that stops the forwarder; the caller should
// defer the returned func. If the request is not a web UI request the returned cleanup
// func is a no-op and forwarder is nil.
func (h *Handler) setupCallbackForwarder(c *gin.Context, port int, provider, callbackPath string) (cleanup func(), err error) {
	noop := func() {}
	if !isWebUIRequest(c) {
		return noop, nil
	}
	targetURL, errTarget := h.managementCallbackURL(callbackPath)
	if errTarget != nil {
		return noop, fmt.Errorf("failed to compute %s callback target: %w", provider, errTarget)
	}
	forwarder, errStart := startCallbackForwarder(port, provider, targetURL)
	if errStart != nil {
		return noop, fmt.Errorf("failed to start %s callback forwarder: %w", provider, errStart)
	}
	return func() { stopCallbackForwarderInstance(port, forwarder) }, nil
}

// saveAndCompleteAuth persists the token record, prints a success message, then marks the
// OAuth session complete by state and by provider. It is the final step shared by every
// OAuth provider handler.
func (h *Handler) saveAndCompleteAuth(ctx context.Context, state, provider string, record *coreauth.Auth, successMsg string) error {
	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		SetOAuthSessionError(state, "Failed to save authentication tokens")
		return fmt.Errorf("failed to save authentication tokens: %w", errSave)
	}
	fmt.Printf("%s Token saved to %s\n", successMsg, savedPath)
	CompleteOAuthSession(state)
	CompleteOAuthSessionsByProvider(provider)
	return nil
}
