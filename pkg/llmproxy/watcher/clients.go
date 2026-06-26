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

<<<<<<< HEAD:pkg/llmproxy/watcher/clients.go
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/util"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/watcher/diff"
	coreauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
=======
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
>>>>>>> upstream/main:internal/watcher/clients.go
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

	geminiClientCount, vertexCompatClientCount, claudeClientCount, codexClientCount, openAICompatCount := BuildAPIKeyClients(cfg)
	logAPIKeyClientCount(geminiClientCount + vertexCompatClientCount + claudeClientCount + codexClientCount + openAICompatCount)

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
<<<<<<< HEAD:pkg/llmproxy/watcher/clients.go
		w.clientsMutex.Lock()

		w.lastAuthHashes = make(map[string]string)
		w.lastAuthContents = make(map[string]*coreauth.Auth)
=======
		w.authRescanMu.Lock()
		cacheAuthContents := log.IsLevelEnabled(log.DebugLevel)
		newAuthHashes := make(map[string]string)
		var newAuthContents map[string]*coreauth.Auth
		if cacheAuthContents {
			newAuthContents = make(map[string]*coreauth.Auth)
		}
		newFileAuthsByPath := make(map[string]map[string]*coreauth.Auth)

		w.clientsMutex.RLock()
		parser := w.pluginAuthParser
		w.clientsMutex.RUnlock()

>>>>>>> upstream/main:internal/watcher/clients.go
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
<<<<<<< HEAD:pkg/llmproxy/watcher/clients.go
						normalizedPath := w.normalizeAuthPath(path)
						w.lastAuthHashes[normalizedPath] = hex.EncodeToString(sum[:])
						// Parse and cache auth content for future diff comparisons
						var auth coreauth.Auth
						if errParse := json.Unmarshal(data, &auth); errParse == nil {
							w.lastAuthContents[normalizedPath] = &auth
=======
						normalizedPath := w.normalizeAuthPath(fullPath)
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
						if generated := synthesizer.SynthesizeAuthFile(ctx, fullPath, data); len(generated) > 0 {
							if pathAuths := authSliceToMap(generated); len(pathAuths) > 0 {
								newFileAuthsByPath[normalizedPath] = authIDSet(pathAuths)
							}
>>>>>>> upstream/main:internal/watcher/clients.go
						}
					}
				}
			}
		}
		w.clientsMutex.Lock()
		w.lastAuthHashes = newAuthHashes
		w.lastAuthContents = newAuthContents
		w.fileAuthsByPath = newFileAuthsByPath
		w.clientsMutex.Unlock()
		w.authRescanMu.Unlock()
	}

	totalNewClients := authFileCount + geminiClientCount + vertexCompatClientCount + claudeClientCount + codexClientCount + openAICompatCount

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback before auth refresh")
		w.reloadCallback(cfg)
	}

	w.refreshAuthState(forceAuthRefresh)
	redisqueue.NotifyUsageRefresh()

	log.Infof("full client load complete - %d clients (%d auth files + %d Gemini API keys + %d Vertex API keys + %d Claude API keys + %d Codex keys + %d OpenAI-compat)",
		totalNewClients,
		authFileCount,
		geminiClientCount,
		vertexCompatClientCount,
		claudeClientCount,
		codexClientCount,
		openAICompatCount,
	)
}

func (w *Watcher) addOrUpdateClient(path string) {
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()

	w.addOrUpdateClientLocked(path)
}

