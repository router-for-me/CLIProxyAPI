// clients.go implements watcher client lifecycle logic and persistence helpers.
// It reloads clients, handles incremental auth file changes, and persists updates when supported.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (w *Watcher) reloadClients(rescanAuth bool, affectedOAuthProviders []string, forceAuthRefresh bool) {
	log.Debugf("starting full client load process")

	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()

	if cfg == nil {
		log.Error("config is nil, cannot reload clients")
		return
	}

	if len(affectedOAuthProviders) > 0 {
		w.clientsMutex.Lock()
		if w.currentAuths != nil {
			filtered := make(map[string]*coreauth.Auth, len(w.currentAuths))
			pendingReload := make(map[string]*coreauth.Auth)
			for id, auth := range w.currentAuths {
				if auth == nil {
					continue
				}
				provider := strings.ToLower(strings.TrimSpace(auth.Provider))
				if _, match := matchProvider(provider, affectedOAuthProviders); match {
					pendingReload[id] = auth
					continue
				}
				filtered[id] = auth
			}
			w.currentAuths = filtered
			w.pendingProviderReloadAuths = pendingReload
			log.Debugf("applying oauth-excluded-models to providers %v", affectedOAuthProviders)
		} else {
			w.currentAuths = nil
			w.pendingProviderReloadAuths = nil
		}
		w.clientsMutex.Unlock()
	}

	geminiAPIKeyCount, vertexCompatAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, xaiAPIKeyCount, metaAPIKeyCount, openAICompatCount := BuildAPIKeyClients(cfg)
	totalAPIKeyClients := geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + xaiAPIKeyCount + metaAPIKeyCount + openAICompatCount
	log.Debugf("loaded %d API key clients", totalAPIKeyClients)

	var authFileCount int
	if rescanAuth {
		authFileCount = w.loadFileClients(cfg)
		log.Debugf("loaded %d file-based clients", authFileCount)
	} else {
		w.clientsMutex.RLock()
		authFileCount = len(w.lastAuthHashes)
		w.clientsMutex.RUnlock()
		log.Debugf("skipping auth directory rescan; retaining %d existing auth files", authFileCount)
	}

	if rescanAuth {
		w.authRescanMu.Lock()
		cacheAuthContents := log.IsLevelEnabled(log.DebugLevel)
		newAuthHashes := make(map[string]string)
		var newAuthContents map[string]*coreauth.Auth
		if cacheAuthContents {
			newAuthContents = make(map[string]*coreauth.Auth)
		}
		newFileAuthsByPath := make(map[string]map[string]*coreauth.Auth)
		var incompleteRescanPaths map[string]struct{}
		var incompleteRescanDir string

		w.clientsMutex.RLock()
		parser := w.pluginAuthParser
		w.clientsMutex.RUnlock()

		if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
			log.Errorf("failed to resolve auth directory for hash cache: %v", errResolveAuthDir)
		} else if resolvedAuthDir != "" {
			entries, errReadDir := os.ReadDir(resolvedAuthDir)
			if errReadDir != nil {
				if !errors.Is(errReadDir, fs.ErrNotExist) {
					incompleteRescanDir = resolvedAuthDir
				}
				log.Errorf("failed to read auth directory for hash cache: %v", errReadDir)
			} else {
				for _, entry := range entries {
					if entry == nil || entry.IsDir() {
						continue
					}
					name := entry.Name()
					if !strings.HasSuffix(strings.ToLower(name), ".json") {
						continue
					}
					fullPath := filepath.Join(resolvedAuthDir, name)
					data, errReadFile := os.ReadFile(fullPath)
					if errReadFile != nil {
						if !errors.Is(errReadFile, fs.ErrNotExist) {
							if incompleteRescanPaths == nil {
								incompleteRescanPaths = make(map[string]struct{})
							}
							incompleteRescanPaths[w.normalizeAuthPath(fullPath)] = struct{}{}
						}
						continue
					}
					normalizedPath := w.normalizeAuthPath(fullPath)
					if !json.Valid(data) {
						if incompleteRescanPaths == nil {
							incompleteRescanPaths = make(map[string]struct{})
						}
						incompleteRescanPaths[normalizedPath] = struct{}{}
						continue
					}
					sum := sha256.Sum256(data)
					newAuthHashes[normalizedPath] = hex.EncodeToString(sum[:])
					// Parse and cache auth content for future diff comparisons (debug only).
					if cacheAuthContents {
						var auth coreauth.Auth
						if errParse := json.Unmarshal(data, &auth); errParse == nil {
							newAuthContents[normalizedPath] = &auth
						}
					}
					ctx := &synthesizer.SynthesisContext{
						Config:           cfg,
						AuthDir:          resolvedAuthDir,
						Now:              time.Now(),
						IDGenerator:      synthesizer.NewStableIDGenerator(),
						PluginAuthParser: parser,
					}
					generated, errSynthesize := synthesizer.SynthesizeAuthFile(ctx, fullPath, data)
					if errSynthesize != nil {
						log.WithError(errSynthesize).Warnf("skipping auth file %s", name)
					} else if len(generated) > 0 {
						if pathAuths := authSliceToMap(generated); len(pathAuths) > 0 {
							newFileAuthsByPath[normalizedPath] = authIDSet(pathAuths)
						}
					}
				}
			}
		}
		w.clientsMutex.Lock()
		if incompleteRescanDir != "" {
			markDirMemberIncomplete := func(trackedPath string) {
				if !w.sameNormalizedAuthPath(filepath.Dir(trackedPath), incompleteRescanDir) {
					return
				}
				if incompleteRescanPaths == nil {
					incompleteRescanPaths = make(map[string]struct{})
				}
				incompleteRescanPaths[trackedPath] = struct{}{}
			}
			for trackedPath := range w.lastAuthHashes {
				markDirMemberIncomplete(trackedPath)
			}
			for trackedPath := range w.fileAuthsByPath {
				markDirMemberIncomplete(trackedPath)
			}
		}
		for normalizedPath := range incompleteRescanPaths {
			if _, ok := w.lastAuthHashes[normalizedPath]; ok {
				newAuthHashes[normalizedPath] = ""
			}
			if cacheAuthContents && w.lastAuthContents != nil {
				if prevContent, ok := w.lastAuthContents[normalizedPath]; ok {
					newAuthContents[normalizedPath] = prevContent
				}
			}
			if prevAuths, ok := w.fileAuthsByPath[normalizedPath]; ok && len(prevAuths) > 0 {
				newFileAuthsByPath[normalizedPath] = maps.Clone(prevAuths)
			}
		}
		// Keep IDs that are still in currentAuths or pending provider reload indexed
		// under their file path until refreshAuthState commits the new state.
		for path, prevAuths := range w.fileAuthsByPath {
			for id := range prevAuths {
				_, stillCurrent := w.currentAuths[id]
				_, pendingReload := w.pendingProviderReloadAuths[id]
				if !stillCurrent && !pendingReload {
					continue
				}
				if newFileAuthsByPath[path] == nil {
					newFileAuthsByPath[path] = make(map[string]*coreauth.Auth)
				}
				newFileAuthsByPath[path][id] = nil
			}
		}
		w.lastAuthHashes = newAuthHashes
		w.lastAuthContents = newAuthContents
		w.fileAuthsByPath = newFileAuthsByPath
		w.clientsMutex.Unlock()
		w.authRescanMu.Unlock()
	}

	totalNewClients := authFileCount + geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + xaiAPIKeyCount + metaAPIKeyCount + openAICompatCount

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback before auth refresh")
		w.reloadCallback(cfg)
	}

	w.refreshAuthState(forceAuthRefresh)
	redisqueue.NotifyUsageRefresh()

	log.Infof("full client load complete - %d clients (%d auth files + %d Gemini API keys + %d Vertex API keys + %d Claude API keys + %d Codex keys + %d xAI keys + %d Meta API keys + %d OpenAI-compat)",
		totalNewClients,
		authFileCount,
		geminiAPIKeyCount,
		vertexCompatAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		xaiAPIKeyCount,
		metaAPIKeyCount,
		openAICompatCount,
	)
}

