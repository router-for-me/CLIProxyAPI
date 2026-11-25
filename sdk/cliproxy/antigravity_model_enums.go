package cliproxy

import (
	"context"
	"crypto/sha256"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func antigravityProbeRefreshGrant(auth *coreauth.Auth) string {
	for _, key := range []string{"refresh_token", "refreshToken"} {
		if grant, _ := auth.Metadata[key].(string); strings.TrimSpace(grant) != "" {
			return strings.TrimSpace(grant)
		}
	}
	return ""
}

func antigravityModelEnumGrant(auth *coreauth.Auth) [sha256.Size]byte {
	grant := antigravityProbeRefreshGrant(auth)
	if grant == "" {
		grant, _ = auth.Metadata["access_token"].(string)
	}
	return sha256.Sum256([]byte(strings.TrimSpace(grant)))
}

func (s *Service) checkSeededAntigravityModelEnums(ctx context.Context, auth *coreauth.Auth) {
	key := s.antigravityCapabilityKey(auth)
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[key]
	antigravityCapabilityMu.RUnlock()
	if !entry.checkSeededEnums {
		return
	}
	missing, errCheck := coreauth.AntigravityModelEnumSnapshotMissing(ctx, auth)
	if errCheck != nil {
		return
	}
	antigravityCapabilityMu.Lock()
	defer antigravityCapabilityMu.Unlock()
	current := antigravityCapabilityCache[key]
	if current.hints.revision == entry.hints.revision {
		current.checkSeededEnums = false
		if missing {
			current.expiresAt = antigravityNowFunc()
		}
		antigravityCapabilityCache[key] = current
	}
}

func (s *Service) publishAntigravityModelEnums(ctx context.Context, auth *coreauth.Auth, key string, revision uint64) {
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[key]
	antigravityCapabilityMu.RUnlock()
	if entry.pendingEnums == nil || entry.hints.revision != revision {
		return
	}
	if s.coreManager != nil {
		current, exists := s.coreManager.GetByID(auth.ID)
		if !exists || isAntigravityAuthDisabled(current) || s.antigravityCapabilityKey(current) != key {
			return
		}
		auth = current
	}
	// Account refresh workers serialize publication per auth without holding
	// the shared capability lock during KV I/O. The regular scan retries misses.
	grantChanged := antigravityModelEnumGrant(auth) != entry.pendingEnumGrant
	if !grantChanged {
		store := coreauth.StoreAntigravityModelCatalog
		if s.coreManager != nil {
			store = s.coreManager.StoreAntigravityModelCatalog
		}
		if !store(ctx, auth, entry.pendingEnums, entry.pendingGroups, entry.pendingThinkingWebSearch) {
			return
		}
	}
	antigravityCapabilityMu.Lock()
	defer antigravityCapabilityMu.Unlock()
	current := antigravityCapabilityCache[key]
	if current.hints.revision == revision {
		current.pendingEnums = nil
		current.pendingGroups = nil
		current.pendingThinkingWebSearch = nil
		if grantChanged {
			current.expiresAt = antigravityNowFunc()
		}
		antigravityCapabilityCache[key] = current
	}
}
