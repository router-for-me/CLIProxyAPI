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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var errAuthFileTooLarge = errors.New("auth file exceeds allowed size")

func (w *Watcher) readAuthFile(path string) ([]byte, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return nil, errStat
	}
	if info.IsDir() {
		return nil, fmt.Errorf("auth path is directory: %s", path)
	}
	if info.Size() > maxAuthFileSize {
		return nil, fmt.Errorf("%w: size=%d limit=%d", errAuthFileTooLarge, info.Size(), maxAuthFileSize)
	}

	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(data)) > maxAuthFileSize {
		return nil, fmt.Errorf("%w: size=%d limit=%d", errAuthFileTooLarge, len(data), maxAuthFileSize)
	}
	return data, nil
}

func logAuthReadFailure(path string, err error) {
	if errors.Is(err, errAuthFileTooLarge) {
		log.Warnf("skipping oversized auth file %s: %v", filepath.Base(path), err)
		return
	}
	log.Errorf("failed to read auth file %s: %v", filepath.Base(path), err)
}

func sanitizeAuthFieldChange(change string) string {
	trimmed := strings.TrimSpace(change)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, keyword := range authSensitiveLogKeywords {
		if strings.Contains(lower, keyword) {
			return "[sensitive auth field change redacted]"
		}
	}
	return trimmed
}