func (w *Watcher) addOrUpdateClient(path string) {
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()

	w.addOrUpdateClientLocked(path)
}

func (w *Watcher) rereadDiscardedFileSnapshot(auth *coreauth.Auth) {
	if w == nil || auth == nil || !auth.HasFileSnapshot() {
		return
	}
	path := auth.FileSnapshotPath()
	if path == "" {
		return
	}
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()

	w.clientsMutex.Lock()
	existing := w.currentAuths[auth.ID]
	if existing == nil || !existing.SameFileSnapshot(auth) {
		w.clientsMutex.Unlock()
		return
	}
	normalized := w.invalidateAuthFileHashLocked(path)
	if normalized == "" {
		w.clientsMutex.Unlock()
		return
	}
	existing.InvalidateFileSnapshot()
	for id := range w.fileAuthsByPath[normalized] {
		if sibling := w.currentAuths[id]; sibling != nil && sibling.SameFileSnapshot(auth) {
			sibling.InvalidateFileSnapshot()
		}
	}
	w.clientsMutex.Unlock()

	w.addOrUpdateClientLocked(path)
}

func (w *Watcher) sameNormalizedAuthPath(pathA, pathB string) bool {
	normA := w.normalizeAuthPath(pathA)
	normB := w.normalizeAuthPath(pathB)
	if normA == "" || normB == "" {
		return false
	}
	if normA == normB {
		return true
	}
	if absA, errAbsA := filepath.Abs(normA); errAbsA == nil {
		normA = w.normalizeAuthPath(absA)
	}
	if absB, errAbsB := filepath.Abs(normB); errAbsB == nil {
		normB = w.normalizeAuthPath(absB)
	}
	return normA != "" && normA == normB
}

