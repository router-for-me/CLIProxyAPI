package management

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// serializes list+save+delete so two concurrent OAuth completions for the same
// account_id cannot each keep the other's file and then delete both.
var codexOAuthReplaceMu sync.Mutex

var oauthRuntimeMetadataKeys = []string{
	"disabled",
	"status",
	"status_message",
	"unavailable",
	"last_error",
	"quota",
	"model_states",
	"next_retry_after",
	"failed",
	"success",
}

type codexAuthFileRef struct {
	Name      string
	AccountID string
	Disabled  bool
	Metadata  map[string]any
}

// saveCodexOAuthRecord persists a Codex OAuth credential. If this ChatGPT
// account_id already has a file, tokens are written back to that filename so
// usage stats and operator notes stay on the same row. Plan changes do not
// rename the file. A replace hint selects which sibling to keep when several exist.
func (h *Handler) saveCodexOAuthRecord(ctx context.Context, record *coreauth.Auth, replaceHint string) (string, error) {
	if record == nil {
		return "", fmt.Errorf("token record is nil")
	}

	canonicalName := strings.TrimSpace(record.FileName)
	if canonicalName == "" {
		canonicalName = strings.TrimSpace(record.ID)
	}
	accountID := codexAccountIDFromRecord(record)
	replaceHint = sanitizeCodexAuthRelPath(replaceHint)

	codexOAuthReplaceMu.Lock()
	defer codexOAuthReplaceMu.Unlock()
	if errGuard := guardOAuthSaveSession(ctx); errGuard != nil {
		return "", errGuard
	}

	siblings, errList := h.listCodexFilesByAccountID(ctx, accountID)
	if errList != nil {
		return "", errList
	}
	keep, remove := pickCodexKeepAndRemove(siblings, canonicalName, replaceHint)
	writeName := canonicalName
	if keep != "" {
		writeName = keep
		mergeCodexOperatorMetadata(record, metadataForCodexRef(siblings, keep))
	}

	prepareCodexOAuthRecordForSave(record, writeName)
	h.preserveCodexRuntimeSettings(record, writeName)
	if errGuard := guardOAuthSaveSession(ctx); errGuard != nil {
		return "", errGuard
	}

	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		return savedPath, errSave
	}
	// Nested files are ignored by the auth-dir watcher; refresh runtime before sibling cleanup.
	h.refreshRuntimeAuthAfterReplace(ctx, record, writeName)
	h.closeCodexSessionsForRel(writeName)

	var deleteErr error
	for _, name := range remove {
		if name == "" || name == writeName {
			continue
		}
		if errDelete := h.deleteCodexAuthRel(ctx, name); errDelete != nil {
			log.WithError(errDelete).WithField("file", name).Error("failed to remove replaced Codex auth file")
			deleteErr = fmt.Errorf("saved %s but failed to remove %s: %w", writeName, name, errDelete)
		}
	}
	if deleteErr != nil {
		return savedPath, deleteErr
	}
	return savedPath, nil
}

func (h *Handler) refreshRuntimeAuthAfterReplace(ctx context.Context, record *coreauth.Auth, writeName string) {
	if h == nil || h.authManager == nil || record == nil {
		return
	}
	writeName = sanitizeCodexAuthRelPath(writeName)
	if writeName == "" {
		return
	}
	path := h.resolveCodexAuthPath(writeName)
	now := time.Now()
	updated := false
	for _, id := range h.authIDsForCodexPath(path, writeName) {
		existing, ok := h.authManager.GetByID(id)
		if !ok || existing == nil {
			continue
		}
		applyCodexReplacementToRuntimeAuth(existing, record)
		existing.UpdatedAt = now
		if _, errUpdate := h.authManager.Update(ctx, existing); errUpdate != nil {
			log.WithError(errUpdate).WithField("id", id).Warn("failed to update runtime auth after Codex OAuth replace")
			continue
		}
		updated = true
	}
	if updated {
		return
	}
	record.ID = writeName
	record.FileName = writeName
	if record.Attributes == nil {
		record.Attributes = make(map[string]string)
	}
	record.Attributes["path"] = path
	record.Disabled = false
	record.Status = coreauth.StatusActive
	if _, errRegister := h.authManager.Register(ctx, record); errRegister != nil {
		log.WithError(errRegister).WithField("id", writeName).Warn("failed to register runtime auth after Codex OAuth replace")
	}
}

