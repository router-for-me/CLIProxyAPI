// config_reload.go implements debounced configuration hot reload.
// It detects material changes and reloads clients when the config changes.
package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/diff"
	"gopkg.in/yaml.v3"

	log "github.com/sirupsen/logrus"
)

const maxConfigReloadPasses = 8

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

// ReloadConfigIfChanged runs the same config reload path used by filesystem events.
func (w *Watcher) ReloadConfigIfChanged() {
	if w == nil {
		return
	}
	w.reloadConfigIfChanged()
}

func (w *Watcher) reloadConfigIfChanged() {
	w.configApplyMu.Lock()
	defer w.configApplyMu.Unlock()

	for pass := 0; pass < maxConfigReloadPasses; pass++ {
		data, errRead := os.ReadFile(w.configPath)
		if errRead != nil {
			log.Errorf("failed to read config file for hash check: %v", errRead)
			return
		}
		if len(data) == 0 {
			log.Debug("ignoring empty config file write event")
			return
		}
		newHash := configContentHash(data)

		w.clientsMutex.RLock()
		currentHash := w.lastConfigHash
		w.clientsMutex.RUnlock()

		if currentHash != "" && currentHash == newHash {
			log.Debug("config file content unchanged (hash match), skipping reload")
			return
		}
		log.Infof("config file changed, reloading: %s", w.configPath)
		applied, changedDuringLoad := w.reloadConfigAtHash(newHash)
		if changedDuringLoad {
			continue
		}
		if !applied {
			return
		}
		w.clientsMutex.Lock()
		w.lastConfigHash = newHash
		w.clientsMutex.Unlock()
		w.persistConfigAsync()
		// Loop once more while holding configApplyMu. If the file changed while
		// the callback was running, the newer content is applied before any
		// waiter can observe this reload as complete.
	}

	log.Warn("config file kept changing during reload; scheduling another pass")
	w.scheduleConfigReload()
}

func (w *Watcher) reloadConfig() bool {
	applied, _ := w.reloadConfigAtHash("")
	return applied
}

func (w *Watcher) reloadConfigAtHash(expectedHash string) (applied bool, changedDuringLoad bool) {
	log.Debug("=========================== CONFIG RELOAD ============================")
	log.Debugf("starting config reload from: %s", w.configPath)

	newConfig, errLoadConfig := config.LoadConfig(w.configPath)
	if errLoadConfig != nil {
		log.Errorf("failed to reload config: %v", errLoadConfig)
		return false, false
	}
	if expectedHash != "" {
		loadedData, errRead := os.ReadFile(w.configPath)
		if errRead != nil {
			log.Errorf("failed to verify config file after load: %v", errRead)
			return false, false
		}
		if len(loadedData) == 0 || configContentHash(loadedData) != expectedHash {
			log.Debug("config file changed while it was being loaded; retrying newest content")
			return false, true
		}
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
	forceAuthRefresh := oldConfig != nil && (oldConfig.ForceModelPrefix != newConfig.ForceModelPrefix || !reflect.DeepEqual(oldConfig.OAuthModelAlias, newConfig.OAuthModelAlias) || retryConfigChanged)

	log.Infof("config successfully reloaded, triggering client reload")
	w.reloadClients(authDirChanged, affectedOAuthProviders, forceAuthRefresh)
	return true, false
}

func configContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