func (w *Watcher) invalidateAuthFileHashLocked(path string) string {
	normalized := w.normalizeAuthPath(path)
	if normalized == "" {
		return ""
	}
	if w.lastAuthHashes == nil {
		w.lastAuthHashes = make(map[string]string)
	}
	w.lastAuthHashes[normalized] = ""
	if abs, errAbs := filepath.Abs(normalized); errAbs == nil {
		if normalizedAbs := w.normalizeAuthPath(abs); normalizedAbs != "" {
			if _, known := w.lastAuthHashes[normalizedAbs]; known {
				w.lastAuthHashes[normalizedAbs] = ""
			}
		}
	}
	return normalized
}

func (w *Watcher) addOrUpdateClientLocked(path string) {
	w.observeAuthFile(path)
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		log.Errorf("failed to read auth file %s: %v", filepath.Base(path), errRead)
		return
	}
	if len(data) == 0 {
		log.Debugf("ignoring empty auth file: %s", filepath.Base(path))
		return
	}

	sum := sha256.Sum256(data)
	curHash := hex.EncodeToString(sum[:])
	normalized := w.normalizeAuthPath(path)

	// Parse new auth content for diff comparison
	var newAuth coreauth.Auth
	if errParse := json.Unmarshal(data, &newAuth); errParse != nil {
		log.Errorf("failed to parse auth file %s: %v", filepath.Base(path), errParse)
		return
	}

	cacheAuthContents := log.IsLevelEnabled(log.DebugLevel)
	w.clientsMutex.Lock()
	if w.config == nil {
		log.Error("config is nil, cannot add or update client")
		w.clientsMutex.Unlock()
		return
	}
	cfg := w.config
	authDir := w.authDir
	parser := w.pluginAuthParser
	if w.fileAuthsByPath == nil {
		w.fileAuthsByPath = make(map[string]map[string]*coreauth.Auth)
	}
	if prev, ok := w.lastAuthHashes[normalized]; ok && prev == curHash {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Get old auth for diff comparison
	var oldAuth *coreauth.Auth
	if cacheAuthContents && w.lastAuthContents != nil {
		if cached := w.lastAuthContents[normalized]; cached != nil {
			oldAuth = cached.Clone()
		}
	}

	// Update caches
	if w.lastAuthHashes == nil {
		w.lastAuthHashes = make(map[string]string)
	}
	w.lastAuthHashes[normalized] = curHash
	if cacheAuthContents {
		if w.lastAuthContents == nil {
			w.lastAuthContents = make(map[string]*coreauth.Auth)
		}
		w.lastAuthContents[normalized] = &newAuth
	}

	oldByID := make(map[string]*coreauth.Auth, len(w.fileAuthsByPath[normalized]))
	for id, a := range w.fileAuthsByPath[normalized] {
		oldByID[id] = a
	}
	w.clientsMutex.Unlock()

	// Compute and log field changes
	if cacheAuthContents {
		if changes := diff.BuildAuthChangeDetails(oldAuth, &newAuth); len(changes) > 0 {
			log.Debugf("auth field changes for %s:", filepath.Base(path))
			for _, c := range changes {
				log.Debugf("  %s", c)
			}
		}
	}

	// Build synthesized auth entries for this single file only.
	sctx := &synthesizer.SynthesisContext{
		Config:           cfg,
		AuthDir:          authDir,
		Now:              time.Now(),
		IDGenerator:      synthesizer.NewStableIDGenerator(),
		PluginAuthParser: parser,
	}
	generated, errSynthesize := synthesizer.SynthesizeAuthFile(sctx, path, data)
	if errSynthesize != nil {
		log.WithError(errSynthesize).Warnf("skipping auth file %s", filepath.Base(path))
	}
	newByID := authSliceToMap(generated)
	w.clientsMutex.Lock()
	if len(newByID) > 0 {
		w.fileAuthsByPath[normalized] = authIDSet(newByID)
	} else {
		delete(w.fileAuthsByPath, normalized)
	}
	updates := w.computePerPathUpdatesLocked(oldByID, newByID)
	w.clientsMutex.Unlock()

	if errSynthesize == nil {
		w.persistAuthAsync(fmt.Sprintf("Sync auth %s", filepath.Base(path)), path)
	}
	w.dispatchAuthUpdates(updates)
	redisqueue.NotifyUsageRefresh()
}

