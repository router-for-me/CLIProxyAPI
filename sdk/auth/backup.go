package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const backupSubDir = "401-bak"

// MoveToBackup moves the auth file for the given auth record into the 401-bak
// subdirectory under the configured auth directory. The operation is idempotent:
// if the file is already in 401-bak or does not exist, it returns nil.
func (s *FileTokenStore) MoveToBackup(ctx context.Context, auth *cliproxyauth.Auth) error {
	if auth == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	srcPath, err := s.resolveAuthPath(auth)
	if err != nil {
		return fmt.Errorf("resolve auth path: %w", err)
	}
	if srcPath == "" {
		log.Debugf("401-bak: skipping %s — no resolvable path", maskSensitiveID(auth.ID))
		return nil
	}

	if _, statErr := os.Stat(srcPath); os.IsNotExist(statErr) {
		return nil
	}

	dir := filepath.Dir(srcPath)
	if filepath.Base(dir) == backupSubDir {
		return nil
	}

	backupDir := filepath.Join(dir, backupSubDir)
	if err = os.MkdirAll(backupDir, 0o700); err != nil {
		return fmt.Errorf("create 401-bak dir: %w", err)
	}

	baseName := filepath.Base(srcPath)
	dstPath := filepath.Join(backupDir, baseName)

	if _, statErr := os.Stat(dstPath); statErr == nil {
		dstPath = deduplicatePath(backupDir, baseName)
	}

	maskedID := maskSensitiveID(auth.ID)
	log.Infof("401-bak: moving auth %s to %s", maskedID, backupSubDir)

	if err = os.Rename(srcPath, dstPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		log.Errorf("401-bak: failed to move %s: %v", maskedID, err)
		return fmt.Errorf("move to 401-bak: %w", err)
	}

	log.Infof("401-bak: successfully moved %s -> %s", maskedID, filepath.Base(dstPath))
	return nil
}

// BackupScanner periodically queries the Manager for auth entries that are
// in unauthorized (401) state and moves their backing files to 401-bak.
type BackupScanner struct {
	store    *FileTokenStore
	manager  *cliproxyauth.Manager
	interval time.Duration
	stopCh   chan struct{}
	once     sync.Once
}

// NewBackupScanner creates a scanner that runs at the specified interval.
func NewBackupScanner(store *FileTokenStore, manager *cliproxyauth.Manager, interval time.Duration) *BackupScanner {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &BackupScanner{
		store:    store,
		manager:  manager,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the periodic scan loop in a background goroutine.
func (bs *BackupScanner) Start() {
	go bs.run()
}

// Stop terminates the background scan loop.
func (bs *BackupScanner) Stop() {
	bs.once.Do(func() { close(bs.stopCh) })
}

func (bs *BackupScanner) run() {
	ticker := time.NewTicker(bs.interval)
	defer ticker.Stop()

	for {
		select {
		case <-bs.stopCh:
			return
		case <-ticker.C:
			bs.scan()
		}
	}
}

func (bs *BackupScanner) scan() {
	if bs.manager == nil || bs.store == nil {
		return
	}

	auths := bs.manager.List()

	moved := 0
	ctx := context.Background()
	for _, auth := range auths {
		if auth == nil || auth.StatusMessage != "unauthorized" {
			continue
		}
		if moveErr := bs.store.MoveToBackup(ctx, auth); moveErr != nil {
			log.Warnf("401-bak scan: move failed for %s: %v", maskSensitiveID(auth.ID), moveErr)
		} else {
			moved++
		}
	}

	if moved > 0 {
		log.Infof("401-bak scan: moved %d unauthorized auth file(s)", moved)
	}
}

func deduplicatePath(dir, baseName string) string {
	ext := filepath.Ext(baseName)
	nameOnly := strings.TrimSuffix(baseName, ext)
	ts := time.Now().Format("20060102-150405.000000000")
	return filepath.Join(dir, fmt.Sprintf("%s_%s%s", nameOnly, ts, ext))
}

func maskSensitiveID(id string) string {
	if len(id) <= 8 {
		return "***"
	}
	return id[:4] + "***" + id[len(id)-3:]
}
