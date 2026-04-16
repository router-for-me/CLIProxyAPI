package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPostgresStoreImportConfigFromFileRejectsInvalidYAML(t *testing.T) {
	store := &PostgresStore{}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: [\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	imported, err := store.importConfigFromFile(context.Background(), configPath)
	if err == nil {
		t.Fatalf("expected importConfigFromFile() to fail for invalid yaml")
	}
	if imported {
		t.Fatalf("expected imported=false for invalid yaml")
	}
	if !strings.Contains(err.Error(), "validate local config for migration") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestChooseConfigBootstrapMode(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		localExists  bool
		localValid   bool
		localModTime time.Time
		dbExists     bool
		dbUpdatedAt  time.Time
		dbHasContent bool
		want         configBootstrapMode
	}{
		{
			name:         "import local when database missing",
			localExists:  true,
			localValid:   true,
			localModTime: now,
			dbExists:     false,
			want:         configBootstrapImportLocal,
		},
		{
			name:         "sync database when local missing",
			dbExists:     true,
			dbUpdatedAt:  now,
			dbHasContent: true,
			want:         configBootstrapSyncDatabase,
		},
		{
			name:         "sync database when local invalid",
			localExists:  true,
			localValid:   false,
			localModTime: now.Add(time.Hour),
			dbExists:     true,
			dbUpdatedAt:  now,
			dbHasContent: true,
			want:         configBootstrapSyncDatabase,
		},
		{
			name:         "sync database when database newer",
			localExists:  true,
			localValid:   true,
			localModTime: now,
			dbExists:     true,
			dbUpdatedAt:  now.Add(time.Minute),
			dbHasContent: true,
			want:         configBootstrapSyncDatabase,
		},
		{
			name:         "import local when local newer",
			localExists:  true,
			localValid:   true,
			localModTime: now.Add(time.Minute),
			dbExists:     true,
			dbUpdatedAt:  now,
			dbHasContent: true,
			want:         configBootstrapImportLocal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chooseConfigBootstrapMode(tt.localExists, tt.localValid, tt.localModTime, tt.dbExists, tt.dbUpdatedAt, tt.dbHasContent)
			if got != tt.want {
				t.Fatalf("chooseConfigBootstrapMode() = %v, want %v", got, tt.want)
			}
		})
	}
}