func (w *Watcher) removeClient(path string) {
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()

	w.removeClientLocked(path)
}

func (w *Watcher) removeClientLocked(path string) {
	w.observeAuthFile(path)
	normalized := w.normalizeAuthPath(path)
	w.clientsMutex.Lock()
	oldByID := make(map[string]*coreauth.Auth, len(w.fileAuthsByPath[normalized]))
	for id, a := range w.fileAuthsByPath[normalized] {
		oldByID[id] = a
	}
	delete(w.lastAuthHashes, normalized)
	delete(w.lastAuthContents, normalized)
	delete(w.fileAuthsByPath, normalized)

	updates := w.computePerPathUpdatesLocked(oldByID, map[string]*coreauth.Auth{})
	// Only the whole-file removal path gets this mark. Content diffs may drop
	// individual plugin auth IDs while their source file still exists.
	for index := range updates {
		if updates[index].Action == AuthUpdateActionDelete {
			updates[index].SetFileRemoval(true)
		}
	}
	w.clientsMutex.Unlock()

	w.persistAuthAsync(fmt.Sprintf("Remove auth %s", filepath.Base(path)), path)
	w.dispatchAuthUpdates(updates)
	redisqueue.NotifyUsageRefresh()
}

func (w *Watcher) computePerPathUpdatesLocked(oldByID, newByID map[string]*coreauth.Auth) []AuthUpdate {
	if w.currentAuths == nil {
		w.currentAuths = make(map[string]*coreauth.Auth)
	}
	updates := make([]AuthUpdate, 0, len(oldByID)+len(newByID))
	for id, newAuth := range newByID {
		delete(w.persistedAuthRevisions, id)
		existing, ok := w.currentAuths[id]
		if !ok {
			w.currentAuths[id] = newAuth.Clone()
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionAdd, ID: id, Auth: newAuth.Clone()})
			continue
		}
		w.currentAuths[id] = newAuth.Clone()
		if !authEqual(existing, newAuth) {
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionModify, ID: id, Auth: newAuth.Clone()})
		}
	}
	for id := range oldByID {
		if _, stillExists := newByID[id]; stillExists {
			continue
		}
		delete(w.persistedAuthRevisions, id)
		delete(w.currentAuths, id)
		if w.pendingProviderReloadAuths != nil {
			delete(w.pendingProviderReloadAuths, id)
		}
		updates = append(updates, AuthUpdate{Action: AuthUpdateActionDelete, ID: id})
	}
	w.stampAuthUpdatesLocked(updates)
	return updates
}

func authSliceToMap(auths []*coreauth.Auth) map[string]*coreauth.Auth {
	byID := make(map[string]*coreauth.Auth, len(auths))
	for _, a := range auths {
		if a == nil || strings.TrimSpace(a.ID) == "" {
			continue
		}
		byID[a.ID] = a
	}
	return byID
}

func authIDSet(auths map[string]*coreauth.Auth) map[string]*coreauth.Auth {
	set := make(map[string]*coreauth.Auth, len(auths))
	for id := range auths {
		set[id] = nil
	}
	return set
}

func (w *Watcher) loadFileClients(cfg *config.Config) int {
	authFileCount := 0
	successfulAuthCount := 0

	authDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir)
	if errResolveAuthDir != nil {
		log.Errorf("failed to resolve auth directory: %v", errResolveAuthDir)
		return 0
	}
	if authDir == "" {
		return 0
	}

	entries, errReadDir := os.ReadDir(authDir)
	if errReadDir != nil {
		log.Errorf("error reading auth directory: %v", errReadDir)
		return 0
	}
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		authFileCount++
		log.Debugf("processing auth file %d: %s", authFileCount, name)
		fullPath := filepath.Join(authDir, name)
		if data, errReadFile := os.ReadFile(fullPath); errReadFile == nil && len(data) > 0 {
			successfulAuthCount++
		}
	}
	log.Debugf("auth directory scan complete - found %d .json files, %d readable", authFileCount, successfulAuthCount)
	return authFileCount
}