func applyCodexReplacementToRuntimeAuth(existing, record *coreauth.Auth) {
	if existing == nil || record == nil {
		return
	}
	if existing.Metadata == nil {
		existing.Metadata = make(map[string]any)
	}
	if strings.TrimSpace(existing.Prefix) == "" {
		existing.Prefix = record.Prefix
	}
	if strings.TrimSpace(existing.ProxyURL) == "" {
		existing.ProxyURL = record.ProxyURL
	}
	if strings.TrimSpace(existing.Label) == "" {
		existing.Label = record.Label
	}
	existing.Attributes = mergeAuthAttributes(existing.Attributes, record.Attributes)
	for key, value := range record.Metadata {
		existing.Metadata[key] = value
	}
	applyCodexTokenStorageToMetadata(existing.Metadata, record.Storage)
	applyCodexPlanTypeFromIDToken(existing)
	existing.Disabled = false
	existing.Status = coreauth.StatusActive
	existing.StatusMessage = ""
	existing.Unavailable = false
}

func (h *Handler) preserveCodexRuntimeSettings(record *coreauth.Auth, writeName string) {
	if record == nil {
		return
	}
	path := ""
	if h != nil {
		path = h.resolveCodexAuthPath(writeName)
	}
	var existing *coreauth.Auth
	if h != nil && h.authManager != nil {
		for _, id := range h.authIDsForCodexPath(path, writeName) {
			got, ok := h.authManager.GetByID(id)
			if ok && got != nil {
				existing = got
				break
			}
		}
	}
	if existing != nil {
		if strings.TrimSpace(record.Prefix) == "" {
			record.Prefix = existing.Prefix
		}
		if strings.TrimSpace(record.ProxyURL) == "" {
			record.ProxyURL = existing.ProxyURL
		}
		if strings.TrimSpace(record.Label) == "" {
			record.Label = existing.Label
		}
		record.Attributes = mergeAuthAttributes(existing.Attributes, record.Attributes)
	}
	applyCodexRuntimeFieldsFromMetadata(record, path)
}

func mergeAuthAttributes(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return overlay
	}
	out := make(map[string]string, len(base)+len(overlay))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overlay {
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	return out
}

func applyCodexRuntimeFieldsFromMetadata(record *coreauth.Auth, path string) {
	if record == nil {
		return
	}
	if record.Attributes == nil {
		record.Attributes = make(map[string]string)
	}
	if path != "" {
		if strings.TrimSpace(record.Attributes[coreauth.AttributePath]) == "" {
			record.Attributes[coreauth.AttributePath] = path
		}
		if strings.TrimSpace(record.Attributes[coreauth.AttributeSource]) == "" {
			record.Attributes[coreauth.AttributeSource] = path
		}
		if strings.TrimSpace(record.Attributes[coreauth.AttributeSourceBackend]) == "" {
			record.Attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceFile
		}
	}
	if record.Metadata == nil {
		return
	}
	if strings.TrimSpace(record.ProxyURL) == "" {
		if proxyURL, ok := record.Metadata["proxy_url"].(string); ok {
			record.ProxyURL = strings.TrimSpace(proxyURL)
		}
	}
	if strings.TrimSpace(record.Prefix) == "" {
		if prefix, ok := record.Metadata["prefix"].(string); ok {
			trimmed := strings.Trim(strings.TrimSpace(prefix), "/")
			if trimmed != "" && !strings.Contains(trimmed, "/") {
				record.Prefix = trimmed
			}
		}
	}
	if strings.TrimSpace(record.Label) == "" {
		if email, ok := record.Metadata["email"].(string); ok {
			if trimmed := strings.TrimSpace(email); trimmed != "" {
				record.Label = trimmed
			}
		}
	}
	if strings.TrimSpace(record.Attributes["priority"]) == "" {
		switch value := record.Metadata["priority"].(type) {
		case float64:
			record.Attributes["priority"] = strconv.Itoa(int(value))
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				record.Attributes["priority"] = trimmed
			}
		}
	}
	if strings.TrimSpace(record.Attributes["note"]) == "" {
		if note, ok := record.Metadata["note"].(string); ok {
			if trimmed := strings.TrimSpace(note); trimmed != "" {
				record.Attributes["note"] = trimmed
			}
		}
	}
	coreauth.ApplyCustomHeadersFromMetadata(record)
	_ = coreauth.ApplyAuthWeightMetadata(record, record.Metadata)
}