func (w *Watcher) addOrUpdateClientLocked(path string) {
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

	cfg := w.config
	if cfg == nil {
		log.Error("config is nil, cannot add or update client")
		w.clientsMutex.Unlock()
		return
	}
<<<<<<< HEAD:pkg/llmproxy/watcher/clients.go
=======
	cfg := w.config
	authDir := w.authDir
	parser := w.pluginAuthParser
	if w.fileAuthsByPath == nil {
		w.fileAuthsByPath = make(map[string]map[string]*coreauth.Auth)
	}
>>>>>>> upstream/main:internal/watcher/clients.go
	if prev, ok := w.lastAuthHashes[normalized]; ok && prev == curHash {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Get old auth for diff comparison
	var oldAuth *coreauth.Auth
<<<<<<< HEAD:pkg/llmproxy/watcher/clients.go
	if w.lastAuthContents != nil {
		oldAuth = w.lastAuthContents[normalized]
	}

	// Compute and log field changes
	if changes := diff.BuildAuthChangeDetails(oldAuth, &newAuth); len(changes) > 0 {
		log.Debugf("auth field changes for %s:", filepath.Base(path))
		for _, c := range changes {
			log.Debugf("  %s", c)
=======
	if cacheAuthContents && w.lastAuthContents != nil {
		if cached := w.lastAuthContents[normalized]; cached != nil {
			oldAuth = cached.Clone()
>>>>>>> upstream/main:internal/watcher/clients.go
		}
	}

	// Update caches
	if w.lastAuthHashes == nil {
		w.lastAuthHashes = make(map[string]string)
	}
	w.lastAuthHashes[normalized] = curHash
	if w.lastAuthContents == nil {
		w.lastAuthContents = make(map[string]*coreauth.Auth)
	}
	w.lastAuthContents[normalized] = &newAuth

<<<<<<< HEAD:pkg/llmproxy/watcher/clients.go
	w.clientsMutex.Unlock() // Unlock before the callback

	w.refreshAuthState(false)
=======
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
	generated := synthesizer.SynthesizeAuthFile(sctx, path, data)
	newByID := authSliceToMap(generated)
	w.clientsMutex.Lock()
	if len(newByID) > 0 {
		w.fileAuthsByPath[normalized] = authIDSet(newByID)
	} else {
		delete(w.fileAuthsByPath, normalized)
	}
	updates := w.computePerPathUpdatesLocked(oldByID, newByID)
	w.clientsMutex.Unlock()
>>>>>>> upstream/main:internal/watcher/clients.go

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after add/update")
		w.reloadCallback(cfg)
	}
	w.persistAuthAsync(fmt.Sprintf("Sync auth %s", filepath.Base(path)), path)
<<<<<<< HEAD:pkg/llmproxy/watcher/clients.go
=======
	w.dispatchAuthUpdates(updates)
	redisqueue.NotifyUsageRefresh()
>>>>>>> upstream/main:internal/watcher/clients.go
}

func (w *Watcher) removeClient(path string) {
	w.authRescanMu.Lock()
	defer w.authRescanMu.Unlock()

	w.removeClientLocked(path)
}

func (w *Watcher) removeClientLocked(path string) {
	normalized := w.normalizeAuthPath(path)
	w.clientsMutex.Lock()

	cfg := w.config
	delete(w.lastAuthHashes, normalized)
	delete(w.lastAuthContents, normalized)

	w.clientsMutex.Unlock() // Release the lock before the callback

	w.refreshAuthState(false)

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after removal")
		w.reloadCallback(cfg)
	}
	w.persistAuthAsync(fmt.Sprintf("Remove auth %s", filepath.Base(path)), path)
<<<<<<< HEAD:pkg/llmproxy/watcher/clients.go
=======
	w.dispatchAuthUpdates(updates)
	redisqueue.NotifyUsageRefresh()
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
>>>>>>> upstream/main:internal/watcher/clients.go
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

// logAPIKeyClientCount logs the total number of API key clients loaded.
// Extracted to a separate function so that integer counts derived from config
// are not passed directly into log call sites alongside config-tainted values.
func logAPIKeyClientCount(total int) {
	log.Debugf("loaded %d API key clients", total)
}

func BuildAPIKeyClients(cfg *config.Config) (int, int, int, int, int) {
	geminiClientCount := 0
	vertexCompatClientCount := 0
	claudeClientCount := 0
	codexClientCount := 0
	openAICompatCount := 0

	if len(cfg.GeminiKey) > 0 {
		geminiClientCount += len(cfg.GeminiKey)
	}
	if len(cfg.VertexCompatAPIKey) > 0 {
		vertexCompatClientCount += len(cfg.VertexCompatAPIKey)
	}
	if len(cfg.ClaudeKey) > 0 {
		claudeClientCount += len(cfg.ClaudeKey)
	}
	if len(cfg.CodexKey) > 0 {
		codexClientCount += len(cfg.CodexKey)
	}
	if len(cfg.OpenAICompatibility) > 0 {
		for _, compatConfig := range cfg.OpenAICompatibility {
			if compatConfig.Disabled {
				continue
			}
			openAICompatCount += len(compatConfig.APIKeyEntries)
		}
	}
	return geminiClientCount, vertexCompatClientCount, claudeClientCount, codexClientCount, openAICompatCount
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
