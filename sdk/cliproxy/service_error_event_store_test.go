package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
)

func TestInitErrorEventStore_DegradesWhenMongoInitializationFails(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	writeBaseConfigFile(t, configPath)
	statePath := filepath.Join(tempDir, "state-store.local.ini")
	content := []byte("[mongo]\nenabled = true\nuri = ://bad-uri\ndatabase = cliproxy_state\n")
	if err := os.WriteFile(statePath, content, 0o600); err != nil {
		t.Fatalf("write state-store.local.ini: %v", err)
	}

	svc := &Service{configPath: configPath}
	if err := svc.initErrorEventStore(context.Background()); err != nil {
		t.Fatalf("initErrorEventStore() error = %v, want nil degradation", err)
	}
	if svc.errorEventStore != nil {
		t.Fatal("svc.errorEventStore should stay nil after degraded init")
	}
	if store := mongostate.GetGlobalErrorEventStore(); store != nil {
		t.Fatalf("global error event store = %#v, want nil", store)
	}
}
