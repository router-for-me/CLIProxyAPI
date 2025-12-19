// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultStatsFileName      = "usage_statistics.json"
	defaultSaveInterval       = 5 * time.Minute
	defaultMaxDetailsPerModel = 1000
)

var (
	persistenceMu      sync.Mutex
	persistencePath    string
	persistenceEnabled bool
	autoSaveCtxCancel  context.CancelFunc
)

// persistedData is the structure saved to disk.
type persistedData struct {
	SavedAt       time.Time                    `json:"saved_at"`
	TotalRequests int64                        `json:"total_requests"`
	SuccessCount  int64                        `json:"success_count"`
	FailureCount  int64                        `json:"failure_count"`
	TotalTokens   int64                        `json:"total_tokens"`
	APIs          map[string]persistedAPIStats `json:"apis"`
	RequestsByDay map[string]int64             `json:"requests_by_day"`
	TokensByDay   map[string]int64             `json:"tokens_by_day"`
}

type persistedAPIStats struct {
	TotalRequests int64                          `json:"total_requests"`
	TotalTokens   int64                          `json:"total_tokens"`
	Models        map[string]persistedModelStats `json:"models"`
}

type persistedModelStats struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

// EnablePersistence enables statistics persistence to the specified directory.
func EnablePersistence(authDir string) error {
	persistenceMu.Lock()
	defer persistenceMu.Unlock()

	if authDir == "" {
		log.Debug("usage persistence disabled: empty auth directory")
		return nil
	}

	statsPath := filepath.Join(authDir, defaultStatsFileName)
	persistencePath = statsPath
	persistenceEnabled = true

	log.Infof("usage statistics persistence enabled: %s", statsPath)
	return nil
}

// LoadStatistics loads statistics from the persistence file.
func LoadStatistics() error {
	persistenceMu.Lock()
	path := persistencePath
	enabled := persistenceEnabled
	persistenceMu.Unlock()

	if !enabled || path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug("no existing usage statistics file found, starting fresh")
			return nil
		}
		return err
	}

	var persisted persistedData
	if err := json.Unmarshal(data, &persisted); err != nil {
		log.WithError(err).Warn("failed to parse usage statistics file, starting fresh")
		return nil
	}

	stats := GetRequestStatistics()
	if stats == nil {
		return nil
	}

	stats.mu.Lock()
	defer stats.mu.Unlock()

	stats.totalRequests = persisted.TotalRequests
	stats.successCount = persisted.SuccessCount
	stats.failureCount = persisted.FailureCount
	stats.totalTokens = persisted.TotalTokens

	if persisted.APIs != nil {
		for apiName, apiData := range persisted.APIs {
			as := &apiStats{
				TotalRequests: apiData.TotalRequests,
				TotalTokens:   apiData.TotalTokens,
				Models:        make(map[string]*modelStats),
			}
			for modelName, modelData := range apiData.Models {
				ms := &modelStats{
					TotalRequests: modelData.TotalRequests,
					TotalTokens:   modelData.TotalTokens,
					Details:       modelData.Details,
				}
				as.Models[modelName] = ms
			}
			stats.apis[apiName] = as
		}
	}

	if persisted.RequestsByDay != nil {
		for k, v := range persisted.RequestsByDay {
			stats.requestsByDay[k] = v
		}
	}

	if persisted.TokensByDay != nil {
		for k, v := range persisted.TokensByDay {
			stats.tokensByDay[k] = v
		}
	}

	log.Infof("loaded usage statistics: %d requests, %d tokens (saved at %s)",
		persisted.TotalRequests, persisted.TotalTokens, persisted.SavedAt.Format(time.RFC3339))

	return nil
}

// SaveStatistics saves the current statistics to the persistence file.
func SaveStatistics() error {
	persistenceMu.Lock()
	path := persistencePath
	enabled := persistenceEnabled
	persistenceMu.Unlock()

	if !enabled || path == "" {
		return nil
	}

	stats := GetRequestStatistics()
	if stats == nil {
		return nil
	}

	stats.mu.RLock()
	persisted := persistedData{
		SavedAt:       time.Now(),
		TotalRequests: stats.totalRequests,
		SuccessCount:  stats.successCount,
		FailureCount:  stats.failureCount,
		TotalTokens:   stats.totalTokens,
		APIs:          make(map[string]persistedAPIStats),
		RequestsByDay: make(map[string]int64),
		TokensByDay:   make(map[string]int64),
	}

	for apiName, as := range stats.apis {
		pas := persistedAPIStats{
			TotalRequests: as.TotalRequests,
			TotalTokens:   as.TotalTokens,
			Models:        make(map[string]persistedModelStats),
		}
		for modelName, ms := range as.Models {
			details := ms.Details
			if len(details) > defaultMaxDetailsPerModel {
				details = details[len(details)-defaultMaxDetailsPerModel:]
			}
			pas.Models[modelName] = persistedModelStats{
				TotalRequests: ms.TotalRequests,
				TotalTokens:   ms.TotalTokens,
				Details:       details,
			}
		}
		persisted.APIs[apiName] = pas
	}

	for k, v := range stats.requestsByDay {
		persisted.RequestsByDay[k] = v
	}

	for k, v := range stats.tokensByDay {
		persisted.TokensByDay[k] = v
	}
	stats.mu.RUnlock()

	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}

	log.Debugf("saved usage statistics: %d requests, %d tokens", persisted.TotalRequests, persisted.TotalTokens)
	return nil
}

// StartAutoSave starts a background goroutine that periodically saves statistics.
func StartAutoSave(ctx context.Context) {
	persistenceMu.Lock()
	if autoSaveCtxCancel != nil {
		persistenceMu.Unlock()
		return
	}

	autoCtx, cancel := context.WithCancel(ctx)
	autoSaveCtxCancel = cancel
	enabled := persistenceEnabled
	persistenceMu.Unlock()

	if !enabled {
		return
	}

	go func() {
		ticker := time.NewTicker(defaultSaveInterval)
		defer ticker.Stop()

		for {
			select {
			case <-autoCtx.Done():
				log.Debug("auto-save stopped")
				return
			case <-ticker.C:
				if err := SaveStatistics(); err != nil {
					log.WithError(err).Warn("failed to auto-save usage statistics")
				}
			}
		}
	}()

	log.Infof("usage statistics auto-save started (interval: %s)", defaultSaveInterval)
}

// StopAutoSave stops the auto-save goroutine and performs a final save.
func StopAutoSave() {
	persistenceMu.Lock()
	cancel := autoSaveCtxCancel
	autoSaveCtxCancel = nil
	persistenceMu.Unlock()

	if cancel != nil {
		cancel()
	}

	if err := SaveStatistics(); err != nil {
		log.WithError(err).Warn("failed to save usage statistics on shutdown")
	} else {
		log.Info("usage statistics saved on shutdown")
	}
}
