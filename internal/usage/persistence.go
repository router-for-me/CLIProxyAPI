package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

// LoadFromFile reads the usage statistics from a JSON file and overwrites the in-memory store.
func LoadFromFile(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Errorf("Failed to read usage statistics file: %v", err)
		}
		return
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		log.Errorf("Failed to parse usage statistics file: %v", err)
		return
	}
	GetRequestStatistics().RestoreSnapshot(snapshot)
	lastSavedTotal = snapshot.TotalRequests
	log.Infof("Loaded usage statistics from %s", filePath)
}

var lastSavedTotal int64 = -1

// SaveToFile writes the current usage statistics snapshot to a JSON file.
func SaveToFile(filePath string) {
	stats := GetRequestStatistics()
	currentTotal := stats.GetTotalRequests()

	if lastSavedTotal != -1 && currentTotal == lastSavedTotal {
		return
	}

	// Encode to memory buffer first to minimize lock contention duration
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := stats.WriteJSON(enc); err != nil {
		log.Errorf("Failed to encode usage statistics: %v", err)
		return
	}

	tempFile := filePath + ".tmp"
	if err := os.WriteFile(tempFile, buf.Bytes(), 0644); err != nil {
		log.Errorf("Failed to write temp usage file: %v", err)
		return
	}

	if err := os.Rename(tempFile, filePath); err != nil {
		log.Errorf("Failed to rename temp usage file: %v", err)
		return
	}

	lastSavedTotal = currentTotal
}

// StartPersistence starts a background goroutine to periodically save usage statistics.
func StartPersistence(ctx context.Context, filePath string, interval time.Duration) <-chan struct{} {
	LoadFromFile(filePath)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				SaveToFile(filePath)
			case <-ctx.Done():
				SaveToFile(filePath) // Final save on graceful shutdown
				return
			}
		}
	}()
	return done
}
