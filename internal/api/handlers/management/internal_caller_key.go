package management

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// InternalCallerKeyAlias is the stable key_alias of the dashboard's dedicated
// caller key. Keeping it constant makes the ensure step idempotent: the server
// looks the row up by alias rather than piling up a new key on every login.
const InternalCallerKeyAlias = "nixllm-internal-default"

// internalCallerKeyEmail is the stable identity of the internal user that owns
// the dashboard caller key. The .invalid TLD is reserved for documentation and
// testing — no real mailbox can collide with it.
const internalCallerKeyEmail = "nixllm-internal@nixllm.invalid"

// internalCallerKeyName is the display name shown on the API Keys page so an
// operator can recognize the row (and delete it to force recreation, if ever
// needed).
const internalCallerKeyName = "NixLLM Internal Caller"

// internalCallerKeyUserAlias is the internal_user alias rendered in the
// dashboard's user and key listings.
const internalCallerKeyUserAlias = "NixLLM Internal"

// internalCallerKeyMetadata marks the key row as NixLLM-internal so the API
// Keys list (which renders key metadata) makes the provenance visible.
var internalCallerKeyMetadata = map[string]any{
	"source":  "nixllm-internal",
	"purpose": "dashboard caller-key",
}

// EnsureInternalCallerKey handles POST /v0/management/internal/caller-key.
//
// It guarantees that NixLLM's own dashboard caller API key exists in the
// api_keys table: the owning internal user is created when missing, the key row
// is created when missing, and the secret is rotated (Regenerate) on every call
// so a fresh plaintext is always returned. The server stores only the SHA-256
// hash (existing Create/Regenerate contract), so the plaintext must be returned
// here exactly once — the dashboard persists it as the stored caller key.
//
// The key is created with no policy caps (nil budget/RPM → unlimited, full
// catalog visibility) and unrestricted models, which is what a Models Catalog
// sync needs. The returned shape matches the POST /api-keys-pg response
// ({key, key_prefix}) so client code stays consistent.
//
// This route sits behind the management-token middleware (group gateway in
// server_management.go), so the plaintext is only reachable by an authenticated
// operator, never by a bearer of a client API key. Without PGSTORE_DSN it
// returns 503 via requirePG, matching every other PG-backed management route.
func (h *Handler) EnsureInternalCallerKey(c *gin.Context) {
	apiKeys, _, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	users, ok := h.requireUsers(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	// The internal owner user is ensured first: every api_keys row must be
	// owned by an internal user (see POST /api-keys-pg). MaxBudget stays nil so
	// the per-user budget fallback never re-caps the unlimited key policy.
	userID, err := ensureInternalCallerUser(ctx, users)
	if err != nil {
		log.WithError(err).Error("management: ensure internal caller user failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type":    "internal_error",
			"message": "failed to ensure internal caller user: " + err.Error(),
		}})
		return
	}

	secret, keyPrefix, err := ensureInternalCallerKey(ctx, apiKeys, userID)
	if err != nil {
		log.WithError(err).Error("management: ensure internal caller key failed")
		if errors.Is(err, store.ErrAmbiguousAlias) {
			c.JSON(http.StatusConflict, gin.H{"error": gin.H{
				"type": "ambiguous_internal_caller_key",
				"message": "multiple api_keys rows share the reserved internal caller key alias " +
					InternalCallerKeyAlias + " — merge or delete them, then log in again",
			}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{
			"type":    "internal_error",
			"message": "failed to ensure internal caller key: " + err.Error(),
		}})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"key":        secret,
		"key_prefix": keyPrefix,
	})
}

// ensureInternalCallerUser returns the id of the reserved internal user,
// creating the row (email identity) when it does not yet exist. Operators who
// delete the user get it recreated on the next login — intended.
func ensureInternalCallerUser(ctx context.Context, users *store.UserStore) (string, error) {
	u, err := users.GetByEmail(ctx, internalCallerKeyEmail)
	switch {
	case err == nil:
		return u.ID, nil
	case errors.Is(err, store.ErrInternalUserNotFound):
		created, createErr := users.Create(ctx, store.InternalUser{
			UserAlias: internalCallerKeyUserAlias,
			UserEmail: internalCallerKeyEmail,
			UserRole:  store.InternalUserRole,
			// MaxBudget stays nil: the per-user budget fallback must never
			// re-cap the (deliberately unlimited) caller key policy.
			MaxBudget: nil,
		})
		if createErr != nil {
			return "", createErr
		}
		return created.ID, nil
	default:
		return "", err
	}
}

// ensureInternalCallerKey returns a fresh plaintext secret for the reserved
// caller key, creating the row when missing and rotating (Regenerate) when it
// already exists so the plaintext returned is always current. The policy is
// left nil: no budget/RPM/model caps means unlimited and full visibility.
func ensureInternalCallerKey(ctx context.Context, apiKeys *store.APIKeyStore, userID string) (secret, keyPrefix string, err error) {
	existing, _, lookupErr := apiKeys.LookupByAlias(ctx, InternalCallerKeyAlias)
	switch {
	case lookupErr == nil:
		// Rotate in place; Regenerate returns the same shape as Create.
		secret, err = apiKeys.Regenerate(ctx, existing.ID, "")
		if err != nil {
			return "", "", err
		}
		return secret, store.SecretPrefixOf(secret), nil
	case errors.Is(lookupErr, store.ErrAPIKeyNotFound):
		created, createdSecret, createErr := apiKeys.Create(ctx, internalCallerKeyName, InternalCallerKeyAlias,
			"" /* server-generates */, nil /* no expiry */, internalCallerKeyMetadata, nil /* no caps */)
		if createErr != nil {
			return "", "", createErr
		}
		// Stamp the owner assignment, mirroring CreatePGAPIKey. On failure,
		// roll the orphaned row back so we never leave an unowned internal key.
		if attachErr := apiKeys.UpdateUserID(ctx, created.ID, userID); attachErr != nil {
			if delErr := apiKeys.Delete(ctx, created.ID); delErr != nil {
				log.WithError(delErr).WithField("api_key_id", created.ID).
					Warn("management: failed to roll back orphaned internal caller key after user attach failure")
			}
			return "", "", attachErr
		}
		return createdSecret, store.SecretPrefixOf(createdSecret), nil
	case errors.Is(lookupErr, store.ErrAmbiguousAlias):
		return "", "", lookupErr
	default:
		return "", "", lookupErr
	}
}