func applyCodexTokenStorageToMetadata(metadata map[string]any, storage any) {
	if metadata == nil {
		return
	}
	codexStorage, ok := storage.(*codex.CodexTokenStorage)
	if !ok || codexStorage == nil {
		return
	}
	metadata["access_token"] = codexStorage.AccessToken
	metadata["refresh_token"] = codexStorage.RefreshToken
	metadata["id_token"] = codexStorage.IDToken
	metadata["expired"] = codexStorage.Expire
	metadata["last_refresh"] = codexStorage.LastRefresh
	metadata["email"] = codexStorage.Email
	metadata["account_id"] = codexStorage.AccountID
	metadata["type"] = "codex"
}

func applyCodexPlanTypeFromIDToken(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	idToken := ""
	if auth.Metadata != nil {
		idToken, _ = auth.Metadata["id_token"].(string)
	}
	if strings.TrimSpace(idToken) == "" {
		if storage, ok := auth.Storage.(*codex.CodexTokenStorage); ok && storage != nil {
			idToken = storage.IDToken
		}
	}
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return
	}
	claims, errParse := codex.ParseJWTToken(idToken)
	if errParse != nil || claims == nil {
		return
	}
	planType := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
	if planType == "" {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["plan_type"] = planType
}

func prepareCodexOAuthRecordForSave(record *coreauth.Auth, writeName string) {
	if record == nil {
		return
	}
	record.ID = writeName
	record.FileName = writeName
	record.Disabled = false
	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	record.Metadata["disabled"] = false
	record.Metadata["status"] = ""
	record.Metadata["status_message"] = ""
	record.Metadata["unavailable"] = false
	applyCodexTokenStorageToMetadata(record.Metadata, record.Storage)
	applyCodexPlanTypeFromIDToken(record)
	if setter, ok := record.Storage.(interface{ SetMetadata(map[string]any) }); ok {
		setter.SetMetadata(record.Metadata)
	}
}

func codexAccountIDFromRecord(record *coreauth.Auth) string {
	if record == nil {
		return ""
	}
	if storage, ok := record.Storage.(*codex.CodexTokenStorage); ok {
		if id := strings.TrimSpace(storage.AccountID); id != "" {
			return id
		}
	}
	if record.Metadata == nil {
		return ""
	}
	id, _ := record.Metadata["account_id"].(string)
	return strings.TrimSpace(id)
}

func (h *Handler) listCodexFilesByAccountID(ctx context.Context, accountID string) ([]codexAuthFileRef, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || h == nil {
		return nil, nil
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return nil, fmt.Errorf("token store unavailable")
	}
	auths, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list existing Codex auth records: %w", err)
	}
	var out []codexAuthFileRef
	for _, auth := range auths {
		if auth == nil || !isCodexAuthRecord(auth) {
			continue
		}
		id := authPayloadAccountID(auth.Metadata)
		if id == "" || id != accountID {
			continue
		}
		name := authRecordFileName(auth, h.cfg)
		if name == "" || isUnsafeCodexAuthRelPath(name) {
			continue
		}
		out = append(out, codexAuthFileRef{
			Name:      name,
			AccountID: id,
			Disabled:  auth.Disabled || truthyJSON(auth.Metadata["disabled"]),
			Metadata:  cloneMetadataMap(auth.Metadata),
		})
	}
	return out, nil
}

func isCodexAuthRecord(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return true
	}
	return isCodexAuthPayload(auth.Metadata)
}

func isCodexAuthPayload(payload map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(jsonString(payload["type"])), "codex")
}

func authRecordFileName(auth *coreauth.Auth, cfg *config.Config) string {
	if auth == nil {
		return ""
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = strings.TrimSpace(auth.ID)
	}
	name = filepath.ToSlash(name)
	if name == "" || name == "." {
		return ""
	}
	authDir := ""
	if cfg != nil {
		authDir = strings.TrimSpace(cfg.AuthDir)
	}
	if authDir != "" && filepath.IsAbs(filepath.FromSlash(name)) {
		if rel, errRel := filepath.Rel(authDir, filepath.FromSlash(name)); errRel == nil {
			name = filepath.ToSlash(rel)
		}
	}
	return sanitizeCodexAuthRelPath(name)
}

func sanitizeCodexAuthRelPath(name string) string {
	name = filepath.ToSlash(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	if isUnsafeCodexAuthRelPath(name) {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(name))
}

func isUnsafeCodexAuthRelPath(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	from := filepath.FromSlash(name)
	if filepath.IsAbs(from) || filepath.VolumeName(from) != "" {
		return true
	}
	clean := filepath.ToSlash(filepath.Clean(from))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return true
	}
	return false
}