func (w *Watcher) isPathWithinBase(basePath, targetPath string) bool {
	base := w.normalizeAuthPath(basePath)
	target := w.normalizeAuthPath(targetPath)
	if base == "" || target == "" {
		return false
	}
	rel, errRel := filepath.Rel(base, target)
	if errRel != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

func (w *Watcher) isAuthWalkPathAllowed(path, authDir, resolvedAuthDir string) bool {
	if !w.isPathWithinBase(authDir, path) {
		return false
	}
	if strings.TrimSpace(resolvedAuthDir) == "" {
		return true
	}
	resolvedPath, errEval := filepath.EvalSymlinks(path)
	if errEval != nil {
		log.WithError(errEval).Debugf("auth path symlink resolution failed, fallback to lexical boundary check: %s", filepath.Base(path))
		return true
	}
	return w.isPathWithinBase(resolvedAuthDir, resolvedPath)
}

func (w *Watcher) invokeReloadCallback(cfg *config.Config, reason string) bool {
	if w == nil || cfg == nil || w.reloadCallback == nil {
		return false
	}
	if w.stopped.Load() {
		if reason != "" {
			log.Debugf("watcher stopped, skip reload callback: %s", reason)
		}
		return false
	}
	w.reloadCallback(cfg)
	return true
}

func (w *Watcher) reloadClients(rescanAuth bool, affectedOAuthProviders []string, forceAuthRefresh bool) {
	if w.stopped.Load() {
		log.Debug("watcher stopped, skipping client reload")
		return
	}
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
		w.lastAuthContents = make(map[string]*coreauth.Auth)
		if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
			log.Errorf("failed to resolve auth directory for hash cache: %v", errResolveAuthDir)
		} else if resolvedAuthDir != "" {
			resolvedWalkRoot := ""
			if root, errEval := filepath.EvalSymlinks(resolvedAuthDir); errEval == nil {
				resolvedWalkRoot = root
			} else {
				log.WithError(errEval).Debugf("failed to resolve auth walk root symlink: %s", resolvedAuthDir)
			}
			_ = filepath.Walk(resolvedAuthDir, func(path string, info fs.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !w.isAuthWalkPathAllowed(path, resolvedAuthDir, resolvedWalkRoot) {
					log.Warnf("skipping auth path outside authDir boundary: %s", path)
					if info != nil && info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
					data, errReadFile := w.readAuthFile(path)
					if errReadFile != nil {
						logAuthReadFailure(path, errReadFile)
						return nil
					}
					if len(data) == 0 {
						return nil
					}
					sum := sha256.Sum256(data)
					normalizedPath := w.normalizeAuthPath(path)
					w.lastAuthHashes[normalizedPath] = hex.EncodeToString(sum[:])
					// Parse and cache auth content for future diff comparisons
					var auth coreauth.Auth
					if errParse := json.Unmarshal(data, &auth); errParse == nil {
						w.lastAuthContents[normalizedPath] = &auth
					} else {
						log.Errorf("failed to parse auth file %s during hash cache preload: %v", filepath.Base(path), errParse)
					}
				}
				return nil
			})
		}
		w.clientsMutex.Unlock()
	}

	totalNewClients := authFileCount + geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + openAICompatCount

	if w.invokeReloadCallback(cfg, "full reload before auth refresh") {
		log.Debugf("triggering server update callback before auth refresh")
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
	data, errRead := w.readAuthFile(path)
	if errRead != nil {
		logAuthReadFailure(path, errRead)
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

	cfg := w.config
	if cfg == nil {
		log.Error("config is nil, cannot add or update client")
		w.clientsMutex.Unlock()
		return
	}
	if prev, ok := w.lastAuthHashes[normalized]; ok && prev == curHash {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Get old auth for diff comparison
	var oldAuth *coreauth.Auth
	if w.lastAuthContents != nil {
		oldAuth = w.lastAuthContents[normalized]
	}

	// Compute and log field changes
	if changes := diff.BuildAuthChangeDetails(oldAuth, &newAuth); len(changes) > 0 {
		log.Debugf("auth field changes for %s:", filepath.Base(path))
		for _, c := range changes {
			if sanitized := sanitizeAuthFieldChange(c); sanitized != "" {
				log.Debugf("  %s", sanitized)
			}
		}
	}

	// Update caches
	w.lastAuthHashes[normalized] = curHash
	if w.lastAuthContents == nil {
		w.lastAuthContents = make(map[string]*coreauth.Auth)
	}
	w.lastAuthContents[normalized] = &newAuth

	w.clientsMutex.Unlock() // Unlock before the callback

	w.refreshAuthState(false)

	log.Debugf("triggering server update callback after add/update")
	w.triggerServerUpdate(cfg)
	w.persistAuthAsync(fmt.Sprintf("Sync auth %s", filepath.Base(path)), path)
}

func (w *Watcher) removeClient(path string) {
	normalized := w.normalizeAuthPath(path)
	w.clientsMutex.Lock()

	cfg := w.config
	delete(w.lastAuthHashes, normalized)
	delete(w.lastAuthContents, normalized)

	w.clientsMutex.Unlock() // Release the lock before the callback

	w.refreshAuthState(false)

	log.Debugf("triggering server update callback after removal")
	w.triggerServerUpdate(cfg)
	w.persistAuthAsync(fmt.Sprintf("Remove auth %s", filepath.Base(path)), path)
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
	resolvedWalkRoot := ""
	if root, errEval := filepath.EvalSymlinks(authDir); errEval == nil {
		resolvedWalkRoot = root
	} else {
		log.WithError(errEval).Debugf("failed to resolve auth walk root symlink: %s", authDir)
	}

	errWalk := filepath.Walk(authDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			log.Debugf("error accessing path %s: %v", path, err)
			return err
		}
		if !w.isAuthWalkPathAllowed(path, authDir, resolvedWalkRoot) {
			log.Warnf("skipping auth path outside authDir boundary: %s", path)
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") {
			authFileCount++
			log.Debugf("processing auth file %d: %s", authFileCount, filepath.Base(path))
			data, errCreate := w.readAuthFile(path)
			if errCreate != nil {
				logAuthReadFailure(path, errCreate)
				return nil
			}
			if len(data) > 0 {
				successfulAuthCount++
			}
		}
		return nil
	})

	if errWalk != nil {
		log.Errorf("error walking auth directory: %v", errWalk)
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
		w.serverUpdateMu.Unlock()
		w.invokeReloadCallback(cfg, "server update immediate")
		return
	}

	if w.serverUpdatePend {
		w.serverUpdateMu.Unlock()
		return
	}

	delay := serverUpdateDebounce - now.Sub(w.serverUpdateLast)
	if delay < minServerUpdateDelay {
		delay = minServerUpdateDelay
	}
	w.serverUpdatePend = true
	if w.serverUpdateTimer != nil {
		w.serverUpdateTimer.Stop()
	}
	w.serverUpdateTimer = time.AfterFunc(delay, func() {
		if w.stopped.Load() {
			w.serverUpdateMu.Lock()
			w.serverUpdatePend = false
			w.serverUpdateTimer = nil
			w.serverUpdateMu.Unlock()
			return
		}
		w.clientsMutex.RLock()
		latestCfg := w.config
		w.clientsMutex.RUnlock()
		if latestCfg == nil || w.stopped.Load() {
			w.serverUpdateMu.Lock()
			w.serverUpdatePend = false
			w.serverUpdateTimer = nil
			w.serverUpdateMu.Unlock()
			return
		}

		w.serverUpdateMu.Lock()
		w.serverUpdateLast = time.Now()
		w.serverUpdatePend = false
		w.serverUpdateTimer = nil
		w.serverUpdateMu.Unlock()
		w.invokeReloadCallback(latestCfg, "server update deferred")
	})
	w.serverUpdateMu.Unlock()
}
