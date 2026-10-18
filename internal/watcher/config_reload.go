// config_reload.go implements debounced configuration hot reload.
// It detects material changes and reloads clients when the config changes.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
	"gopkg.in/yaml.v3"

	log "github.com/sirupsen/logrus"
)

var loadConfigFileFunc = config.LoadConfig

// configPollInterval is how often the config file's bytes are re-checked as a
// safety net for changes no filesystem event reports.
var configPollInterval = 5 * time.Second

// failedConfigRetryInterval is how long the periodic check waits before retrying
// bytes whose reload failed.
const failedConfigRetryInterval = time.Minute

func (w *Watcher) stopConfigReloadTimer() {
	w.configReloadMu.Lock()
	if w.configReloadTimer != nil {
		w.configReloadTimer.Stop()
		w.configReloadTimer = nil
	}
	w.configReloadMu.Unlock()
}

func (w *Watcher) scheduleConfigReload() {
	w.configReloadMu.Lock()
	defer w.configReloadMu.Unlock()
	if w.configReloadTimer != nil {
		w.configReloadTimer.Stop()
	}
	w.configReloadTimer = time.AfterFunc(configReloadDebounce, func() {
		w.configReloadMu.Lock()
		w.configReloadTimer = nil
		w.configReloadMu.Unlock()
		w.reloadConfigIfChanged()
	})
}

// pollConfig re-checks the config file every configPollInterval. Filesystem events
// cannot follow every way the path can change (symlinks in ancestor directories,
// a link target that appears later, directory bind mounts, network filesystems),
// so this applies such a change within one interval. A check reads and hashes the
// file and reloads only when its bytes differ from the last ones applied.
func (w *Watcher) pollConfig(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.stopped.Load() {
				return
			}
			if w.configBytesChanged() {
				w.reloadConfigIfChanged()
			}
		}
	}
}

// seedConfigHash records the hash of the bytes the applied config was parsed from,
// so the first check does not reload an unchanged file. It never reads the file:
// a write landing after the config was loaded must still count as a change. Without
// an applied hash, the first check reloads once.
func (w *Watcher) seedConfigHash() {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	if w.lastConfigHash == "" && w.config != nil {
		w.lastConfigHash = w.config.SourceHash
	}
}

// configBytesChanged reports whether the config file now holds bytes other than the
// last ones applied. Bytes whose reload failed are retried only after
// failedConfigRetryInterval: the load re-reads the file, so the failure may belong to
// bytes written in between. Read failures are left to the event path, which logs them.
func (w *Watcher) configBytesChanged() bool {
	data, err := os.ReadFile(w.configPath)
	if err != nil || len(data) == 0 {
		return false
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	if hash == w.failedConfigHash && time.Since(w.failedConfigAt) < failedConfigRetryInterval {
		return false
	}
	return hash != w.lastConfigHash
}

// ReloadConfigIfChanged runs the same config reload path used by filesystem events.
func (w *Watcher) ReloadConfigIfChanged() {
	if w == nil {
		return
	}
	w.reloadConfigIfChanged()
}

func (w *Watcher) reloadConfigIfChanged() {
	// File events and post-save reloads may arrive together; an older read must
	// not apply after a newer one.
	w.configApplyMu.Lock()
	defer w.configApplyMu.Unlock()

	data, err := os.ReadFile(w.configPath)
	if err != nil {
		log.Errorf("failed to read config file for hash check: %v", err)
		return
	}
	if len(data) == 0 {
		log.Debugf("ignoring empty config file write event")
		return
	}
	sum := sha256.Sum256(data)
	newHash := hex.EncodeToString(sum[:])

	w.clientsMutex.RLock()
	currentHash := w.lastConfigHash
	w.clientsMutex.RUnlock()

	if currentHash != "" && currentHash == newHash {
		log.Debugf("config file content unchanged (hash match), skipping reload")
		return
	}
	log.Infof("config file changed, reloading: %s", w.configPath)
	if w.reloadConfig() {
		// Record the bytes the reload actually parsed, not the change-check read or a
		// later re-read: a write landing between them must not count as applied. A
		// write-back during load only costs one extra reload.
		w.clientsMutex.Lock()
		appliedHash := newHash
		if w.config != nil && w.config.SourceHash != "" {
			appliedHash = w.config.SourceHash
		}
		w.lastConfigHash = appliedHash
		w.failedConfigHash = ""
		w.clientsMutex.Unlock()
		w.persistConfigAsync()
		return
	}
	// Remember the bytes that failed, so the periodic check does not retry them on
	// every tick. A filesystem event still retries.
	w.clientsMutex.Lock()
	w.failedConfigHash = newHash
	w.failedConfigAt = time.Now()
	w.clientsMutex.Unlock()
}

func (w *Watcher) reloadConfig() bool {
	log.Debug("=========================== CONFIG RELOAD ============================")
	log.Debugf("starting config reload from: %s", w.configPath)

	newConfig, errLoadConfig := loadConfigFileFunc(w.configPath)
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
			log.Info("config changes detected:")
			for _, d := range details {
				log.Infof("  %s", d)
			}
		} else {
			log.Debugf("no material config field changes detected")
		}
	}

	authDirChanged := oldConfig == nil || oldConfig.AuthDir != newConfig.AuthDir
	retryConfigChanged := oldConfig != nil && (oldConfig.RequestRetry != newConfig.RequestRetry || oldConfig.MaxRetryInterval != newConfig.MaxRetryInterval || oldConfig.MaxRetryCredentials != newConfig.MaxRetryCredentials)
	forceAuthRefresh := oldConfig != nil && (oldConfig.ForceModelPrefix != newConfig.ForceModelPrefix || !reflect.DeepEqual(oldConfig.OAuthModelAlias, newConfig.OAuthModelAlias) || !reflect.DeepEqual(oldConfig.OAuthSettings, newConfig.OAuthSettings) || retryConfigChanged)

	log.Infof("config successfully reloaded, triggering client reload")
	w.reloadClients(authDirChanged, affectedOAuthProviders, forceAuthRefresh)
	return true
}
