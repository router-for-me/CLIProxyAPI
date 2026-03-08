package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromFile(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name          string
		setupFile     func(string) string
		checkSnapshot func(*testing.T)
	}{
		{
			name: "Happy path - load valid JSON",
			setupFile: func(dir string) string {
				path := filepath.Join(dir, "valid.json")
				snapshot := StatisticsSnapshot{
					TotalRequests: 10,
					SuccessCount:  8,
					FailureCount:  2,
					TotalTokens:   100,
				}
				data, _ := json.Marshal(snapshot)
				os.WriteFile(path, data, 0644)
				return path
			},
			checkSnapshot: func(t *testing.T) {
				stats := GetRequestStatistics().Snapshot()
				if stats.TotalRequests != 10 {
					t.Errorf("Expected TotalRequests 10, got %d", stats.TotalRequests)
				}
				if stats.TotalTokens != 100 {
					t.Errorf("Expected TotalTokens 100, got %d", stats.TotalTokens)
				}
			},
		},
		{
			name: "Edge case - file does not exist",
			setupFile: func(dir string) string {
				return filepath.Join(dir, "nonexistent.json")
			},
			checkSnapshot: func(t *testing.T) {},
		},
		{
			name: "Edge case - malformed JSON",
			setupFile: func(dir string) string {
				path := filepath.Join(dir, "malformed.json")
				os.WriteFile(path, []byte("{bad json"), 0644)
				return path
			},
			checkSnapshot: func(t *testing.T) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			GetRequestStatistics().RestoreSnapshot(StatisticsSnapshot{})
			lastSavedTotal = -1

			filePath := tt.setupFile(tmpDir)
			LoadFromFile(filePath)

			tt.checkSnapshot(t)
		})
	}
}

func TestSaveToFile(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name   string
		setup  func() string
		verify func(*testing.T, string)
	}{
		{
			name: "Happy path - save to file",
			setup: func() string {
				GetRequestStatistics().RestoreSnapshot(StatisticsSnapshot{
					TotalRequests: 5,
					TotalTokens:   50,
				})
				lastSavedTotal = -1
				return filepath.Join(tmpDir, "save_happy.json")
			},
			verify: func(t *testing.T, path string) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("Expected file to be written, got error: %v", err)
				}
				var snap StatisticsSnapshot
				if err := json.Unmarshal(data, &snap); err != nil {
					t.Fatalf("Failed to unmarshal saved JSON: %v", err)
				}
				if snap.TotalRequests != 5 {
					t.Errorf("Expected 5 total requests saved, got %d", snap.TotalRequests)
				}
			},
		},
		{
			name: "Skip save if unchanged",
			setup: func() string {
				GetRequestStatistics().RestoreSnapshot(StatisticsSnapshot{
					TotalRequests: 5,
				})
				lastSavedTotal = 5
				return filepath.Join(tmpDir, "save_unchanged.json")
			},
			verify: func(t *testing.T, path string) {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("Expected file not to be created when unchanged")
				}
			},
		},
		{
			name: "Edge case - unwritable directory",
			setup: func() string {
				GetRequestStatistics().RestoreSnapshot(StatisticsSnapshot{
					TotalRequests: 15,
				})
				lastSavedTotal = -1

				unwritableDir := filepath.Join(tmpDir, "unwritable")
				os.Mkdir(unwritableDir, 0555)
				return filepath.Join(unwritableDir, "save_error.json")
			},
			verify: func(t *testing.T, path string) {
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := tt.setup()
			SaveToFile(filePath)
			tt.verify(t, filePath)
		})
	}
}

func TestStartPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "persist.json")
	ctx, cancel := context.WithCancel(context.Background())

	done := StartPersistence(ctx, filePath, 10*time.Millisecond)

	GetRequestStatistics().RestoreSnapshot(StatisticsSnapshot{
		TotalRequests: 42,
	})
	lastSavedTotal = -1

	time.Sleep(30 * time.Millisecond)
	cancel()

	<-done

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Expected file to be written on shutdown, got error: %v", err)
	}
	var snap StatisticsSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("Failed to unmarshal saved JSON: %v", err)
	}
	if snap.TotalRequests != 42 {
		t.Errorf("Expected 42 total requests saved, got %d", snap.TotalRequests)
	}
}