func BuildAPIKeyClients(cfg *config.Config) (int, int, int, int, int, int, int) {
	geminiAPIKeyCount := 0
	vertexCompatAPIKeyCount := 0
	claudeAPIKeyCount := 0
	codexAPIKeyCount := 0
	xaiAPIKeyCount := 0
	metaAPIKeyCount := 0
	openAICompatCount := 0

	if len(cfg.GeminiKey) > 0 {
		geminiAPIKeyCount += len(cfg.GeminiKey)
	}
	if len(cfg.InteractionsKey) > 0 {
		geminiAPIKeyCount += len(cfg.InteractionsKey)
	}
	if len(cfg.VertexCompatAPIKey) > 0 {
		vertexCompatAPIKeyCount += len(cfg.VertexCompatAPIKey)
	}
	if len(cfg.ClaudeKey) > 0 {
		claudeAPIKeyCount += len(cfg.ClaudeKey)
	}
	if len(cfg.CodexKey) > 0 {
		codexAPIKeyCount += len(cfg.CodexKey)
	}
	if len(cfg.XAIKey) > 0 {
		xaiAPIKeyCount += len(cfg.XAIKey)
	}
	if len(cfg.MetaKey) > 0 {
		metaAPIKeyCount += len(cfg.MetaKey)
	}
	if len(cfg.OpenAICompatibility) > 0 {
		for _, compatConfig := range cfg.OpenAICompatibility {
			if compatConfig.Disabled {
				continue
			}
			openAICompatCount += len(compatConfig.APIKeyEntries)
		}
	}
	return geminiAPIKeyCount, vertexCompatAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, xaiAPIKeyCount, metaAPIKeyCount, openAICompatCount
}

func (w *Watcher) persistConfigAsync() {
	if w == nil || w.storePersister == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.storePersister.PersistConfig(ctx); err != nil {
			log.Errorf("failed to persist config change: %v", err)
		}
	}()
}

func (w *Watcher) persistAuthAsync(message string, paths ...string) {
	if w == nil || w.storePersister == nil {
		return
	}
	filtered := make([]string, 0, len(paths))
	for _, p := range paths {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	if len(filtered) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.storePersister.PersistAuthFiles(ctx, message, filtered...); err != nil {
			log.Errorf("failed to persist auth changes: %v", err)
		}
	}()
}

func (w *Watcher) stopServerUpdateTimer() {
	w.serverUpdateMu.Lock()
	defer w.serverUpdateMu.Unlock()
	if w.serverUpdateTimer != nil {
		w.serverUpdateTimer.Stop()
		w.serverUpdateTimer = nil
	}
	w.serverUpdatePend = false
}

func (w *Watcher) triggerServerUpdate(cfg *config.Config) {
	if w == nil || w.reloadCallback == nil || cfg == nil {
		return
	}
	if w.stopped.Load() {
		return
	}

	now := time.Now()

	w.serverUpdateMu.Lock()
	if w.serverUpdateLast.IsZero() || now.Sub(w.serverUpdateLast) >= serverUpdateDebounce {
		w.serverUpdateLast = now
		if w.serverUpdateTimer != nil {
			w.serverUpdateTimer.Stop()
			w.serverUpdateTimer = nil
		}
		w.serverUpdatePend = false
		w.serverUpdateMu.Unlock()
		w.reloadCallback(cfg)
		return
	}

	if w.serverUpdatePend {
		w.serverUpdateMu.Unlock()
		return
	}

	delay := serverUpdateDebounce - now.Sub(w.serverUpdateLast)
	if delay < 10*time.Millisecond {
		delay = 10 * time.Millisecond
	}
	w.serverUpdatePend = true
	if w.serverUpdateTimer != nil {
		w.serverUpdateTimer.Stop()
		w.serverUpdateTimer = nil
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		if w.stopped.Load() {
			return
		}
		w.clientsMutex.RLock()
		latestCfg := w.config
		w.clientsMutex.RUnlock()

		w.serverUpdateMu.Lock()
		if w.serverUpdateTimer != timer || !w.serverUpdatePend {
			w.serverUpdateMu.Unlock()
			return
		}
		w.serverUpdateTimer = nil
		w.serverUpdatePend = false
		if latestCfg == nil || w.reloadCallback == nil || w.stopped.Load() {
			w.serverUpdateMu.Unlock()
			return
		}

		w.serverUpdateLast = time.Now()
		w.serverUpdateMu.Unlock()
		w.reloadCallback(latestCfg)
	})
	w.serverUpdateTimer = timer
	w.serverUpdateMu.Unlock()
}
