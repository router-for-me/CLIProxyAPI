package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type usageEventWriter struct {
	dir           string
	retentionDays int
}

func newUsageEventWriter(dir string, retentionDays int) *usageEventWriter {
	return &usageEventWriter{
		dir:           dir,
		retentionDays: retentionDays,
	}
}

func (w *usageEventWriter) write(event UsageEvent) error {
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return fmt.Errorf("create usage event directory: %w", err)
	}
	if err := os.Chmod(w.dir, 0o700); err != nil {
		return fmt.Errorf("set usage event directory permissions: %w", err)
	}

	path := filepath.Join(w.dir, "usage-events-"+resolveUsageEventMonth(event)+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open usage event ledger: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("set usage event ledger permissions: %w; close usage event ledger: %w", err, closeErr)
		}
		return fmt.Errorf("set usage event ledger permissions: %w", err)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("marshal usage event: %w; close usage event ledger: %w", err, closeErr)
		}
		return fmt.Errorf("marshal usage event: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := file.Write(payload); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("write usage event ledger: %w; close usage event ledger: %w", err, closeErr)
		}
		return fmt.Errorf("write usage event ledger: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close usage event ledger: %w", err)
	}

	if err := w.cleanupExpiredLedgers(); err != nil {
		return fmt.Errorf("cleanup usage event ledgers: %w", err)
	}
	return nil
}

func resolveUsageEventMonth(event UsageEvent) string {
	requestedAt, err := time.Parse(time.RFC3339Nano, event.RequestedAt)
	if err != nil {
		requestedAt = time.Now()
	}
	return requestedAt.Format("2006-01")
}

func (w *usageEventWriter) cleanupExpiredLedgers() error {
	if w.retentionDays <= 0 {
		return nil
	}

	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return fmt.Errorf("list usage event ledgers: %w", err)
	}
	cutoff := time.Now().AddDate(0, 0, -w.retentionDays)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "usage-events-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		path := filepath.Join(w.dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat usage event ledger %s: %w", path, err)
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("remove expired usage event ledger %s: %w", path, err)
		}
	}
	return nil
}
