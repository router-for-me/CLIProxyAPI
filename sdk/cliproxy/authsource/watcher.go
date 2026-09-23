package authsource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	// OwnedIDPrefix marks conductor auths this watcher owns. Any other id is
	// foreign: it is not removed when it is absent from the catalog, and it is
	// only eligible for the config-shadow pass.
	OwnedIDPrefix = "pc-cred-"

	// AttrTokenRotatedAt is the operator rotation marker. A difference copies
	// token metadata from the catalog snapshot onto the live auth. A conductor
	// refresh must not stamp this attribute.
	AttrTokenRotatedAt = "pc_token_rotated_at"

	// AttrSubsActive is the catalog subscription flag. It is operator-owned
	// and is not token material, so a missing value clears a stale "true".
	AttrSubsActive = "pc_subs_active"

	attrBaseURL           = "base_url"
	attrCompatName        = "compat_name"
	attrCatalogProvider   = "pc_provider"
	attrCredentialType    = "pc_credential_type"
	attrCredentialID      = "pc_credential_id"
	headerAttrPrefix      = "header:"
	secretAttrPrefix      = "secret_"
	mediaEndpointURLPref  = "pc_endpoint_url:"
	mediaEndpointBasePref = "pc_endpoint_base:"
	mediaEndpointAuthPref = "pc_endpoint_auth:"
)

// Source lists the desired auth catalog. Nil entries and empty ids are skipped.
type Source interface {
	List(ctx context.Context) ([]*coreauth.Auth, error)
}

// Target is the conductor surface the watcher writes.
// *coreauth.Manager satisfies it.
type Target interface {
	List() []*coreauth.Auth
	Register(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error)
	Update(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error)
	Remove(ctx context.Context, id string)
}

// Result is one surgical reconcile.
type Result struct {
	Added     int
	Updated   int
	Removed   int
	Unchanged int
	// ShadowRemoved counts config-synthesized auths dropped because an
	// identified twin can serve the same identity.
	ShadowRemoved int
	// ShadowUnresolved counts config-synthesized auths left in place because
	// no twin can serve them. A non-zero value is a defect signal.
	ShadowUnresolved int
}

// Watcher reconciles a catalog Source into a conductor Target.
type Watcher struct {
	source Source
	target Target
}

// NewWatcher returns the catalog auth-source watcher. Sync is safe to call on
// any cadence; a nil source or target makes Sync a no-op.
func NewWatcher(source Source, target Target) *Watcher {
	return &Watcher{source: source, target: target}
}

var _ Target = (*coreauth.Manager)(nil)

// Sync reconciles the conductor with the current catalog.
//
// New owned ids are registered as-is. Owned ids missing from the catalog are
// removed. An owned id that is already live is updated only when operator
// fields differ, and the update is applied onto the live auth so token
// metadata, runtime, and quota stay conductor-owned. One rejected Update does
// not stop the rest of the pass; those errors are joined and returned after
// shadow reconcile. A rejected Register stops the pass.
func (w *Watcher) Sync(ctx context.Context) (Result, error) {
	var out Result
	if w == nil || w.source == nil || w.target == nil {
		return out, nil
	}

	desired, err := w.source.List(ctx)
	if err != nil {
		return out, fmt.Errorf("authsource: list: %w", err)
	}
	want := make(map[string]*coreauth.Auth, len(desired))
	for _, auth := range desired {
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		want[auth.ID] = auth
	}

	have := make(map[string]*coreauth.Auth)
	var shadow []*coreauth.Auth
	for _, auth := range w.target.List() {
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		if !strings.HasPrefix(auth.ID, OwnedIDPrefix) {
			shadow = append(shadow, auth)
			continue
		}
		have[auth.ID] = auth
	}

	for id := range have {
		if _, ok := want[id]; !ok {
			w.target.Remove(ctx, id)
			out.Removed++
		}
	}

	var updateErrs []error
	for id, fresh := range want {
		existing, ok := have[id]
		if !ok {
			if _, err := w.target.Register(ctx, fresh); err != nil {
				return out, fmt.Errorf("authsource: register %s: %w", id, err)
			}
			out.Added++
			continue
		}
		if operatorFieldsEqual(existing, fresh) {
			out.Unchanged++
			continue
		}
		merged := applyOperatorFields(existing, fresh)
		if _, err := w.target.Update(ctx, merged); err != nil {
			updateErrs = append(updateErrs, fmt.Errorf("authsource: update %s: %w", id, err))
			continue
		}
		out.Updated++
	}
	reconcileShadowAuths(ctx, w.target, shadow, desired, &out)
	if len(updateErrs) > 0 {
		return out, errors.Join(updateErrs...)
	}
	return out, nil
}

