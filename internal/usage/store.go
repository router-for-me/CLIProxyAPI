package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const defaultFlushInterval = 60 * time.Second

// UsageStore persists and restores usage snapshots.
type UsageStore interface {
	Load(ctx context.Context) (StatisticsSnapshot, error)
	Save(ctx context.Context, snapshot StatisticsSnapshot) error
}

// FileUsageStore persists usage snapshots to a JSON file.
type FileUsageStore struct {
	path string
	mu   sync.Mutex
}

// NewFileUsageStore constructs a file-backed usage store.
func NewFileUsageStore(path string) *FileUsageStore {
	return &FileUsageStore{path: path}
}

// Path returns the backing file path.
func (s *FileUsageStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load reads the latest persisted usage snapshot.
func (s *FileUsageStore) Load(ctx context.Context) (StatisticsSnapshot, error) {
	result := StatisticsSnapshot{}
	if err := checkContext(ctx); err != nil {
		return result, err
	}
	if s == nil {
		return result, errors.New("file usage store is nil")
	}
	if s.path == "" {
		return result, errors.New("file usage store path is empty")
	}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read usage snapshot %q: %w", s.path, err)
	}
	if err := checkContext(ctx); err != nil {
		return result, err
	}
	if len(data) == 0 {
		return result, nil
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("decode usage snapshot %q: %w", s.path, err)
	}

	return result, nil
}

// Save writes a snapshot atomically (tmp + rename).
func (s *FileUsageStore) Save(ctx context.Context, snapshot StatisticsSnapshot) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if s == nil {
		return errors.New("file usage store is nil")
	}
	if s.path == "" {
		return errors.New("file usage store path is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create usage snapshot directory %q: %w", dir, err)
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode usage snapshot: %w", err)
	}
	data = append(data, '\n')
	if err := checkContext(ctx); err != nil {
		return err
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d", s.path, time.Now().UnixNano())
	file, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open temp usage snapshot %q: %w", tmpPath, err)
	}

	if _, errWrite := file.Write(data); errWrite != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp usage snapshot %q: %w", tmpPath, errWrite)
	}
	if errSync := file.Sync(); errSync != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync temp usage snapshot %q: %w", tmpPath, errSync)
	}
	if errClose := file.Close(); errClose != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp usage snapshot %q: %w", tmpPath, errClose)
	}

	if err := checkContext(ctx); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename usage snapshot %q -> %q: %w", tmpPath, s.path, err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync usage snapshot directory %q: %w", dir, err)
	}

	return nil
}

// StartPeriodicFlush starts a background flush loop and returns a stop function.
// The stop function cancels the loop but does not perform a final flush.
func StartPeriodicFlush(parent context.Context, stats *RequestStatistics, store UsageStore, interval time.Duration) context.CancelFunc {
	if interval <= 0 {
		interval = defaultFlushInterval
	}
	if parent == nil {
		parent = context.Background()
	}

	ctx, cancel := context.WithCancel(parent)
	if stats == nil || store == nil {
		return cancel
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				flushCtx, flushCancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := stats.FlushToStore(flushCtx, store)
				flushCancel()
				if err != nil {
					log.WithError(err).Warn("failed to flush usage statistics")
				}
			}
		}
	}()

	return cancel
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
