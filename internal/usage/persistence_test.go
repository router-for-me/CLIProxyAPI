package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestPersistenceManager_StartupEnabled_LoadsSnapshotAndRuns(t *testing.T) {
	tmpDir := t.TempDir()
	stats := NewRequestStatistics()
	manager := NewPersistenceManager(stats, tmpDir)
	t.Cleanup(func() { manager.Stop(false) })

	persistPath := filepath.Join(tmpDir, "usage.json")
	payload := persistencePayload{
		Version: 1,
		SavedAt: time.Now().UTC(),
		Usage: StatisticsSnapshot{
			APIs: map[string]APISnapshot{
				"api-key": {
					Models: map[string]ModelSnapshot{
						"gpt": {
							Details: []RequestDetail{{
								Timestamp: time.Now().UTC(),
								Tokens:    TokenStats{TotalTokens: 10},
							}},
						},
					},
				},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := os.WriteFile(persistPath, data, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	manager.ApplyConfig(config.UsagePersistenceConfig{
		Enabled:         true,
		FilePath:        "usage.json",
		IntervalSeconds: 60,
	})

	status := manager.Status()
	if !status.Enabled {
		t.Fatalf("expected enabled status")
	}
	if status.LastLoadedAt.IsZero() {
		t.Fatalf("expected LastLoadedAt to be set")
	}
	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("expected merged total requests 1, got %d", snapshot.TotalRequests)
	}
}

func TestPersistenceManager_EnableToDisable_StopsRunningWithoutReconfigure(t *testing.T) {
	manager := NewPersistenceManager(NewRequestStatistics(), t.TempDir())
	t.Cleanup(func() { manager.Stop(false) })

	cfg := config.UsagePersistenceConfig{Enabled: true, FilePath: "usage.json", IntervalSeconds: 60}
	manager.ApplyConfig(cfg)
	if !manager.running {
		t.Fatalf("expected running after enable")
	}

	cfg.Enabled = false
	manager.ApplyConfig(cfg)
	if manager.running {
		t.Fatalf("expected running=false after disable with same path/interval")
	}
}

func TestPersistenceManager_ReconfigureInterval_RestartsLoop(t *testing.T) {
	manager := NewPersistenceManager(NewRequestStatistics(), t.TempDir())
	t.Cleanup(func() { manager.Stop(false) })

	manager.ApplyConfig(config.UsagePersistenceConfig{Enabled: true, FilePath: "usage.json", IntervalSeconds: 60})

	manager.mu.Lock()
	firstStopCh := manager.stopCh
	manager.mu.Unlock()

	manager.ApplyConfig(config.UsagePersistenceConfig{Enabled: true, FilePath: "usage.json", IntervalSeconds: 120})

	manager.mu.Lock()
	secondStopCh := manager.stopCh
	manager.mu.Unlock()

	if firstStopCh == nil || secondStopCh == nil {
		t.Fatalf("expected non-nil stop channels before/after reconfigure")
	}
	if firstStopCh == secondStopCh {
		t.Fatalf("expected stop channel to change after interval reconfigure")
	}
}

func TestPersistenceManager_StopFlushDisabled_DoesNotWriteSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	persistPath := filepath.Join(tmpDir, "usage.json")
	manager := NewPersistenceManager(NewRequestStatistics(), tmpDir)

	manager.ApplyConfig(config.UsagePersistenceConfig{
		Enabled:         false,
		FilePath:        "usage.json",
		IntervalSeconds: 30,
	})
	manager.Stop(true)

	if _, err := os.Stat(persistPath); !os.IsNotExist(err) {
		t.Fatalf("expected no persistence file when disabled stop flush; err=%v", err)
	}
}

func TestPersistenceManager_LoadNow_AcceptsLegacyVersionZero(t *testing.T) {
	tmpDir := t.TempDir()
	stats := NewRequestStatistics()
	manager := NewPersistenceManager(stats, tmpDir)
	t.Cleanup(func() { manager.Stop(false) })

	persistPath := filepath.Join(tmpDir, "usage.json")
	payload := persistencePayload{
		Version: 0,
		SavedAt: time.Now().UTC(),
		Usage: StatisticsSnapshot{
			APIs: map[string]APISnapshot{
				"legacy-api": {
					Models: map[string]ModelSnapshot{
						"legacy-model": {
							Details: []RequestDetail{{
								Timestamp: time.Now().UTC(),
								Tokens:    TokenStats{TotalTokens: 2},
							}},
						},
					},
				},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := os.WriteFile(persistPath, data, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	manager.ApplyConfig(config.UsagePersistenceConfig{
		Enabled:         true,
		FilePath:        "usage.json",
		IntervalSeconds: 60,
	})

	snapshot := stats.Snapshot()
	if _, ok := snapshot.APIs["legacy-api"]; !ok {
		t.Fatalf("expected v0 snapshot to be loaded")
	}
}

func TestPersistenceManager_LoadNow_CorruptedFileReturnsErrorAndRecoversAfterRewrite(t *testing.T) {
	tmpDir := t.TempDir()
	stats := NewRequestStatistics()
	manager := NewPersistenceManager(stats, tmpDir)
	t.Cleanup(func() { manager.Stop(false) })

	persistPath := filepath.Join(tmpDir, "usage.json")
	if err := os.WriteFile(persistPath, []byte("{invalid-json"), 0o600); err != nil {
		t.Fatalf("write corrupted payload: %v", err)
	}

	manager.ApplyConfig(config.UsagePersistenceConfig{
		Enabled:         true,
		FilePath:        "usage.json",
		IntervalSeconds: 60,
	})

	status := manager.Status()
	if status.LastError == "" {
		t.Fatalf("expected corrupted file load error to be recorded")
	}

	if _, err := manager.SaveNow(); err != nil {
		t.Fatalf("save after corrupted load failed: %v", err)
	}
	if _, err := manager.LoadNow(); err != nil {
		t.Fatalf("load after rewrite should succeed: %v", err)
	}

	status = manager.Status()
	if status.LastError != "" {
		t.Fatalf("expected last error to be cleared after successful load, got: %q", status.LastError)
	}
}

func TestPersistenceManager_ConcurrentApplyConfigAndStop_NoPanicOrDeadlock(t *testing.T) {
	manager := NewPersistenceManager(NewRequestStatistics(), t.TempDir())
	t.Cleanup(func() { manager.Stop(false) })

	manager.ApplyConfig(config.UsagePersistenceConfig{Enabled: true, FilePath: "usage.json", IntervalSeconds: 1})

	const goroutines = 24
	const iterations = 40

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("panic in goroutine %d: %v", idx, r)
				}
			}()

			for j := 0; j < iterations; j++ {
				enabled := (j+idx)%2 == 0
				manager.ApplyConfig(config.UsagePersistenceConfig{
					Enabled:         enabled,
					FilePath:        "usage.json",
					IntervalSeconds: 1 + ((j + idx) % 2),
				})
				if (j+idx)%3 == 0 {
					manager.Stop(false)
				}
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("concurrent apply/stop timed out, possible deadlock")
	}

	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent apply/stop failed: %v", err)
		}
	}

	status := manager.Status()
	if strings.TrimSpace(status.Path) == "" {
		t.Fatalf("expected manager to keep a resolved path after concurrent updates")
	}
}