// operatorAttrKeys are the attribute keys the catalog owns. Token metadata
// keys are absent on purpose.
var operatorAttrKeys = []string{
	coreauth.AttributeAuthKind,
	coreauth.AttributeAPIKey,
	"api_key",
	"auth_header_name",
	"auth_header_value",
	attrBaseURL,
	attrCatalogProvider,
	attrCredentialType,
	attrCredentialID,
	attrCompatName,
	"priority",
	AttrTokenRotatedAt,
	AttrSubsActive,
}

func isOperatorAttrKey(key string) bool {
	if strings.HasPrefix(key, headerAttrPrefix) ||
		strings.HasPrefix(key, secretAttrPrefix) ||
		strings.HasPrefix(key, mediaEndpointURLPref) ||
		strings.HasPrefix(key, mediaEndpointBasePref) ||
		strings.HasPrefix(key, mediaEndpointAuthPref) {
		return true
	}
	for _, candidate := range operatorAttrKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

func attrGet(m map[string]string, key string) string {
	if m == nil {
		return ""
	}
	return m[key]
}

func operatorFieldsEqual(a, b *coreauth.Auth) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Disabled != b.Disabled || a.Label != b.Label || a.Provider != b.Provider {
		return false
	}
	keys := map[string]struct{}{}
	for key := range a.Attributes {
		if isOperatorAttrKey(key) {
			keys[key] = struct{}{}
		}
	}
	for key := range b.Attributes {
		if isOperatorAttrKey(key) {
			keys[key] = struct{}{}
		}
	}
	for key := range keys {
		if attrGet(a.Attributes, key) != attrGet(b.Attributes, key) {
			return false
		}
	}
	return true
}

// applyOperatorFields copies catalog-owned fields onto a clone of the live
// auth. Metadata is copied only when the rotation marker changed, and then
// only the token and account-identity keys in operatorRotatableMetadataKeys.
func applyOperatorFields(existing, fresh *coreauth.Auth) *coreauth.Auth {
	if existing == nil {
		return fresh
	}
	if fresh == nil {
		return existing
	}
	out := existing.Clone()
	if out == nil {
		return existing
	}
	out.Disabled = fresh.Disabled
	out.Label = fresh.Label
	out.Provider = fresh.Provider
	if out.Attributes == nil {
		out.Attributes = map[string]string{}
	}
	for key := range out.Attributes {
		if isOperatorAttrKey(key) {
			delete(out.Attributes, key)
		}
	}
	for key, value := range fresh.Attributes {
		if isOperatorAttrKey(key) {
			out.Attributes[key] = value
		}
	}
	if attrGet(fresh.Attributes, AttrTokenRotatedAt) != attrGet(existing.Attributes, AttrTokenRotatedAt) {
		if out.Metadata == nil {
			out.Metadata = map[string]any{}
		}
		for _, key := range operatorRotatableMetadataKeys {
			if value, ok := fresh.Metadata[key]; ok {
				out.Metadata[key] = value
			} else {
				delete(out.Metadata, key)
			}
		}
		clearAuthFailureStateForRotation(out)
	}
	return out
}

// operatorRotatableMetadataKeys are the metadata keys an operator rotation may
// replace. Every expiry spelling Auth.ExpirationTime recognises is here:
// "expired" outranks "expires_at", so leaving a stale spelling in place would
// keep the pasted token looking expired.
var operatorRotatableMetadataKeys = []string{
	"access_token", "refresh_token",
	"expires_at", "expired", "expire", "expiresAt", "expiry", "expires", "expires_in",
	"project_id",
}

// clearAuthFailureStateForRotation returns an auth to serving after an
// operator pasted a replacement token. Quota-shaped model state is left
// alone: a new token does not restore an exhausted quota.
func clearAuthFailureStateForRotation(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	auth.LastError = nil
	auth.Unavailable = false
	auth.StatusMessage = ""
	auth.NextRetryAfter = time.Time{}
	auth.NextRefreshAfter = time.Time{}
	if auth.Status == coreauth.StatusError {
		auth.Status = coreauth.StatusActive
	}
	for _, state := range auth.ModelStates {
		if state == nil || state.Quota.Exceeded {
			continue
		}
		state.Unavailable = false
		state.NextRetryAfter = time.Time{}
		state.LastError = nil
		if state.Status == coreauth.StatusError {
			state.Status = coreauth.StatusActive
		}
	}
}
