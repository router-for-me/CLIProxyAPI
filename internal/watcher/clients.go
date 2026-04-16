// clients.go implements watcher client lifecycle logic and persistence helpers.
// It reloads clients, handles incremental auth file changes, and persists updates when supported.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
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
			for id, auth := range w.currentAuths {
				if auth == nil {
					continue
				}
				provider := strings.ToLower(strings.TrimSpace(auth.Provider))
				if _, match := matchProvider(provider, affectedOAuthProviders); match {
					continue
				}
				filtered[id] = auth
			}
			w.currentAuths = filtered
			log.Debugf("applying oauth-excluded-models to providers %v", affectedOAuthProviders)
		} else {
			w.currentAuths = nil
		}
		w.clientsMutex.Unlock()
	}

	geminiAPIKeyCount, vertexCompatAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount := BuildAPIKeyClients(cfg)
	totalAPIKeyClients := geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + openAICompatCount
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
		w.clientsMutex.Lock()

		w.lastAuthHashes = make(map[string]string)
		cacheAuthContents := log.IsLevelEnabled(log.DebugLevel)
		if cacheAuthContents {
			w.lastAuthContents = make(map[string]*coreauth.Auth)
		} else {
			w.lastAuthContents = nil
		}
		w.fileAuthsByPath = make(map[string]map[string]*coreauth.Auth)
		if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
			log.Errorf("failed to resolve auth directory for hash cache: %v", errResolveAuthDir)
		} else if resolvedAuthDir != "" {
			entries, errReadDir := os.ReadDir(resolvedAuthDir)
			if errReadDir != nil {
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
					if data, errReadFile := os.ReadFile(fullPath); errReadFile == nil && len(data) > 0 {
						sum := sha256.Sum256(data)
						normalizedPath := w.normalizeAuthPath(fullPath)
						w.lastAuthHashes[normalizedPath] = hex.EncodeToString(sum[:])
						// Parse and cache auth content for future diff comparisons (debug only).
						if cacheAuthContents {
							var auth coreauth.Auth
							if errParse := json.Unmarshal(data, &auth); errParse == nil {
								w.lastAuthContents[normalizedPath] = &auth
							}
						}
						ctx := &synthesizer.SynthesisContext{
							Config:      cfg,
							AuthDir:     resolvedAuthDir,
							Now:         time.Now(),
							IDGenerator: synthesizer.NewStableIDGenerator(),
						}
						if generated := synthesizer.SynthesizeAuthFile(ctx, fullPath, data); len(generated) > 0 {
							if pathAuths := authSliceToMap(generated); len(pathAuths) > 0 {
								w.fileAuthsByPath[normalizedPath] = authIDSet(pathAuths)
							}
						}
					}
				}
			}
		}
		w.clientsMutex.Unlock()
	}

	totalNewClients := authFileCount + geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + openAICompatCount

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback before auth refresh")
		w.reloadCallback(cfg)
	}

	w.refreshAuthState(forceAuthRefresh)

	log.Infof("full client load complete - %d clients (%d auth files + %d Gemini API keys + %d Vertex API keys + %d Claude API keys + %d Codex keys + %d OpenAI-compat)",
		totalNewClients,
		authFileCount,
		geminiAPIKeyCount,
		vertexCompatAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		openAICompatCount,
	)
}

func (w *Watcher) addOrUpdateClient(path string) {
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

	w.clientsMutex.Lock()
	if w.config == nil {
		log.Error("config is nil, cannot add or update client")
		w.clientsMutex.Unlock()
		return
	}
	if w.fileAuthsByPath == nil {
		w.fileAuthsByPath = make(map[string]map[string]*coreauth.Auth)
	}
	if prev, ok := w.lastAuthHashes[normalized]; ok && prev == curHash {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Get old auth for diff comparison
	cacheAuthContents := log.IsLevelEnabled(log.DebugLevel)
	var oldAuth *coreauth.Auth
	if cacheAuthContents && w.lastAuthContents != nil {
		oldAuth = w.lastAuthContents[normalized]
	}

	// Compute and log field changes
	if cacheAuthContents {
		if changes := diff.BuildAuthChangeDetails(oldAuth, &newAuth); len(changes) > 0 {
			log.Debugf("auth field changes for %s:", filepath.Base(path))
			for _, c := range changes {
				log.Debugf("  %s", c)
			}
		}
	}

	// Update caches
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

	// Build synthesized auth entries for this single file only.
	sctx := &synthesizer.SynthesisContext{
		Config:      w.config,
		AuthDir:     w.authDir,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	generated := synthesizer.SynthesizeAuthFile(sctx, path, data)
	newByID := authSliceToMap(generated)
	if len(newByID) > 0 {
		w.fileAuthsByPath[normalized] = authIDSet(newByID)
	} else {
		delete(w.fileAuthsByPath, normalized)
	}
	updates := w.computePerPathUpdatesLocked(oldByID, newByID)
	w.clientsMutex.Unlock()

	w.persistAuthAsync(fmt.Sprintf("Sync auth %s", filepath.Base(path)), path)
	w.dispatchAuthUpdates(updates)
}

func (w *Watcher) removeClient(path string) {
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
	w.clientsMutex.Unlock()

	w.persistAuthAsync(fmt.Sprintf("Remove auth %s", filepath.Base(path)), path)
	w.dispatchAuthUpdates(updates)
}

func (w *Watcher) computePerPathUpdatesLocked(oldByID, newByID map[string]*coreauth.Auth) []AuthUpdate {
	if w.currentAuths == nil {
		w.currentAuths = make(map[string]*coreauth.Auth)
	}
	updates := make([]AuthUpdate, 0, len(oldByID)+len(newByID))
	for id, newAuth := range newByID {
		existing, ok := w.currentAuths[id]
		if !ok {
			w.currentAuths[id] = newAuth.Clone()
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionAdd, ID: id, Auth: newAuth.Clone()})
			continue
		}
		if !authEqual(existing, newAuth) {
			w.currentAuths[id] = newAuth.Clone()
			updates = append(updates, AuthUpdate{Action: AuthUpdateActionModify, ID: id, Auth: newAuth.Clone()})
		}
	}
	for id := range oldByID {
		if _, stillExists := newByID[id]; stillExists {
			continue
		}
		delete(w.currentAuths, id)
		updates = append(updates, AuthUpdate{Action: AuthUpdateActionDelete, ID: id})
	}
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

func BuildAPIKeyClients(cfg *config.Config) (int, int, int, int, int) {
	geminiAPIKeyCount := 0
	vertexCompatAPIKeyCount := 0
	claudeAPIKeyCount := 0
	codexAPIKeyCount := 0
	openAICompatCount := 0

	if len(cfg.GeminiKey) > 0 {
		geminiAPIKeyCount += len(cfg.GeminiKey)
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
	if len(cfg.OpenAICompatibility) > 0 {
		for _, compatConfig := range cfg.OpenAICompatibility {
			openAICompatCount += len(compatConfig.APIKeyEntries)
		}
	}
	return geminiAPIKeyCount, vertexCompatAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount
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
