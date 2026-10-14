// config_reload.go implements debounced configuration hot reload.
// It detects material changes and reloads clients when the config changes.
package watcher

import (
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
	// Serialize reloads so a debounced filesystem reload and an explicit reload
	// cannot interleave their hash bookkeeping.
	w.configLoadMu.Lock()
	defer w.configLoadMu.Unlock()

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
		// Record the hash of the content observed before loading, never a later
		// re-read: a write landing after the load would otherwise be marked as
		// applied and skipped. A load that rewrites the file costs one extra
		// reload, which then converges.
		w.clientsMutex.Lock()
		w.lastConfigHash = newHash
		w.clientsMutex.Unlock()
		w.persistConfigAsync()
	}
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