func (h *Handler) resolveCodexAuthPath(relName string) string {
	path := filepath.FromSlash(relName)
	if h != nil && h.cfg != nil && strings.TrimSpace(h.cfg.AuthDir) != "" && !filepath.IsAbs(path) {
		path = filepath.Join(h.cfg.AuthDir, path)
	}
	return path
}

func (h *Handler) closeCodexSessionsForRel(relName string) {
	relName = sanitizeCodexAuthRelPath(relName)
	if relName == "" {
		return
	}
	path := h.resolveCodexAuthPath(relName)
	for _, id := range h.authIDsForCodexPath(path, relName) {
		executor.CloseCodexWebsocketSessionsForAuthID(id, "oauth_replaced")
	}
}

func (h *Handler) deleteCodexAuthRel(ctx context.Context, relName string) error {
	relName = sanitizeCodexAuthRelPath(relName)
	if relName == "" {
		return fmt.Errorf("invalid auth path")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return fmt.Errorf("token store unavailable")
	}
	path := h.resolveCodexAuthPath(relName)
	ids := h.authIDsForCodexPath(path, relName)
	if errDelete := store.Delete(ctx, path); errDelete != nil {
		return errDelete
	}
	h.removeAuthsForPath(ctx, path, relName)
	// Nested files are not seen by the auth-dir watcher, so unregister runtime
	// state here instead of waiting for applyCoreAuthRemoval.
	for _, id := range ids {
		registry.GetGlobalRegistry().UnregisterClient(id)
		executor.CloseCodexWebsocketSessionsForAuthID(id, "oauth_replaced")
	}
	return nil
}

func (h *Handler) authIDsForCodexPath(path, relName string) []string {
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if h != nil && h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil {
				continue
			}
			if sameAuthFilePath(authAttribute(auth, "path"), path) ||
				sameAuthFilePath(authAttribute(auth, coreauth.AttributeVirtualSource), path) ||
				filepath.ToSlash(strings.TrimSpace(auth.FileName)) == relName ||
				filepath.ToSlash(strings.TrimSpace(auth.ID)) == relName {
				add(auth.ID)
			}
		}
	}
	if len(ids) == 0 {
		add(relName)
	}
	return ids
}

func authPayloadAccountID(payload map[string]any) string {
	if id := strings.TrimSpace(jsonString(payload["account_id"])); id != "" {
		return id
	}
	return strings.TrimSpace(jsonString(payload["chatgpt_account_id"]))
}

func jsonString(v any) string {
	s, _ := v.(string)
	return s
}

func truthyJSON(v any) bool {
	switch n := v.(type) {
	case bool:
		return n
	case string:
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func pickCodexKeepAndRemove(siblings []codexAuthFileRef, canonicalName, replaceHint string) (keep string, remove []string) {
	canonicalName = strings.TrimSpace(canonicalName)
	replaceHint = strings.TrimSpace(replaceHint)

	if replaceHint != "" {
		for _, sibling := range siblings {
			if sibling.Name == replaceHint {
				keep = replaceHint
				break
			}
		}
	}
	if keep == "" {
		for _, sibling := range siblings {
			if sibling.Name == canonicalName {
				keep = canonicalName
				break
			}
		}
	}
	if keep == "" && len(siblings) == 1 {
		keep = siblings[0].Name
	}
	if keep == "" && len(siblings) > 1 {
		for _, sibling := range siblings {
			if !sibling.Disabled {
				keep = sibling.Name
				break
			}
		}
		if keep == "" {
			keep = siblings[0].Name
		}
	}

	for _, sibling := range siblings {
		if sibling.Name != keep {
			remove = append(remove, sibling.Name)
		}
	}
	return keep, remove
}

func metadataForCodexRef(siblings []codexAuthFileRef, name string) map[string]any {
	for _, sibling := range siblings {
		if sibling.Name == name {
			return sibling.Metadata
		}
	}
	return nil
}

func mergeCodexOperatorMetadata(record *coreauth.Auth, existingMap map[string]any) {
	if record == nil || len(existingMap) == 0 {
		return
	}
	cloned := cloneMetadataMap(existingMap)
	for _, key := range oauthRuntimeMetadataKeys {
		delete(cloned, key)
	}
	coreauth.MergeExistingAuthMetadata(record, cloned)
}

func cloneMetadataMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
