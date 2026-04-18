// config_reload.go implements debounced configuration hot reload.
// It detects material changes and reloads clients when the config changes.
package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	"gopkg.in/yaml.v3"

	log "github.com/sirupsen/logrus"
)

func (w *Watcher) stopConfigReloadTimer() {
	w.configReloadMu.Lock()
	if w.configReloadTimer != nil {
		w.configReloadTimer.Stop()
		w.configReloadTimer = nil
	}
	w.configReloadPend = false
	w.configReloadMu.Unlock()
}

func (w *Watcher) scheduleConfigReload() {
	if w == nil || w.stopped.Load() {
		return
	}
	w.configReloadMu.Lock()
	w.scheduleConfigReloadLocked(configReloadDebounce)
	w.configReloadMu.Unlock()
}

func (w *Watcher) scheduleConfigReloadLocked(baseDelay time.Duration) {
	if w == nil {
		return
	}
	if baseDelay < 10*time.Millisecond {
		baseDelay = 10 * time.Millisecond
	}
	w.configReloadPend = true
	if w.configReloadRun {
		log.Debug("config reload already running; coalescing latest config change")
		return
	}
	delay := baseDelay
	if !w.configReloadLast.IsZero() {
		if remaining := configReloadCooldown - time.Since(w.configReloadLast); remaining > delay {
			delay = remaining
		}
	}
	if delay > baseDelay {
		log.Debugf("deferring config reload for %s to honor reload cooldown", delay)
	}
	if w.configReloadTimer != nil {
		w.configReloadTimer.Stop()
	}
	w.configReloadTimer = time.AfterFunc(delay, func() {
		w.runScheduledConfigReload()
	})
}

func (w *Watcher) runScheduledConfigReload() {
	if w == nil || w.stopped.Load() {
		return
	}
	w.configReloadMu.Lock()
	if w.configReloadRun {
		w.configReloadMu.Unlock()
		return
	}
	w.configReloadTimer = nil
	if !w.configReloadPend {
		w.configReloadMu.Unlock()
		return
	}
	w.configReloadPend = false
	w.configReloadRun = true
	w.configReloadMu.Unlock()

	reloaded := w.reloadConfigIfChanged()

	w.configReloadMu.Lock()
	w.configReloadRun = false
	if reloaded {
		w.configReloadLast = time.Now()
	}
	if w.configReloadPend && !w.stopped.Load() {
		w.scheduleConfigReloadLocked(configReloadDebounce)
	}
	w.configReloadMu.Unlock()
}

func (w *Watcher) reloadConfigIfChanged() bool {
	data, err := os.ReadFile(w.configPath)
	if err != nil {
		log.Errorf("failed to read config file for hash check: %v", err)
		return false
	}
	if len(data) == 0 {
		log.Debugf("ignoring empty config file write event")
		return false
	}
	sum := sha256.Sum256(data)
	newHash := hex.EncodeToString(sum[:])

	w.clientsMutex.RLock()
	currentHash := w.lastConfigHash
	w.clientsMutex.RUnlock()

	if currentHash != "" && currentHash == newHash {
		log.Debugf("config file content unchanged (hash match), skipping reload")
		return false
	}
	log.Infof("config file changed, reloading: %s", w.configPath)
	if w.reloadConfig() {
		w.clientsMutex.Lock()
		// Record the hash of the config content that was actually applied.
		// If the file changes again while reload is running, a pending reload
		// should still observe a new hash and apply the latest content.
		w.lastConfigHash = newHash
		w.clientsMutex.Unlock()
		w.persistConfigAsync()
		return true
	}
	return false
}

func (w *Watcher) reloadConfig() bool {
	log.Debug("=========================== CONFIG RELOAD ============================")
	log.Debugf("starting config reload from: %s", w.configPath)

	newConfig, errLoadConfig := config.LoadConfig(w.configPath)
	if errLoadConfig != nil {
		log.Errorf("failed to reload config: %v", errLoadConfig)
		return false
	}

	if w.mirroredAuthDir != "" {
		newConfig.AuthDir = w.mirroredAuthDir
	} else {
		if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(newConfig.AuthDir); errResolveAuthDir != nil {
			log.Errorf("failed to resolve auth directory from config: %v", errResolveAuthDir)
		} else {
			newConfig.AuthDir = resolvedAuthDir
		}
	}

	w.clientsMutex.Lock()
	var oldConfig *config.Config
	_ = yaml.Unmarshal(w.oldConfigYaml, &oldConfig)
	w.oldConfigYaml, _ = yaml.Marshal(newConfig)
	w.config = newConfig
	w.clientsMutex.Unlock()

	var affectedOAuthProviders []string
	if oldConfig != nil {
		_, affectedOAuthProviders = diff.DiffOAuthExcludedModelChanges(oldConfig.OAuthExcludedModels, newConfig.OAuthExcludedModels)
	}

	util.SetLogLevel(newConfig)
	if oldConfig != nil && oldConfig.Debug != newConfig.Debug {
		log.Debugf("log level updated - debug mode changed from %t to %t", oldConfig.Debug, newConfig.Debug)
	}

	if oldConfig != nil {
		details := diff.BuildConfigChangeDetails(oldConfig, newConfig)
		if len(details) > 0 {
			log.Debugf("config changes detected:")
			for _, d := range details {
				log.Debugf("  %s", d)
			}
		} else {
			log.Debugf("no material config field changes detected")
		}
	}

	authDirChanged := oldConfig == nil || oldConfig.AuthDir != newConfig.AuthDir
	retryConfigChanged := oldConfig != nil && (oldConfig.RequestRetry != newConfig.RequestRetry || oldConfig.MaxRetryInterval != newConfig.MaxRetryInterval || oldConfig.MaxRetryCredentials != newConfig.MaxRetryCredentials)
	forceAuthRefresh := oldConfig != nil && (oldConfig.ForceModelPrefix != newConfig.ForceModelPrefix || !reflect.DeepEqual(oldConfig.OAuthModelAlias, newConfig.OAuthModelAlias) || retryConfigChanged)

	log.Infof("config successfully reloaded, triggering client reload")
	w.reloadClients(authDirChanged, affectedOAuthProviders, forceAuthRefresh)
	return true
}
